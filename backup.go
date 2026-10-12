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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/pgzip"
)

// 備份不呼叫 wsl --export（它會先把執行中的 distro 終止）。
// 改成在 distro 裡以 root 執行 tar，把原始的 tar 從 stdout 送出來，在 Windows 這邊壓縮、算雜湊、寫檔。
// distro 全程照常運作，它的磁碟不會被寫入。唯一寫進 distro 的東西是差異模式下的清單暫存檔，
// 放在 /run（它不在記憶體裡的話改放 /dev/shm），讀完就刪（見 backup.sh 與 snapshot.go）。

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
	return false, now.Sub(w.lastMove) > currentStallLimit()
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
		// 檔案在 tar 看過它之後才被刪掉：它照樣進了封存（有內容的從已經打開的檔案讀，空檔不必讀），
		// 只是接著用路徑去查 ACL 與延伸屬性時找不到它。tar 自己標成 Warning。
		// file_has_acl_at 與 llistxattrat 兩種是在 tar 1.35 上實際看到的（收到這種警告的 30 個檔案
		// 都在封存裡）；其餘三個是 tar 同一段程式裡同樣用路徑查的呼叫。
		// 只認「No such file or directory」：其他原因（例如讀取錯誤）照樣算錯誤。
		regexp.MustCompile(`: Warning: Cannot (file_has_acl_at|acl_get_file_at|llistxattrat|lgetxattrat|lgetfileconat): No such file or directory$`),
		regexp.MustCompile(`: File shrank by \d+ bytes; padding with zeros$`),
		// root 也讀不了的只有沒開 allow_root 的 FUSE 掛載點，那裡本來就不在這個檔案系統上。
		regexp.MustCompile(`: Cannot stat: Permission denied$`),
	}
	// 結尾的總結行，本身不帶資訊。
	tarSummary = regexp.MustCompile(`^tar: Exiting with failure status due to previous errors$`)
)

// tar 寫到 stderr 的一行屬於哪一種。
type tarLineKind int

const (
	tarLineFatal   tarLineKind = iota // 真正的錯誤：不在已知的無害種類裡的，一律算這種
	tarLineBenign                     // 在執行中的系統上正常會出現的警告
	tarLineSummary                    // 結尾的總結行，本身不帶資訊
)

func classifyTarLine(line string) tarLineKind {
	if tarSummary.MatchString(line) {
		return tarLineSummary
	}
	for _, re := range benignTar {
		if re.MatchString(line) {
			return tarLineBenign
		}
	}
	return tarLineFatal
}

// classifyTarStderr 把 tar 寫到 stderr 的訊息分成可以接受的警告與真正的錯誤。
func classifyTarStderr(lines []string) (warnings, fatal []string) {
	for _, line := range lines {
		switch classifyTarLine(line) {
		case tarLineBenign:
			warnings = append(warnings, line)
		case tarLineFatal:
			fatal = append(fatal, line)
		}
	}
	return warnings, fatal
}

// tarSucceeded 依結束碼與訊息判斷 tar 算不算成功。
// 0 是成功；1 是「有檔案在讀取時變動」；2 在 live 的系統上也會因為目錄中途被刪而出現，
// 所以 2 只有在每一行訊息都屬於已知的無害種類時才放行。
func tarSucceeded(code int, warnings, fatal []string) bool {
	return tarExitOK(code, len(warnings), len(fatal))
}

// tarExitOK 和 tarSucceeded 相同，吃的是兩種訊息的行數。
func tarExitOK(code, warnings, fatal int) bool {
	switch code {
	case 0, 1:
		return fatal == 0
	case 2:
		return fatal == 0 && warnings > 0
	}
	return false
}

// maxFatalKept 是留下來的 tar 錯誤行數上限；判斷成敗看的是總數，留下來的只用來說明。
const maxFatalKept = 20

// stderrLog 收集腳本寫到 stderr 的內容。
type stderrLog struct {
	mu    sync.Mutex
	proto [][]string
	// first 是 tar（或 sh）說的第一行，不管屬於哪一種：連封存的開頭都沒有來的時候，拿它說明原因。
	first string
	// tar 的訊息一到就分類，各自計數，只留前面幾行。先分類再決定留不留：
	// 一個真正的錯誤不會因為排在上千行無害的警告後面而被丟掉。
	warnings     []string
	warningCount int
	fatal        []string
	fatalCount   int
	notes        []string // wsl.exe 自己的警告
	totals       int64
	// vanished 是 tar 走到時已經不見的名稱（tar 的寫法，還沒還原跳脫）。
	vanished []string
}

