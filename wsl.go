package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

//go:embed backup.sh
var backupScript string

//go:embed check.sh
var checkScript string

//go:embed probe.sh
var probeScript string

// protoPrefix 是腳本寫到 stderr 的診斷行開頭；沒有這個前綴的行是工具自己的訊息。
const protoPrefix = "@wslbak\t"

// createNoWindow 讓子行程不要另開主控台視窗（從 WSL 經管線啟動時我們自己沒有主控台）。
const createNoWindow = 0x08000000

type distroInfo struct {
	Name    string
	Running bool
	Version int
}

func systemRoot() string {
	if r := os.Getenv("SystemRoot"); r != "" {
		return r
	}
	return `C:\Windows`
}

func system32(name string) string {
	return filepath.Join(systemRoot(), "System32", name)
}

// decodeWSLText 解碼 wsl.exe 自己印出的文字：預設是 UTF-16LE，設了 WSL_UTF8=1 才是 UTF-8。
func decodeWSLText(b []byte) string {
	if bytes.IndexByte(b, 0) < 0 {
		return strings.TrimPrefix(string(b), "\ufeff")
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return strings.TrimPrefix(string(utf16.Decode(u)), "\ufeff")
}

// parseDistroList 解析 `wsl.exe -l -v`。標題列可能被在地化，所以只認「最後一欄是數字」的列。
func parseDistroList(text string) []distroInfo {
	var list []distroInfo
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "*"))
		if len(f) < 3 {
			continue
		}
		ver, err := strconv.Atoi(f[len(f)-1])
		if err != nil {
			continue
		}
		list = append(list, distroInfo{
			Name:    strings.Join(f[:len(f)-2], " "),
			Running: f[len(f)-2] == "Running",
			Version: ver,
		})
	}
	return list
}

// runSystem 執行 System32 底下的工具，不開視窗，工作目錄固定在 Windows 目錄
// （從 WSL 啟動時目前目錄可能是 \\wsl.localhost\… 的 UNC 路徑）。
func runSystem(ctx context.Context, stdin string, exe string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = systemRoot()
	cmd.Env = append(os.Environ(), "WSL_UTF8=1")
	cmd.Stdin = strings.NewReader(stdin)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return out.Bytes(), err
}

// listDistros 回傳所有 distro；沒有安裝 WSL 時回傳 nil, nil。
func listDistros() ([]distroInfo, error) {
	wsl := system32("wsl.exe")
	if _, err := os.Stat(wsl); err != nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := runSystem(ctx, "", wsl, "-l", "-v")
	list := parseDistroList(decodeWSLText(out))
	if err != nil && len(list) == 0 {
		// 沒有任何 distro 時 wsl.exe 會以非零結束碼印出說明，這不算錯誤。
		return nil, nil
	}
	return list, nil
}

// lfOnly 去掉 CR：腳本若帶 CRLF，sh 會把 \r 當成指令的一部分。
func lfOnly(s string) string {
	return strings.ReplaceAll(s, "\r", "")
}

// runInDistro 以 root 在 distro 裡執行腳本並等它結束，回傳 stdout 與 stderr 合在一起的輸出。
func runInDistro(distro, script string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// -e 會把後面的參數原樣交給程式；腳本走 stdin，避開 wsl.exe 的引號處理。
	return runSystem(ctx, lfOnly(script), system32("wsl.exe"), "-d", distro, "-u", "root", "-e", "sh", "-s")
}

// shQuote 把字串寫成 sh 的單引號字面值。
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type scriptVar struct{ Name, Value string }

// withVars 在腳本前面加上變數賦值。
// 使用者可以決定內容的字串（排除樣式、路徑）只走這條路，絕不放進 wsl.exe 的命令列：
// wsl.exe 對引號的處理沒有文件可循，而單引號字面值在 sh 裡沒有任何例外。
func withVars(script string, vars ...scriptVar) string {
	var b strings.Builder
	for _, v := range vars {
		// sh 的字串裡放不了 NUL。
		b.WriteString(v.Name + "=" + shQuote(strings.ReplaceAll(v.Value, "\x00", "")) + "\n")
	}
	b.WriteString(script)
	return b.String()
}

