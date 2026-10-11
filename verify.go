package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// 試還原：把剛寫好的封存真的匯入成一個暫時的 distro，在裡面檢查，然後移除。
// 匯入的是原封不動的封存內容，只在結尾多接了覆寫用的設定檔，讓這個複本不會啟動任何服務
// （見 tarscan.go 的 inertWSLConf）。

// 試還原沒有通過或沒有做的原因。
const (
	reasonNoIndex   = "no-index"   // 備份時掃描器讀不懂這份封存
	reasonEtc       = "etc"        // 封存裡的 /etc 不是一般的目錄，覆寫設定檔不可靠
	reasonNoSpace   = "no-space"   // 放暫時 distro 的磁碟空間不夠
	reasonChanged   = "changed"    // 封存和備份當時不一樣了
	reasonUnread    = "unreadable" // 封存讀不出來
	reasonImport    = "import"     // wsl --import 失敗
	reasonOverride  = "override"   // 覆寫沒有生效
	reasonNotInert  = "not-inert"  // 複本啟動了 systemd、掛了 Windows 磁碟，或開了互通
	reasonCheck     = "check"      // 檢查腳本沒有跑完
	reasonUser      = "user"       // 預設使用者或家目錄不見了
	reasonSamples   = "samples"    // 檔案內容和備份時記下的不一樣
	reasonBase      = "base"       // 差異所依據的完整備份不在了，或不是當初那一份
	importTimeout   = 12 * time.Hour
	checkTimeout    = 15 * time.Minute
	verifySpaceSlop = 1 << 30
)

// skipped 回報這個原因是不是「沒有做」而不是「做了沒過」。
func skippedReason(reason string) bool {
	return reason == reasonNoIndex || reason == reasonEtc || reason == reasonNoSpace
}

// verifySpaceNeeded 是試還原需要的可用空間：暫時的虛擬磁碟最大不會超過 tar 的大小多少。
func verifySpaceNeeded(tarSize int64) uint64 {
	return uint64(tarSize) + uint64(tarSize)/20 + verifySpaceSlop
}

type checkReport struct {
	Done          bool
	PID1          string
	Systemd       bool
	WindowsDrives int
	Interop       bool
	WSLConf       string
	User          string // ok、missing、no-home；沒有檢查時是空的
	SamplesOK     int
	SamplesBad    int
	SizesOK       int
	SizesBad      int
	BadLines      []string
}

func parseCheck(text string) checkReport {
	var r checkReport
	for _, row := range parseProto(text) {
		arg := ""
		if len(row) > 1 {
			arg = row[1]
		}
		switch row[0] {
		case "done":
			r.Done = true
		case "pid1":
			r.PID1 = arg
		case "systemd":
			r.Systemd = arg != "no"
		case "windows-drives":
			r.WindowsDrives, _ = strconv.Atoi(arg)
		case "interop":
			r.Interop = arg != "no"
		case "wslconf":
			r.WSLConf = arg
		case "user":
			r.User = arg
		case "samples":
			if len(row) > 2 {
				r.SamplesOK, _ = strconv.Atoi(row[1])
				r.SamplesBad, _ = strconv.Atoi(row[2])
			}
		case "sample-bad":
			r.BadLines = append(r.BadLines, strings.Join(row[1:], " "))
		case "sizes":
			if len(row) > 2 {
				r.SizesOK, _ = strconv.Atoi(row[1])
				r.SizesBad, _ = strconv.Atoi(row[2])
			}
		case "size-bad":
			r.BadLines = append(r.BadLines, "size of "+strings.Join(row[1:], " "))
		}
	}
	return r
}

// inert 回報複本是不是真的沒有活起來。
func (r checkReport) inert() bool {
	want := sha256.Sum256([]byte(inertWSLConf))
	return r.PID1 != "systemd" && !r.Systemd && r.WindowsDrives == 0 && !r.Interop &&
		r.WSLConf == hex.EncodeToString(want[:])
}