// vanishedRe：tar 走到一個名稱、卻發現它已經不見時說的話（LC_ALL=C 下的原文）。
var vanishedRe = regexp.MustCompile(`^tar: (.+): (?:File removed before we read it|Cannot (?:stat|open): No such file or directory)$`)

// maxVanished 是記下來的「已經不見的名稱」數量上限。超過的不記：差異備份會因為解釋不了它們而改做完整備份。
const maxVanished = 100000

func (l *stderrLog) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// 檔名要原樣留著去和清單比對，所以趕在 plain 之前取。
	if m := vanishedRe.FindStringSubmatch(line); m != nil && len(l.vanished) < maxVanished {
		l.vanished = append(l.vanished, m[1])
	}
	// 這些行來自 distro 裡，可能帶著檔名；之後會顯示在畫面上、寫進紀錄檔。
	line = plain(line)
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
	if l.first == "" {
		l.first = line
	}
	// tar 可能對同一棵樹印出非常多行：每一種都只留前面幾行，其餘的只計數。
	switch classifyTarLine(line) {
	case tarLineBenign:
		l.warningCount++
		if len(l.warnings) < maxWarnings {
			l.warnings = append(l.warnings, line)
		}
	case tarLineFatal:
		l.fatalCount++
		if len(l.fatal) < maxFatalKept {
			l.fatal = append(l.fatal, line)
		}
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

var partialRe = regexp.MustCompile(`^\d{8}T\d{6}Z\.(tar\.gz|diff\.tar\.gz|idx\.gz|dirs\.gz)\.(partial|new)$`)

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
	// OnStage 在開始「列出檔案」這種沒有進度可報的步驟時呼叫（可以是 nil）。
	OnStage func(stage string)

	// 以下只和差異備份有關。Mode 不是 diff 時，這次備份和 0.1.x 做的完全一樣。
	Mode      string
	FullEvery int
	ForceFull bool
	Verify    string // 設定裡的 verify
}

const (
	stageListing = "listing"
	stageReading = "reading"
)

func (req *backupRequest) stage(name string) {
	if req.OnStage != nil {
		req.OnStage(name)
	}
}

func (req *backupRequest) scriptVars(more ...scriptVar) []scriptVar {
	return append([]scriptVar{
		{"WSLBAK_ROOT", "/"},
		{"WSLBAK_EXCLUDES", strings.Join(req.Excludes, "\n")},
	}, more...)
}

// kindQuery 把這次備份的設定整理成 chooseKind 要的樣子。
func (req *backupRequest) kindQuery(backups []*manifest) kindQuery {
	return kindQuery{
		Mode: req.Mode, ForceFull: req.ForceFull, FullEvery: req.FullEvery, Verify: req.Verify,
		Excludes: req.Excludes, Backups: backups, Now: time.Now().Add(testAge()),
		ArchiveSize: func(b *manifest) int64 {
			info, err := os.Stat(b.archivePath())
			if err != nil {
				return -1
			}
			return info.Size()
		},
	}
}

// newBackupID 取一個還沒用過的編號。編號精確到秒；同一秒內的第二次備份等到下一秒。
func newBackupID(dir string) string {
	id := time.Now().UTC().Format(idLayout)
	for fileExists(filepath.Join(dir, id+".json")) {
		time.Sleep(time.Second)
		id = time.Now().UTC().Format(idLayout)
	}
	return id
}

// tarPass 是「在 distro 裡跑一趟 tar，把它寫成一個封存檔」需要的東西。
type tarPass struct {
	Distro     string
	Dir        string
	ID         string
	Suffix     string // 封存檔的副檔名
	Vars       []scriptVar
	Scheduled  bool
	OnProgress func(tarBytes int64)
	// OnEntry 在 tar 寫出每個項目時呼叫一次（可以是 nil）。
	OnEntry func(indexEntry)
}

