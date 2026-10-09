package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/pgzip"
)

// 備份不呼叫 wsl --export（它會先把執行中的 distro 終止）。
// 改成在 distro 裡以 root 執行 tar，把原始的 tar 從 stdout 送出來，在 Windows 這邊壓縮、算雜湊、寫檔。
// distro 全程照常運作，備份對它是唯讀的。

const (
	// stallLimit：這麼久沒有收到任何資料就放棄。備份可以跑很久，所以不設總時間上限，只看有沒有在動。
	stallLimit = 10 * time.Minute
	// maxWarnings 是寫進 manifest 的 tar 警告行數上限；總數另外記在 WarningCount。
	maxWarnings = 20
)

// suspendGap：兩次檢查之間隔了這麼久，代表我們自己被暫停過（電腦睡眠、進入待命、
// 行程被系統凍結），而不是資料停了。
const suspendGap = 30 * time.Second

// stallWatch 判斷「資料是不是停住了」。每秒看一次已經讀到的位元組數：
// 超過 stallLimit 都沒有增加才算停住。筆電闔上蓋子再打開時，中間那段不能算在裡面，
// 否則一醒來就會被誤判成停住而把備份取消。
type stallWatch struct {
	bytes    int64
	lastMove time.Time
	lastTick time.Time
}

func newStallWatch(now time.Time) *stallWatch {
	return &stallWatch{bytes: -1, lastMove: now, lastTick: now}
}

// observe 記下這一次看到的位元組數。moved 表示有新的資料；stuck 表示已經停住太久。
func (w *stallWatch) observe(now time.Time, bytes int64) (moved, stuck bool) {
	if now.Sub(w.lastTick) > suspendGap {
		// 剛從暫停中醒來：重新開始計時。
		w.lastMove = now
	}
	w.lastTick = now
	if bytes != w.bytes {
		w.bytes, w.lastMove = bytes, now
		return true, false
	}
	return false, now.Sub(w.lastMove) > stallLimit
}

// 備份失敗的種類。訊息由呼叫端依種類從 catalog 取，Detail 是英文的細節，寫進紀錄檔。
type backupFailure int

const (
	failStart backupFailure = iota
	failNotGNUTar
	failNotTar
	failWrite
	failStalled
	failTruncated
	failTar
)

type backupError struct {
	Kind   backupFailure
	Detail string
}

func (e *backupError) Error() string { return e.Detail }

var (
	totalsRe = regexp.MustCompile(`^Total bytes written: (\d+)`)

	// benignTar 是 live 備份時一定會遇到、不代表備份失敗的 tar 訊息（LC_ALL=C 下的原文）：
	// 檔案在讀取途中被改寫、被刪除、變短。
	benignTar = []*regexp.Regexp{
		regexp.MustCompile(`: file changed as we read it$`),
		regexp.MustCompile(`: File removed before we read it$`),
		regexp.MustCompile(`: Cannot (stat|open): No such file or directory$`),
		regexp.MustCompile(`: File shrank by \d+ bytes; padding with zeros$`),
		// root 也讀不了的只有沒開 allow_root 的 FUSE 掛載點，那裡本來就不在這個檔案系統上。
		regexp.MustCompile(`: Cannot stat: Permission denied$`),
	}
	// 結尾的總結行，本身不帶資訊。
	tarSummary = regexp.MustCompile(`^tar: Exiting with failure status due to previous errors$`)
)

// classifyTarStderr 把 tar 寫到 stderr 的訊息分成可以接受的警告與真正的錯誤。
func classifyTarStderr(lines []string) (warnings, fatal []string) {
next:
	for _, line := range lines {
		if tarSummary.MatchString(line) {
			continue
		}
		for _, re := range benignTar {
			if re.MatchString(line) {
				warnings = append(warnings, line)
				continue next
			}
		}
		fatal = append(fatal, line)
	}
	return warnings, fatal
}

// tarSucceeded 依結束碼與訊息判斷 tar 算不算成功。
// 0 是成功；1 是「有檔案在讀取時變動」；2 在 live 的系統上也會因為目錄中途被刪而出現，
// 所以 2 只有在每一行訊息都屬於已知的無害種類時才放行。
func tarSucceeded(code int, warnings, fatal []string) bool {
	switch code {
	case 0, 1:
		return len(fatal) == 0
	case 2:
		return len(fatal) == 0 && len(warnings) > 0
	}
	return false
}

// stderrLog 收集腳本寫到 stderr 的內容。
type stderrLog struct {
	mu     sync.Mutex
	proto  [][]string
	tar    []string
	notes  []string // wsl.exe 自己的警告
	totals int64
}