// judgeCheck 依檢查腳本的結果下結論；通過時 reason 是空字串。
func judgeCheck(r checkReport, samples, sizes int) (reason, detail string) {
	switch {
	case !r.Done:
		return reasonCheck, "the check script did not finish"
	case !r.inert():
		return reasonNotInert, fmt.Sprintf("pid1=%s systemd=%v drives=%d interop=%v wslconf=%s",
			r.PID1, r.Systemd, r.WindowsDrives, r.Interop, r.WSLConf)
	case r.User == "missing" || r.User == "no-home":
		return reasonUser, "default user: " + r.User
	case r.SamplesBad > 0 || r.SamplesOK != samples:
		return reasonSamples, fmt.Sprintf("%d of %d files match; %s", r.SamplesOK, samples, strings.Join(r.BadLines, "; "))
	case r.SizesBad > 0 || r.SizesOK != sizes:
		return reasonSamples, fmt.Sprintf("%d of the %d largest files have the right size; %s", r.SizesOK, sizes, strings.Join(r.BadLines, "; "))
	}
	return "", ""
}

// verifySource 是送去匯入的那條 tar，以及匯入之後才能下的結論。
type verifySource struct {
	// stream 是整個 distro 的 tar，結尾接著覆寫用的項目。
	stream io.Reader
	// tailStart 回傳我們接上去的那一段從串流的哪裡開始；還不知道時是 -1。
	tailStart func() int64
	// settle 在匯入結束後呼叫（failed 表示匯入沒成功）：封存有問題時回傳原因，沒問題回傳空字串。
	settle func(failed bool) (reason, detail string)
	close  func()
	// samples 與 largest 是匯入後要比對的檔案，settle 之後才齊全。
	samples []fileSample
	largest []fileSize
}