// passResult 是一趟 tar 的結果。封存還叫 .partial；呼叫端決定要留（keep）還是丟（discard）。
type passResult struct {
	Index        tarIndex
	ScanErr      error // 掃描器看不懂這條 tar；封存本身是完整的
	Size         int64
	SHA256       string
	Proto        [][]string
	Warnings     []string
	WarningCount int
	// Vanished 是 tar 走到時已經不見的名稱（tar 的寫法）。
	Vanished []string

	partial string
	index   *indexWriter
}

// keep 把封存改成正式的檔名，並把檔案索引寫完。
func (r *passResult) keep(archive string) error {
	if err := os.Rename(r.partial, archive); err != nil {
		r.discard()
		return err
	}
	r.partial = ""
	if r.index != nil {
		if err := r.index.close(true); err != nil {
			logf("file index for %s: %v", filepath.Base(archive), err)
		}
		r.index = nil
	}
	return nil
}

// discard 丟掉這一趟寫出來的東西。
func (r *passResult) discard() {
	if r.partial != "" {
		os.Remove(r.partial)
		r.partial = ""
	}
	if r.index != nil {
		r.index.close(false)
		r.index = nil
	}
}

func (r *passResult) rows(key string) []string {
	var out []string
	for _, row := range r.Proto {
		if row[0] == key {
			out = append(out, strings.Join(row[1:], " "))
		}
	}
	return out
}