// maxStderrLine 是 stderr 單行保留的長度上限；tar 的訊息帶著檔名，可以非常長。
const maxStderrLine = 4096

// readLine 讀一行並去掉結尾的換行，超過 limit 的部分丟掉。
func readLine(r *bufio.Reader, limit int) (string, error) {
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if room := limit - len(line); room > 0 {
			line = append(line, chunk[:min(len(chunk), room)]...)
		}
		if err != nil || !isPrefix {
			return strings.TrimRight(string(line), "\r"), err
		}
	}
}

// distroStream 是 distro 裡執行中的腳本。
type distroStream struct {
	// Stdout 是腳本寫出的原始資料，前面墊了一層大緩衝：管線一次只給大約 4 KB。
	Stdout *bufio.Reader

	cmd        *exec.Cmd
	stderrDone chan struct{}
}

// startInDistro 以 root 在 distro 裡執行腳本（腳本走 stdin）。stdout 留給呼叫端當資料讀，
// stderr 逐行交給 onStderr。和 runSystem 不同，兩者分開而且不整包緩衝。
func startInDistro(ctx context.Context, distro, script string, onStderr func(line string)) (*distroStream, error) {
	// -e 會把後面的參數原樣交給程式；腳本走 stdin，避開 wsl.exe 的引號處理。
	cmd := exec.CommandContext(ctx, system32("wsl.exe"), "-d", distro, "-u", "root", "-e", "sh", "-s")
	cmd.Dir = systemRoot()
	cmd.Env = append(os.Environ(), "WSL_UTF8=1")
	cmd.Stdin = strings.NewReader(lfOnly(script))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &distroStream{
		Stdout:     bufio.NewReaderSize(stdout, 1<<20),
		cmd:        cmd,
		stderrDone: make(chan struct{}),
	}
	go func() {
		defer close(s.stderrDone)
		r := bufio.NewReaderSize(stderr, 64<<10)
		for {
			line, err := readLine(r, maxStderrLine)
			if line != "" {
				onStderr(line)
			}
			if err != nil {
				// 不管是什麼錯誤都把剩下的讀完：stderr 沒人讀，腳本會卡在寫入。
				io.Copy(io.Discard, r)
				return
			}
		}
	}()
	return s, nil
}

// Wait 等腳本結束並回傳結束碼。呼叫前要先把 Stdout 讀完，或取消 context。
func (s *distroStream) Wait() (int, error) {
	<-s.stderrDone
	err := s.cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// tarMagicOffset 是 tar 標頭裡 "ustar" 字樣的位置。
const tarMagicOffset = 257

// looksLikeTar 回報資料的開頭是不是 tar 標頭。
// wsl.exe 自己的錯誤（例如 distro 不存在）會以 UTF-16 寫到 stdout，不能把它當成封存存下來。
func looksLikeTar(head []byte) bool {
	return len(head) >= tarMagicOffset+5 && string(head[tarMagicOffset:tarMagicOffset+5]) == "ustar"
}

// initJob 把自己放進一個「關閉時全部終止」的 Job Object：不管我們怎麼結束（包含被強制終止），
// 啟動的 wsl.exe 都會跟著結束，distro 裡的 tar 也就不會留下來繼續跑。
func initJob() {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		debugf("CreateJobObject: %v", err)
		return
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		debugf("SetInformationJobObject: %v", err)
		return
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		debugf("AssignProcessToJobObject: %v", err)
	}
	// 故意不關閉 job 的 handle：它要活到行程結束，由系統在那時關閉。
}

// importDistro 把 tar 匯入成新的 WSL2 distro。source 是封存檔的路徑；
// 給 "-" 的話資料從 stdin 讀。name、dir、source 會出現在 wsl.exe 的命令列上，
// 呼叫端要先確認 name 只含 WSL 允許的字元。
func importDistro(ctx context.Context, name, dir, source string, stdin io.Reader) error {
	cmd := exec.CommandContext(ctx, system32("wsl.exe"), "--import", name, dir, source, "--version", "2")
	cmd.Dir = systemRoot()
	cmd.Env = append(os.Environ(), "WSL_UTF8=1")
	cmd.Stdin = stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return &wslError{Op: "--import", Err: err, Output: strings.TrimSpace(decodeWSLText(out.Bytes()))}
	}
	logf("wsl --import %s: %s", name, strings.TrimSpace(decodeWSLText(out.Bytes())))
	return nil
}