// fullSource：完整備份的封存原封不動，只在結尾接上覆寫用的項目。
func fullSource(m *manifest) (*verifySource, error) {
	a, err := openArchive(m.archivePath())
	if err != nil {
		return nil, err
	}
	src := &verifySource{close: a.Close, samples: m.Index.Samples, largest: m.Index.Largest}
	src.stream = newOverrideReader(a, m.Index.EndOffset, buildOverrideTail())
	src.tailStart = func() int64 { return m.Index.EndOffset }
	src.settle = func(bool) (string, string) {
		// 不管匯入成不成功，都把檔案剩下的部分算進雜湊：封存本身有問題的話，那比「匯入失敗」更接近原因。
		if _, err := io.Copy(io.Discard, a.hashed); err != nil {
			return reasonUnread, err.Error()
		}
		if got := hex.EncodeToString(a.sum.Sum(nil)); got != m.SHA256 {
			return reasonChanged, "sha256 is " + got
		}
		return "", ""
	}
	return src, nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// mergedSource：差異的還原點要把完整備份與差異合併起來，再接上覆寫用的項目。
func mergedSource(m *manifest) *verifySource {
	pr, pw := io.Pipe()
	var start atomic.Int64
	start.Store(-1)
	// 完整備份當時抽樣的檔案，只有還留在這個還原點裡的才能拿來比對。
	interesting := map[string]bool{}
	for _, s := range m.base.Index.Samples {
		interesting[s.Path] = true
	}
	for _, f := range m.base.Index.Largest {
		interesting[f.Path] = true
	}
	kept := map[string]bool{}
	done := make(chan error, 1)
	go func() {
		out := &countingWriter{w: pw}
		err := writeMerged(m, out, mergeOptions{OnBase: func(name string, dropped bool) {
			if !dropped && interesting[name] {
				kept[name] = true
			}
		}})
		if err == nil {
			// 兩個封存都讀完、雜湊也對了，才接上結尾；少了結尾的串流不會被當成完整的。
			start.Store(out.n)
			_, err = pw.Write(buildOverrideTail())
		}
		pw.CloseWithError(err)
		done <- err
	}()
	src := &verifySource{stream: pr, tailStart: start.Load, close: func() { pr.Close() }}
	src.settle = func(failed bool) (string, string) {
		// 匯入先結束的話，讓還在寫的那一端跟著停下來。
		pr.CloseWithError(io.ErrClosedPipe)
		err := <-done
		var changed *errArchiveChanged
		switch {
		case err == nil:
			src.samples = append([]fileSample{}, m.Index.Samples...)
			for _, s := range m.base.Index.Samples {
				if kept[s.Path] {
					src.samples = append(src.samples, s)
				}
			}
			src.largest = append([]fileSize{}, m.Index.Largest...)
			for _, f := range m.base.Index.Largest {
				if kept[f.Path] {
					src.largest = append(src.largest, f)
				}
			}
			return "", ""
		case errors.As(err, &changed):
			return reasonChanged, err.Error()
		case errors.Is(err, errMergeMismatch):
			return reasonBase, err.Error()
		case !failed:
			return reasonUnread, err.Error()
		}
		// 匯入先失敗了，合併是被我們打斷的：看看是不是哪個封存本身已經不對。
		for _, part := range []*manifest{m.base, m} {
			sum, err := fileSHA256(part.archivePath())
			if err != nil {
				return reasonUnread, err.Error()
			}
			if sum != part.SHA256 {
				return reasonChanged, part.Archive + ": sha256 is " + sum
			}
		}
		return "", ""
	}
	return src
}

// recordVerify 把驗證的結果寫回 manifest。
func recordVerify(m *manifest, res *verifyResult, began time.Time, reason, detail string) *verifyResult {
	res.Reason, res.Detail, res.OK = reason, detail, reason == ""
	if skippedReason(reason) {
		res.Mode = verifyModeSkipped
	}
	res.Seconds = time.Since(began).Seconds()
	m.Verify = res
	if err := m.save(); err != nil {
		logf("verify %s: cannot save the manifest: %v", m.ID, err)
	}
	logf("verify %s: ok=%v mode=%s reason=%q detail=%q %.0fs", m.ID, res.OK, res.Mode, reason, detail, res.Seconds)
	return res
}

// runCheck 是差異備份的輕量檢查：不匯入，只確認「之後要還原時，需要的東西都在而且沒變」。
// 差異本身從頭讀一遍（解得開、走得完、項目數與雜湊都對），它的完整備份還在、大小沒變，
// 「哪些項目不要了」的清單完好。完整備份的內容不在這裡重讀：那要把整份再讀一次，留給定期的合併試還原。
func runCheck(m *manifest) *verifyResult {
	began := time.Now()
	res := &verifyResult{At: began.UTC(), Mode: verifyModeCheck}
	finish := func(reason, detail string) *verifyResult { return recordVerify(m, res, began, reason, detail) }
	if !m.isDiff() || m.orphan() {
		return finish(reasonBase, "")
	}
	if info, err := os.Stat(m.base.archivePath()); err != nil || info.Size() != m.base.Size {
		return finish(reasonBase, "the archive of the full backup is missing or has another size")
	}
	drop, err := unpackBits(m.Drop)
	if err != nil || int64(len(drop))*8 < m.BaseEntries || countBits(drop) != m.DropCount {
		return finish(reasonChanged, "the list of dropped entries is damaged")
	}
	if reason, detail := checkArchive(m); reason != "" {
		return finish(reason, detail)
	}
	if m.Index != nil {
		res.Samples = len(m.Index.Samples)
	}
	return finish("", "")
}

// runVerify 對一份備份做試還原，並把結果寫回它的 manifest。
// 差異的還原點是把它和它的完整備份合併之後匯入：試的正是之後還原時會走的那條路。
func runVerify(m *manifest, onProgress func(tarBytes int64)) *verifyResult {
	began := time.Now()
	res := &verifyResult{At: began.UTC(), Mode: verifyModeRestore}
	finish := func(reason, detail string) *verifyResult { return recordVerify(m, res, began, reason, detail) }

	if m.orphan() {
		return finish(reasonBase, "")
	}
	if m.Index == nil || (m.isDiff() && m.base.Index == nil) {
		return finish(reasonNoIndex, "")
	}
	if !m.Index.EtcIsDir {
		return finish(reasonEtc, "")
	}
	root, err := verifyRoot()
	if err != nil {
		return finish(reasonImport, err.Error())
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return finish(reasonImport, err.Error())
	}
	sweepStale(root)
	if vol, err := volumeOf(root); err == nil {
		if need := verifySpaceNeeded(m.tarSize()); vol.Free < need {
			return finish(reasonNoSpace, fmt.Sprintf("%d bytes free on %s, %d needed", vol.Free, vol.Root, need))
		}
	}

	name, err := newVerifyName(began)
	if err != nil {
		return finish(reasonImport, err.Error())
	}
	if err := writeClaim(root, name); err != nil {
		return finish(reasonImport, err.Error())
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		runSystem(ctx, "", system32("wsl.exe"), "--terminate", name)
		cancel()
		if err := unregisterVerifyDistro(root, name); err != nil {
			// 留給下一次執行的 sweepStale 再試。
			logf("verify %s: cleanup of %s failed: %v", m.ID, name, err)
		}
	}()

	// 讀檔 → 算整個檔案的雜湊 → 解壓 → 接上覆寫項目 → 一份送去匯入，一份送去確認覆寫確實在最後面。
	var src *verifySource
	if m.isDiff() {
		src = mergedSource(m)
	} else if src, err = fullSource(m); err != nil {
		return finish(reasonUnread, err.Error())
	}
	defer src.close()
	sent := &progressReader{r: src.stream}

	gateR, gateW := io.Pipe()
	type gateResult struct {
		check overrideCheck
		err   error
	}
	gate := make(chan gateResult, 1)
	go func() {
		check, err := scanOverride(gateR, src.tailStart)
		// 不管結果如何都把剩下的讀完，免得寫的那一端卡住。
		io.Copy(io.Discard, gateR)
		gate <- gateResult{check, err}
	}()

	stop, reporterGone := make(chan struct{}), make(chan struct{})
	if onProgress == nil {
		close(reporterGone)
	} else {
		go func() {
			defer close(reporterGone)
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					onProgress(sent.n.Load())
				}
			}
		}()
	}

	ctx, cancel := context.WithTimeout(context.Background(), importTimeout)
	importErr := importFromReader(ctx, name, filepath.Join(root, name), io.TeeReader(sent, gateW))
	cancel()
	close(stop)
	<-reporterGone
	gateW.Close()
	gated := <-gate

	// 先看是不是封存本身的問題：那比「匯入失敗」更接近原因。
	if reason, detail := src.settle(importErr != nil); reason != "" {
		return finish(reason, detail)
	}
	if importErr != nil {
		return finish(reasonImport, importErr.Error())
	}
	// 到這裡之前，暫時 distro 只是被匯入，還沒有啟動過。覆寫沒有確認生效就不啟動它。
	if gated.err != nil || !gated.check.inert() {
		return finish(reasonOverride, fmt.Sprintf("%s, err=%v", gated.check.summary(), gated.err))
	}

	var list strings.Builder
	for _, s := range src.samples {
		list.WriteString(s.SHA256 + "  " + s.Path + "\n")
	}
	uid := ""
	if m.DefaultUID != 0 {
		uid = strconv.FormatUint(uint64(m.DefaultUID), 10)
	}
	var sizes strings.Builder
	for _, f := range src.largest {
		sizes.WriteString(strconv.FormatInt(f.Size, 10) + "\t" + f.Path + "\n")
	}
	out, err := runInDistro(name, withVars(checkScript,
		scriptVar{"WSLBAK_SAMPLES", list.String()},
		scriptVar{"WSLBAK_SIZES", sizes.String()},
		scriptVar{"WSLBAK_UID", uid},
	), checkTimeout)
	report := parseCheck(decodeWSLText(out))
	res.Samples = report.SamplesOK
	if reason, detail := judgeCheck(report, len(src.samples), len(src.largest)); reason != "" {
		if err != nil {
			detail += " (" + err.Error() + ": " + firstLine(decodeWSLText(out)) + ")"
		}
		return finish(reason, detail)
	}
	return finish("", "")
}
