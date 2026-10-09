package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unsafe"

	"github.com/mattn/go-runewidth"
	"golang.org/x/sys/windows"
)

// 這個檔案負責排版與輸出；所有給使用者看的文字都在 i18n.go 的 catalog 裡。

var (
	useColor bool
	stdin    = bufio.NewReader(os.Stdin)
	// 提示印到 stderr，讓 stdout 可以乾淨地接到管線；測試時會換掉。
	promptOut io.Writer = os.Stderr
	// 東亞寬度只算全形字，「→」這類寬度不明確的字元一律當半形。
	widths = func() *runewidth.Condition {
		c := runewidth.NewCondition()
		c.EastAsianWidth = false
		return c
	}()
)

// initConsole 只有在 stdout 是真正的主控台時才輸出顏色。
func initConsole() {
	h := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil || os.Getenv("NO_COLOR") != "" {
		return
	}
	if windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil {
		useColor = true
	}
}

// plain 把來自 distro 的文字（檔名、連結目標、tar 的訊息、發行版名稱）裡的控制字元換成看得見的寫法。
// 主控台會解讀跳脫序列，而 distro 裡任何使用者都能建立名稱帶有這些字元的檔案。
func plain(s string) string {
	if !strings.ContainsFunc(s, unprintable) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case !unprintable(r):
			b.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}

// unprintable：控制字元（定位字元除外），以及會改變文字顯示方向的字元。
func unprintable(r rune) bool {
	switch {
	case r == '\t':
		return false
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// processMemoryCounters 對應 Windows 的 PROCESS_MEMORY_COUNTERS。
type processMemoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

var procGetProcessMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

// debugPeakMemory 在 --debug 時印出這次執行最多用了多少記憶體，回報問題時有用。
func debugPeakMemory() {
	if !debugEnabled {
		return
	}
	mem := processMemoryCounters{}
	mem.cb = uint32(unsafe.Sizeof(mem))
	if ok, _, _ := procGetProcessMemoryInfo.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&mem)), uintptr(mem.cb)); ok != 0 {
		debugf("peak memory: %s", humanBytes(int64(mem.peakWorkingSetSize)))
	}
}

// debugEnabled 由 --debug 開啟；debugf 把診斷訊息印到 stderr。
// 診斷訊息是給回報問題用的，固定用英文。
var debugEnabled bool

func debugf(format string, args ...any) {
	if debugEnabled {
		fmt.Fprintln(os.Stderr, dim("[debug] "+fmt.Sprintf(format, args...)))
	}
}

func paint(code, s string) string {
	if !useColor {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func bold(s string) string   { return paint("1", s) }
func dim(s string) string    { return paint("2", s) }
func yellow(s string) string { return paint("33", s) }
func red(s string) string    { return paint("31", s) }
func green(s string) string  { return paint("32", s) }

// ask 把提示印到 stderr 並讀一行；讀到 EOF（沒有可互動的輸入）時回傳 false。
func ask(prompt string) (string, bool) {
	fmt.Fprint(promptOut, prompt)
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(promptOut)
		return "", false
	}
	return strings.TrimSpace(line), true
}

// isYes 不分語言：英文介面輸入「是」、中文介面輸入 y 都算同意。
func isYes(s string) bool {
	s = strings.ToLower(s)
	for _, w := range yesWords {
		if s == w {
			return true
		}
	}
	return false
}

func pad(s string, w int) string { return widths.FillRight(s, w) }

// labelWidth 是一組欄位名稱裡最寬的那個再加兩格，用來把後面的值對齊。
func labelWidth(labels []string) int {
	w := 0
	for _, l := range labels {
		w = max(w, widths.StringWidth(l))
	}
	return w + 2
}

// printTable 印出對齊的表格；第一列是標題。最後一欄不補空白。
func printTable(rows [][]string) {
	if len(rows) == 0 {
		return
	}
	cols := make([]int, len(rows[0]))
	for _, row := range rows {
		for c, cell := range row {
			cols[c] = max(cols[c], widths.StringWidth(cell))
		}
	}
	for r, row := range rows {
		var b strings.Builder
		for c, cell := range row {
			if c == len(row)-1 {
				b.WriteString(cell)
			} else {
				b.WriteString(pad(cell, cols[c]+2))
			}
		}
		line := "  " + b.String()
		if r == 0 {
			line = dim(line)
		}
		fmt.Println(line)
	}
}

// humanBytes 用和檔案總管相同的算法（1 GB = 1024 MB）顯示大小。
func humanBytes(n int64) string {
	const unit = 1024
	switch {
	case n >= unit*unit*unit:
		return fmt.Sprintf("%.1f GB", float64(n)/(unit*unit*unit))
	case n >= unit*unit:
		return fmt.Sprintf("%.0f MB", float64(n)/(unit*unit))
	case n >= unit:
		return fmt.Sprintf("%.0f KB", float64(n)/unit)
	}
	return fmt.Sprintf("%d B", n)
}

func humanDuration(d time.Duration) string {
	seconds := int(d.Round(time.Second).Seconds())
	switch {
	case seconds >= 3600:
		return fmt.Sprintf(T.DurationHours, seconds/3600, seconds%3600/60)
	case seconds >= 60:
		return fmt.Sprintf(T.DurationMinutes, seconds/60, seconds%60)
	}
	return fmt.Sprintf(T.DurationSeconds, seconds)
}

// humanSince 把時間換成「多久以前」。
func humanSince(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return T.JustNow
	case d < time.Hour:
		return fmt.Sprintf(T.MinutesAgo, int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf(T.HoursAgo, int(d.Hours()))
	}
	return fmt.Sprintf(T.DaysAgo, int(d.Hours()/24))
}

// progressLine 在同一行上更新進度。只有輸出到真正的主控台時才顯示；
// 接到檔案、管線或由排程啟動時什麼都不印。
type progressLine struct {
	label string
	shown bool
}

func (p *progressLine) update(n int64) {
	if !useColor {
		return
	}
	fmt.Printf("\r  %s %s ", p.label, humanBytes(n))
	p.shown = true
}

func (p *progressLine) clear() {
	if p.shown {
		fmt.Print("\r\x1b[2K")
		p.shown = false
	}
}