// runTarPass 在 distro 裡跑一趟 tar，壓縮、算雜湊，寫成 <編號><副檔名>.partial，並確認 tar 真的把它寫完了。
func runTarPass(p tarPass) (*passResult, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr stderrLog
	stream, err := startInDistro(ctx, p.Distro, withTestStartDelay(withVars(backupScript, p.Vars...)), stderr.add)
	if err != nil {
		return nil, &backupError{failStart, err.Error()}
	}

	// 從第一個位元組之前就有人看著：distro 啟動不了、WSL 整個不回應時，連開頭都不會來，
	// 等開頭的那一步不能沒有時限。讀到的位元組數在這段期間一直是 0，超過時限就照「停住了」處理。
	progress := &progressReader{r: withTestReadRate(stream.Stdout)}
	var stalled atomic.Bool
	finished, watcherGone := make(chan struct{}), make(chan struct{})
	var stopOnce sync.Once
	stopWatch := func() {
		stopOnce.Do(func() {
			close(finished)
			<-watcherGone
		})
	}
	defer stopWatch()
	go func() {
		defer close(watcherGone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		watch := newStallWatch(time.Now())
		for {
			select {
			case <-finished:
				return
			case now := <-ticker.C:
				moved, stuck := watch.observe(now, progress.n.Load())
				if moved && p.OnProgress != nil {
					p.OnProgress(progress.n.Load())
				}
				if stuck {
					stalled.Store(true)
					cancel()
					return
				}
			}
		}
	}()

	// 開頭不是 tar：distro 啟動不了、或 distro 裡的 tar 不是 GNU tar。
	if head, _ := stream.Stdout.Peek(tarBlock); !looksLikeTar(head) {
		rest, _ := io.ReadAll(io.LimitReader(stream.Stdout, 64<<10))
		// 不管它還想寫多少都不再讀了；先結束它，Wait 才不會卡在沒人讀的管線上。
		cancel()
		code, _ := stream.Wait()
		stopWatch()
		if stalled.Load() {
			return nil, &backupError{failStalled, fmt.Sprintf("no data for %v, not even the first block", currentStallLimit())}
		}
		if f, ok := protoValue(stderr.proto, "error"); ok && len(f) > 0 && f[0] == "not-gnu-tar" {
			return nil, &backupError{failNotGNUTar, strings.Join(f[1:], " ")}
		}
		detail := plain(firstLine(decodeWSLText(rest)))
		if stderr.first != "" {
			detail = stderr.first
		}
		return nil, &backupError{failNotTar, fmt.Sprintf("exit %d: %s", code, detail)}
	}

	res := &passResult{partial: filepath.Join(p.Dir, p.ID+p.Suffix+partialSuffix)}
	file, err := os.Create(res.partial)
	if err != nil {
		cancel()
		stream.Wait()
		return nil, &backupError{failWrite, err.Error()}
	}
	done := false
	defer func() {
		if !done {
			file.Close()
			res.discard()
		}
	}()

	sum := sha256.New()
	sink := &stickyWriter{w: io.MultiWriter(withTestFaults(file), sum)}
	buffered := bufio.NewWriterSize(sink, 1<<20)
	zw := pgzip.NewWriter(buffered)
	// 每塊 1 MiB。區塊越大，同時在壓縮的區塊佔的記憶體越多；1 MiB 時大約是「執行緒數 × 幾 MiB」。
	zw.SetConcurrency(1<<20, compressWorkers(p.Scheduled))
	// 檔案索引和封存一起寫；寫不出來不影響備份本身，之後需要時可以再從封存建立。
	index, indexErr := newIndexWriter(filepath.Join(p.Dir, p.ID+indexSuffix), p.ID)
	if indexErr == nil {
		res.index = index
	} else {
		logf("file index for %s: %v", p.ID, indexErr)
	}
	onEntry := func(e indexEntry) {
		if res.index != nil {
			res.index.add(e)
		}
		if p.OnEntry != nil {
			p.OnEntry(e)
		}
	}
	// tar 的輸出先經過 sizeFixReader：8 GiB 以上的檔案要把大小補進標頭，WSL 的匯入才讀得對。
	fixed := newSizeFixReader(progress)
	idx, scanErr := scanTar(io.TeeReader(fixed, zw), onEntry)
	if fixed.Fixed > 0 {
		logf("backup %s: wrote the size of %d file(s) of 8 GiB or more into their tar headers", p.ID, fixed.Fixed)
	}
	closeErr := zw.Close()
	if err := buffered.Flush(); closeErr == nil {
		closeErr = err
	}
	if err := file.Sync(); closeErr == nil {
		closeErr = err
	}
	stopWatch()
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
		return nil, &backupError{failStalled, fmt.Sprintf("no data for %v after %d bytes", currentStallLimit(), idx.Size)}
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
	_, reachedEnd := protoValue(stderr.proto, "done")
	if _, reported := protoValue(stderr.proto, "tar-exit"); !reported || !reachedEnd {
		return nil, &backupError{failTruncated, fmt.Sprintf("the script did not finish (exit %d)", code)}
	}
	// 收到的位元組數要和 tar 自己說它寫出的一樣多。
	if stderr.totals != idx.Size {
		return nil, &backupError{failTruncated, fmt.Sprintf("received %d bytes, tar wrote %d", idx.Size, stderr.totals)}
	}
	if !tarExitOK(code, stderr.warningCount, stderr.fatalCount) {
		detail := fmt.Sprintf("tar exit %d", code)
		if len(stderr.fatal) > 0 {
			detail += ": " + stderr.fatal[0]
		}
		return nil, &backupError{failTar, detail}
	}
	for _, note := range stderr.notes {
		logf("wsl.exe: %s", note)
	}
	res.Index, res.ScanErr = idx, scanErr
	res.Size, res.SHA256 = info.Size(), hex.EncodeToString(sum.Sum(nil))
	res.Proto, res.Vanished = stderr.proto, stderr.vanished
	res.WarningCount = stderr.warningCount
	res.Warnings = stderr.warnings
	done = true
	return res, nil
}

// fill 把一趟 tar 的結果填進 manifest。
func (m *manifest) fill(res *passResult, began time.Time) {
	if res.ScanErr != nil {
		// tar 說它成功了，資料也完整收到，只是我們的掃描器讀不懂（例如超大的延伸標頭）。
		// 備份照樣留下，但沒有索引就不能試還原。
		logf("tar scan failed, the backup cannot be test-restored: %v", res.ScanErr)
	} else {
		idx := res.Index
		m.Index = &idx
	}
	m.Dropped = res.rows("dropped")
	m.SkippedMounts = res.rows("skipped-mount")
	m.WarningCount, m.Warnings = res.WarningCount, res.Warnings
	m.Size, m.SHA256 = res.Size, res.SHA256
	m.Seconds = time.Since(began).Seconds()
}

// listLimit 是「列出檔案」這一趟的時間上限。它在走完整棵樹之前什麼都不會送出來，
// 所以不能用「多久沒有資料」來判斷它是不是停住了。
const listLimit = time.Hour

// listLog 收集清單那一趟寫到 stderr 的內容。
type listLog struct {
	mu      sync.Mutex
	proto   [][]string
	sockets []string
	// vanished 是「走到一半檔案不見了」這類訊息的數量；other 是其他任何訊息。
	vanished int
	other    []string
}

func (l *listLog) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if rest, ok := strings.CutPrefix(line, protoPrefix); ok {
		l.proto = append(l.proto, strings.Split(plain(rest), "\t"))
		return
	}
	if strings.HasPrefix(line, "wsl: ") || tarSummary.MatchString(line) {
		return
	}
	if m := socketLineRe.FindStringSubmatch(line); m != nil {
		// 檔名要原樣留著去和清單比對，所以不經過 plain。
		if len(l.sockets) < 100000 {
			l.sockets = append(l.sockets, m[1])
		}
		return
	}
	for _, re := range benignTar {
		if re.MatchString(line) {
			l.vanished++
			return
		}
	}
	if len(l.other) < 20 {
		l.other = append(l.other, plain(line))
	}
}