func (l *stderrLog) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if rest, ok := strings.CutPrefix(line, protoPrefix); ok {
		l.proto = append(l.proto, strings.Split(rest, "\t"))
		return
	}
	if m := totalsRe.FindStringSubmatch(line); m != nil {
		l.totals, _ = strconv.ParseInt(m[1], 10, 64)
		return
	}
	// wsl.exe 自己的警告（例如「無法啟動 systemd 的使用者工作階段」）也寫在 stderr，
	// 內容會被翻譯，但一律以「wsl: 」開頭。它們不是 tar 的訊息，不能拿來判斷備份成不成功。
	if strings.HasPrefix(line, "wsl: ") {
		l.notes = append(l.notes, line)
		return
	}
	// tar 可能對同一個樹印出非常多行，只留前面一些，其餘的只計數。
	if len(l.tar) < 1000 {
		l.tar = append(l.tar, line)
	}
}

type progressReader struct {
	r io.Reader
	n atomic.Int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n.Add(int64(n))
	return n, err
}

// stickyWriter 記住第一個寫入錯誤：tar 的資料是經由 TeeReader 流過來的，
// 寫檔失敗（例如磁碟滿了）和 tar 本身讀不懂要分得出來。
type stickyWriter struct {
	w   io.Writer
	err error
}

func (s *stickyWriter) Write(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	n, err := s.w.Write(p)
	if err != nil {
		s.err = err
	}
	return n, err
}

// compressWorkers 是同時壓縮的執行緒數。排程執行時只用一半的核心：
// 補跑的備份可能發生在使用者正在工作的時候。
func compressWorkers(scheduled bool) int {
	n := runtime.NumCPU()
	if scheduled {
		n = max(n/2, 1)
	}
	return n
}

var partialRe = regexp.MustCompile(`^\d{8}T\d{6}Z\.tar\.gz\.partial$`)

// cleanPartials 清掉上次沒寫完的檔案。只認我們自己的檔名格式，而且呼叫時握有全域鎖，
// 所以不會刪到正在寫的檔案。
func cleanPartials(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && partialRe.MatchString(e.Name()) {
			if os.Remove(filepath.Join(dir, e.Name())) == nil {
				logf("removed leftover %s", e.Name())
			}
		}
	}
}

type backupRequest struct {
	Distro     regDistro
	Dir        string // 這個 distro 的備份資料夾
	Excludes   []string
	WSL        string
	Scheduled  bool
	OnProgress func(tarBytes int64) // 可以是 nil
}