func importFromReader(ctx context.Context, name, dir string, r io.Reader) error {
	return importDistro(ctx, name, dir, "-", r)
}

// wslError 是 wsl.exe 執行失敗：帶著它自己印出來的說明（已解碼，語言跟著 Windows）。
type wslError struct {
	Op     string
	Err    error
	Output string
}

func (e *wslError) Error() string {
	if e.Output == "" {
		return "wsl " + e.Op + ": " + e.Err.Error()
	}
	return "wsl " + e.Op + ": " + e.Output
}

func (e *wslError) Unwrap() error { return e.Err }

// parseProto 從腳本的輸出裡挑出以 protoPrefix 開頭的行，各自依 tab 切開。
// 其他的行（例如 wsl.exe 自己的警告）一律略過。
func parseProto(text string) [][]string {
	var rows [][]string
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), protoPrefix); ok {
			rows = append(rows, strings.Split(rest, "\t"))
		}
	}
	return rows
}

// protoValue 回傳第一個以 key 開頭的行後面的欄位；沒有這一行時 ok 是 false。
func protoValue(rows [][]string, key string) (fields []string, ok bool) {
	for _, r := range rows {
		if r[0] == key {
			return r[1:], true
		}
	}
	return nil, false
}

var wslVersionRe = regexp.MustCompile(`\d+\.\d+\.\d+(\.\d+)?`)

// parseWSLVersion 從 `wsl.exe --version` 的輸出取出 WSL 的版本。
// 每一行的標籤會被翻譯，但第一行一定是 WSL 自己的版本，所以只取第一行的第一個版本號。
func parseWSLVersion(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			return wslVersionRe.FindString(line)
		}
	}
	return ""
}

// wslVersion 回傳安裝的 WSL 版本。Windows 內建的舊版 WSL 不認得 --version，這時回傳空字串。
func wslVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := runSystem(ctx, "", system32("wsl.exe"), "--version")
	if err != nil {
		return ""
	}
	return parseWSLVersion(decodeWSLText(out))
}

type probeInfo struct {
	TarKind    string // gnu、other、missing
	TarVersion string
	UsedBytes  int64
	OS         string
	HasSHA256  bool
}

func parseProbe(text string) (probeInfo, error) {
	rows := parseProto(text)
	var p probeInfo
	if _, ok := protoValue(rows, "done"); !ok {
		return p, errors.New(firstLine(text))
	}
	if f, ok := protoValue(rows, "tar"); ok && len(f) > 0 {
		p.TarKind = f[0]
		if len(f) > 1 {
			p.TarVersion = f[1]
		}
	}
	if f, ok := protoValue(rows, "used-kb"); ok && len(f) > 0 {
		kb, _ := strconv.ParseInt(f[0], 10, 64)
		p.UsedBytes = kb * 1024
	}
	if f, ok := protoValue(rows, "os"); ok && len(f) > 0 {
		p.OS = f[0]
	}
	if f, ok := protoValue(rows, "sha256sum"); ok && len(f) > 0 {
		p.HasSHA256 = f[0] == "yes"
	}
	return p, nil
}

// firstLine 回傳第一行非空白的文字，拿來當錯誤訊息。
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return "no output"
}

// probeDistro 在 distro 裡跑探測腳本。distro 沒在執行的話會被啟動。
func probeDistro(name string) (probeInfo, error) {
	out, err := runInDistro(name, probeScript, 60*time.Second)
	info, perr := parseProbe(decodeWSLText(out))
	if perr != nil {
		if err != nil {
			return info, fmt.Errorf("%w: %v", err, perr)
		}
		return info, perr
	}
	return info, nil
}