// runListing 讓 distro 裡的 tar 把整棵樹空跑一遍，回傳清單與順便帶回來的資訊。
// 任何一點不對勁都回傳錯誤：清單是用來判斷「哪些檔案被刪了」的，寧可沒有，也不要一份不完整的。
func runListing(req *backupRequest) (*listing, distroFacts, clockInfo, error) {
	req.stage(stageListing)
	began := time.Now()
	clock := clockInfo{Windows: began.Unix()}
	ctx, cancel := context.WithTimeout(context.Background(), listLimit)
	defer cancel()
	var stderr listLog
	stream, err := startInDistro(ctx, req.Distro.Name, withVars(backupScript, req.scriptVars(scriptVar{"WSLBAK_MODE", "list"})...), stderr.add)
	if err != nil {
		return nil, distroFacts{}, clock, err
	}
	list, parseErr := parseSnapshot(stream.Stdout)
	code, waitErr := stream.Wait()
	facts := factsFromProto(stderr.proto)
	facts.Clock += testClockShift()
	clock.Distro = facts.Clock
	fail := func(format string, args ...any) (*listing, distroFacts, clockInfo, error) {
		return nil, facts, clock, fmt.Errorf(format, args...)
	}
	if waitErr != nil {
		return fail("the listing did not end: %v", waitErr)
	}
	if f, ok := protoValue(stderr.proto, "error"); ok {
		return fail("the listing could not start: %s", strings.Join(f, " "))
	}
	_, reachedEnd := protoValue(stderr.proto, "done")
	size, reported := protoValue(stderr.proto, "snapshot-bytes")
	if !reachedEnd || !reported || len(size) != 1 {
		return fail("the listing script did not finish (exit %d)", code)
	}
	if len(stderr.other) > 0 {
		return fail("tar said something unexpected while listing: %s", stderr.other[0])
	}
	// 走到一半有檔案不見了，tar 會以 1 或 2 結束；那在執行中的系統上是正常的。
	if code != 0 && (code > 2 || stderr.vanished == 0) {
		return fail("tar exit %d while listing", code)
	}
	if parseErr != nil {
		return fail("%v", parseErr)
	}
	if want, _ := strconv.ParseInt(size[0], 10, 64); want != list.bytes {
		return fail("received %d bytes of listing, the distro wrote %d", list.bytes, want)
	}
	if facts.Clock <= 0 {
		return fail("the distro did not report its clock")
	}
	if err := list.validate(); err != nil {
		return fail("%v", err)
	}
	unmatched := 0
	for _, quoted := range stderr.sockets {
		if !list.noteSocket(quoted) {
			unmatched++
		}
	}
	logf("listing of %s: %d directories, %d names, %d sockets (%d not matched), %d bytes, %.0fs",
		req.Distro.Name, len(list.dirs), list.entries(), len(stderr.sockets), unmatched, list.bytes, time.Since(began).Seconds())
	return list, facts, clock, nil
}

// runBackup 做一次備份，成功時回傳已經寫好的 manifest。
// 差異模式開著而這次卻做了完整備份時，why 是原因代碼（見 diff.go）。
func runBackup(req backupRequest) (m *manifest, why string, err error) {
	if err := os.MkdirAll(req.Dir, 0o755); err != nil {
		return nil, "", &backupError{failWrite, err.Error()}
	}
	cleanPartials(req.Dir)
	if req.Mode != modeDiff {
		m, err = runFull(&req, nil)
		return m, "", err
	}

	base, why := chooseKind(req.kindQuery(listBackups(req.Dir)))
	if base != nil {
		var detail string
		m, why, detail, err = runDiff(&req, base)
		if err != nil || m != nil {
			return m, "", err
		}
		logf("%s: no differential this time (%s): %s", req.Distro.Name, why, detail)
	}
	m, err = runFull(&req, &why)
	return m, why, err
}