// runBackup 做一次備份，成功時回傳已經寫好的 manifest。
func runBackup(req backupRequest) (*manifest, error) {
	began := time.Now()
	if err := os.MkdirAll(req.Dir, 0o755); err != nil {
		return nil, &backupError{failWrite, err.Error()}
	}
	cleanPartials(req.Dir)

	// 編號精確到秒；同一秒內的第二次備份等到下一秒。
	id := began.UTC().Format(idLayout)
	for readManifest(req.Dir, id) != nil {
		time.Sleep(time.Second)
		id = time.Now().UTC().Format(idLayout)
	}
	m := &manifest{
		Schema: manifestSchema, ID: id, Distro: req.Distro.Name, DistroID: req.Distro.GUID,
		Created: began.UTC(), Tool: version, WSL: req.WSL, Archive: id + archiveSuffix,
		DefaultUID: req.Distro.DefaultUID, Flags: req.Distro.Flags, Excludes: req.Excludes, dir: req.Dir,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	script := withVars(backupScript,
		scriptVar{"WSLBAK_ROOT", "/"},
		scriptVar{"WSLBAK_EXCLUDES", strings.Join(req.Excludes, "\n")},
	)
	var stderr stderrLog
	stream, err := startInDistro(ctx, req.Distro.Name, script, stderr.add)
	if err != nil {
		return nil, &backupError{failStart, err.Error()}
	}

	// 開頭不是 tar：distro 啟動不了、或 distro 裡的 tar 不是 GNU tar。
	if head, _ := stream.Stdout.Peek(tarBlock); !looksLikeTar(head) {
		rest, _ := io.ReadAll(io.LimitReader(stream.Stdout, 64<<10))
		// 不管它還想寫多少都不再讀了；先結束它，Wait 才不會卡在沒人讀的管線上。
		cancel()
		code, _ := stream.Wait()
		if f, ok := protoValue(stderr.proto, "error"); ok && len(f) > 0 && f[0] == "not-gnu-tar" {
			return nil, &backupError{failNotGNUTar, strings.Join(f[1:], " ")}
		}
		detail := firstLine(decodeWSLText(rest))
		if len(stderr.tar) > 0 {
			detail = stderr.tar[0]
		}
		return nil, &backupError{failNotTar, fmt.Sprintf("exit %d: %s", code, detail)}
	}

	partial := filepath.Join(req.Dir, id+partialSuffix)
	file, err := os.Create(partial)
	if err != nil {
		cancel()
		stream.Wait()
		return nil, &backupError{failWrite, err.Error()}
	}
	keep := false
	defer func() {
		if !keep {
			file.Close()
			os.Remove(partial)
		}
	}()

	progress := &progressReader{r: stream.Stdout}
	var stalled atomic.Bool
	done, watcherGone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watcherGone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		watch := newStallWatch(time.Now())
		for {
			select {
			case <-done:
				return
			case now := <-ticker.C:
				moved, stuck := watch.observe(now, progress.n.Load())
				if moved && req.OnProgress != nil {
					req.OnProgress(progress.n.Load())
				}
				if stuck {
					stalled.Store(true)
					cancel()
					return
				}
			}
		}
	}()

	sum := sha256.New()
	sink := &stickyWriter{w: io.MultiWriter(file, sum)}
	buffered := bufio.NewWriterSize(sink, 1<<20)
	zw := pgzip.NewWriter(buffered)
	// 每塊 1 MiB。區塊越大，同時在壓縮的區塊佔的記憶體越多；1 MiB 時大約是「執行緒數 × 幾 MiB」。
	zw.SetConcurrency(1<<20, compressWorkers(req.Scheduled))
	// 檔案索引和封存一起寫；寫不出來不影響備份本身，之後需要時可以再從封存建立。
	indexPath := filepath.Join(req.Dir, id+indexSuffix)
	index, indexErr := newIndexWriter(indexPath, id)
	var onEntry func(indexEntry)
	if indexErr == nil {
		onEntry = index.add
		defer func() {
			if err := index.close(keep); err != nil {
				logf("file index for %s: %v", id, err)
			}
		}()
	} else {
		logf("file index for %s: %v", id, indexErr)
	}
	idx, scanErr := scanTar(io.TeeReader(progress, zw), onEntry)
	closeErr := zw.Close()
	if err := buffered.Flush(); closeErr == nil {
		closeErr = err
	}
	if err := file.Sync(); closeErr == nil {
		closeErr = err
	}
	close(done)
	<-watcherGone
	if sink.err != nil || closeErr != nil {
		// 寫不進去了：不要讓 tar 繼續把整個 distro 讀完。
		cancel()
	}
	code, waitErr := stream.Wait()
	info, statErr := file.Stat()
	if err := file.Close(); closeErr == nil {
		closeErr = err
	}

	switch {
	case stalled.Load():
		return nil, &backupError{failStalled, fmt.Sprintf("no data for %v after %d bytes", stallLimit, idx.Size)}
	case sink.err != nil:
		return nil, &backupError{failWrite, sink.err.Error()}
	case closeErr != nil:
		return nil, &backupError{failWrite, closeErr.Error()}
	case statErr != nil:
		return nil, &backupError{failWrite, statErr.Error()}
	case waitErr != nil:
		return nil, &backupError{failTruncated, waitErr.Error()}
	}

	// 腳本要自己報告 tar 的結束碼並走到最後；少了任何一個就代表它中途死掉了。
	_, finished := protoValue(stderr.proto, "done")
	if _, reported := protoValue(stderr.proto, "tar-exit"); !reported || !finished {
		return nil, &backupError{failTruncated, fmt.Sprintf("the script did not finish (exit %d)", code)}
	}
	// 收到的位元組數要和 tar 自己說它寫出的一樣多。
	if stderr.totals != idx.Size {
		return nil, &backupError{failTruncated, fmt.Sprintf("received %d bytes, tar wrote %d", idx.Size, stderr.totals)}
	}
	warnings, fatal := classifyTarStderr(stderr.tar)
	if !tarSucceeded(code, warnings, fatal) {
		detail := fmt.Sprintf("tar exit %d", code)
		if len(fatal) > 0 {
			detail += ": " + fatal[0]
		}
		return nil, &backupError{failTar, detail}
	}

	if scanErr != nil {
		// tar 說它成功了，資料也完整收到，只是我們的掃描器讀不懂（例如超大的延伸標頭）。
		// 備份照樣留下，但沒有索引就不能試還原。
		logf("tar scan failed, the backup cannot be test-restored: %v", scanErr)
	} else {
		m.Index = &idx
	}
	for _, row := range stderr.proto {
		switch row[0] {
		case "dropped":
			m.Dropped = append(m.Dropped, strings.Join(row[1:], " "))
		case "skipped-mount":
			m.SkippedMounts = append(m.SkippedMounts, strings.Join(row[1:], " "))
		}
	}
	for _, note := range stderr.notes {
		logf("wsl.exe: %s", note)
	}
	m.WarningCount = len(warnings)
	m.Warnings = warnings[:min(len(warnings), maxWarnings)]
	m.Size = info.Size()
	m.SHA256 = hex.EncodeToString(sum.Sum(nil))
	m.Seconds = time.Since(began).Seconds()

	if err := os.Rename(partial, m.archivePath()); err != nil {
		return nil, &backupError{failWrite, err.Error()}
	}
	keep = true
	if err := m.save(); err != nil {
		// 沒有 manifest 的封存不算備份；把它刪掉，不要留一個沒人認得的大檔案。
		os.Remove(m.archivePath())
		// 讓收尾的那幾段把檔案索引也當成失敗來處理。
		keep = false
		return nil, &backupError{failWrite, err.Error()}
	}
	logf("backup %s written: tar %d bytes, archive %d bytes, %d entries, %d warnings, %.0fs",
		m.ID, idx.Size, m.Size, idx.Entries, m.WarningCount, m.Seconds)
	return m, nil
}