// listed 是清單那一趟的結果。
type listed struct {
	list  *listing
	facts distroFacts
	clock clockInfo
}

// runFull 做一份完整備份。why 不是 nil 表示差異模式開著：這份完整備份要能當之後差異的基準，
// 所以打包前後各列一次清單，把兩次都是同一個 inode 的目錄記下來。
// 清單拿不到不算備份失敗：完整備份照樣是一份可以還原的備份，只是不能當基準，下一次會再做一份完整的。
func runFull(req *backupRequest, why *string) (*manifest, error) {
	var before *listed
	if why != nil {
		list, facts, clock, err := runListing(req)
		if err != nil {
			logf("%s: %v; this full backup cannot be the base of a differential", req.Distro.Name, err)
			if *why == "" {
				*why = whyListing
			}
		} else {
			before = &listed{list, facts, clock}
		}
	}

	began := time.Now()
	id := newBackupID(req.Dir)
	m := &manifest{
		Schema: fullSchema, ID: id, Distro: req.Distro.Name, DistroID: req.Distro.GUID,
		Created: began.UTC(), Tool: version, WSL: req.WSL, Archive: id + archiveSuffix,
		DefaultUID: req.Distro.DefaultUID, Flags: req.Distro.Flags, Excludes: req.Excludes, dir: req.Dir,
	}
	var dumped map[string]int64
	pass := tarPass{Distro: req.Distro.Name, Dir: req.Dir, ID: id, Suffix: archiveSuffix,
		Vars: req.scriptVars(), Scheduled: req.Scheduled, OnProgress: req.OnProgress}
	if before != nil {
		dumped = map[string]int64{}
		pass.OnEntry = func(e indexEntry) {
			if e.Type == typeDir {
				dumped[e.name()] = e.ctime
			}
		}
	}
	req.stage(stageReading)
	res, err := runTarPass(pass)
	if err != nil {
		return nil, err
	}
	m.fill(res, began)

	if before != nil && res.ScanErr == nil {
		if after, _, _, err := runListing(req); err != nil {
			logf("%s: %v; this full backup cannot be the base of a differential", req.Distro.Name, err)
		} else if sum, err := writeDirs(m.dirsPath(), id, before.list, after, dumped); err != nil {
			logf("%s: record of directories: %v", id, err)
		} else {
			m.DirsSHA256 = sum
			clock := before.clock
			m.Clock, m.Since, m.TarVersion = &clock, sinceFor(clock), before.facts.TarVersion
			for point := range before.facts.Mounts.Points {
				m.Mounts = append(m.Mounts, point)
			}
			sort.Strings(m.Mounts)
		}
		m.Seconds = time.Since(began).Seconds()
	}

	if err := res.keep(m.archivePath()); err != nil {
		os.Remove(m.dirsPath())
		return nil, &backupError{failWrite, err.Error()}
	}
	if err := m.save(); err != nil {
		// 沒有 manifest 的封存不算備份；把它刪掉，不要留一個沒人認得的大檔案。
		os.Remove(m.archivePath())
		os.Remove(m.indexPath())
		os.Remove(m.dirsPath())
		return nil, &backupError{failWrite, err.Error()}
	}
	logf("backup %s written: tar %d bytes, archive %d bytes, %d entries, %d warnings, %.0fs",
		m.ID, res.Index.Size, m.Size, res.Index.Entries, m.WarningCount, m.Seconds)
	return m, nil
}

// runDiff 試著做一份差異備份。三種結果：
//   - 寫好了：回傳 manifest。
//   - 這次不能做差異：m 是 nil，why 是原因，什麼都沒有留下；呼叫端接著做完整備份。
//   - 真的失敗了（tar 出錯、磁碟滿了）：回傳 err。
func runDiff(req *backupRequest, base *manifest) (m *manifest, why, detail string, err error) {
	baseDirs, otherDirs, err := readDirs(base.dirsPath(), base.DirsSHA256)
	if err != nil {
		return nil, whyBaseDamaged, err.Error(), nil
	}
	if err := ensureIndex(base); err != nil {
		return nil, whyBaseDamaged, "the file index of the full backup: " + err.Error(), nil
	}
	list, facts, clock, err := runListing(req)
	if err != nil {
		// 同一個目錄走到兩次時清單會不成一棵樹；掛載表看得出原因的話，說那個原因。
		if len(facts.Mounts.Aliases) > 0 {
			return nil, whyAlias, fmt.Sprintf("%v; also mounted at %v", err, facts.Mounts.Aliases), nil
		}
		return nil, whyListing, err.Error(), nil
	}
	if problem := environmentProblem(base, facts); problem != "" {
		return nil, problem, fmt.Sprintf("root file system %q, tar %q (was %q), clock %d (was %d), also mounted at %v",
			facts.Mounts.RootFS, facts.TarVersion, base.TarVersion, facts.Clock, base.Clock.Distro, facts.Mounts.Aliases), nil
	}

	began := time.Now()
	id := newBackupID(req.Dir)
	x := newExplainer(list, baseDirs, facts.Mounts.Points, base.Since, clock.Distro)
	for _, ino := range otherDirs {
		x.baseInos[ino] = true
	}
	for _, point := range base.Mounts {
		x.baseMounts[point] = true
	}
	req.stage(stageReading)
	res, err := runTarPass(tarPass{Distro: req.Distro.Name, Dir: req.Dir, ID: id, Suffix: diffSuffix,
		Vars:      req.scriptVars(scriptVar{"WSLBAK_MODE", "diff"}, scriptVar{"WSLBAK_SINCE", strconv.FormatInt(base.Since, 10)}),
		Scheduled: req.Scheduled, OnProgress: req.OnProgress, OnEntry: x.sawDiffEntry})
	if err != nil {
		return nil, "", "", err
	}
	give := func(why, detail string) (*manifest, string, string, error) {
		res.discard()
		return nil, why, detail, nil
	}
	if res.ScanErr != nil {
		return give(whyStructure, "the tar stream could not be read back: "+res.ScanErr.Error())
	}
	if !res.Index.EtcIsDir {
		return give(whyStructure, "/etc is not among the directories")
	}
	if dropped := res.rows("dropped"); !sameStrings(dropped, base.Dropped) {
		return give(whySettings, fmt.Sprintf("tar options left out: %v, for the full backup: %v", dropped, base.Dropped))
	}
	// 清單之後才被刪掉或改名、tar 走到時已經不見的名稱：它們不必有來源。
	for _, quoted := range res.Vanished {
		list.noteGone(quoted)
	}
	if err := scanIndex(base.indexPath(), func(e indexEntry) bool {
		x.sawBaseEntry(e)
		return true
	}); err != nil {
		return give(whyBaseDamaged, "the file index of the full backup: "+err.Error())
	}
	drop, why, detail := x.finish(base.Index.Entries)
	if why != "" {
		return give(why, detail)
	}

	m = &manifest{
		Schema: manifestSchema, Kind: kindDiff, ID: id, Distro: req.Distro.Name, DistroID: req.Distro.GUID,
		Created: began.UTC(), Tool: version, WSL: req.WSL, Archive: id + diffSuffix,
		DefaultUID: req.Distro.DefaultUID, Flags: req.Distro.Flags, Excludes: req.Excludes, dir: req.Dir,
		Since: base.Since, Clock: &clock, TarVersion: facts.TarVersion,
		Base: base.ID, BaseSHA256: base.SHA256, BaseEntries: base.Index.Entries,
		Drop: packBits(drop), DropCount: x.dropped, base: base,
	}
	m.fill(res, began)
	if err := res.keep(m.archivePath()); err != nil {
		return nil, "", "", &backupError{failWrite, err.Error()}
	}
	if err := m.save(); err != nil {
		os.Remove(m.archivePath())
		os.Remove(m.indexPath())
		return nil, "", "", &backupError{failWrite, err.Error()}
	}
	logf("differential %s on %s written: tar %d bytes, archive %d bytes, %d entries; %d of %d entries of the full backup dropped; %d warnings, %.0fs",
		m.ID, base.ID, res.Index.Size, m.Size, res.Index.Entries, m.DropCount, m.BaseEntries, m.WarningCount, m.Seconds)
	return m, "", "", nil
}
