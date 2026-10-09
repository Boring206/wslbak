//go:build ignore

// 端對端測試用的小工具：在一個真正的（隱藏的）主控台視窗裡執行程式，可以模擬鍵盤輸入，
// 結束後把螢幕緩衝區的內容讀回來。管線測不到的事只有這樣才看得到：
// 文字在指定的字碼頁下是否正確、表格照主控台自己的字寬是否對齊、提示能不能從鍵盤回答。
//
//	e2e-console.exe -out result.txt [-cp 950] [-type "y\r"] [-after "[y/N]"] [-timeout 60] -- program args…
//	e2e-console.exe -suspend <process id> [-seconds 30]
//
// 第二種用法把一個行程凍結一段時間再放開，模擬電腦在備份途中睡著又醒來：
// 對那個行程而言，時間跳了一段，而它什麼都沒做。
//
// 結果檔的格式：第一行 exit=<結束碼>（逾時是 exit=timeout），接著一行 [text] 與螢幕上每一列的文字，
// 再來一行 [cells] 與每一列的佔格圖，每格一個字元：空白、W（全形字佔的格）、x（其他）。
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/mattn/go-runewidth"
	"golang.org/x/sys/windows"
)

var (
	kernel32                       = windows.NewLazySystemDLL("kernel32.dll")
	procSetConsoleOutputCP         = kernel32.NewProc("SetConsoleOutputCP")
	procSetConsoleCP               = kernel32.NewProc("SetConsoleCP")
	procSetConsoleScreenBufferSize = kernel32.NewProc("SetConsoleScreenBufferSize")
	procReadConsoleOutputW         = kernel32.NewProc("ReadConsoleOutputW")
	procWriteConsoleInputW         = kernel32.NewProc("WriteConsoleInputW")
)

const (
	createNewConsole = 0x00000010
	leadingByte      = 0x0100
	trailingByte     = 0x0200
	keyEvent         = 0x0001
	vkReturn         = 0x0D
)

type charInfo struct {
	Char uint16
	Attr uint16
}

type smallRect struct{ Left, Top, Right, Bottom int16 }

type inputRecord struct {
	EventType uint16
	_         uint16
	KeyDown   int32
	Repeat    uint16
	VK        uint16
	Scan      uint16
	Char      uint16
	Control   uint32
}

func coord(x, y int) uintptr { return uintptr(uint32(uint16(x)) | uint32(uint16(y))<<16) }

func main() {
	out := flag.String("out", "", "file that receives the result")
	cp := flag.Uint("cp", 0, "console code page (0: leave as it is)")
	typed := flag.String("type", "", `keys to type; \r is Enter`)
	after := flag.String("after", "", "type once this text is on the screen")
	timeout := flag.Int("timeout", 60, "seconds to wait for the program")
	inside := flag.Bool("inside", false, "internal: already running in the hidden console")
	suspend := flag.Uint("suspend", 0, "freeze the process with this id instead of running a program")
	seconds := flag.Int("seconds", 30, "how long to freeze it")
	flag.Parse()
	if *suspend != 0 {
		if err := freeze(uint32(*suspend), time.Duration(*seconds)*time.Second); err != nil {
			fail(err)
		}
		return
	}
	if *out == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: e2e-console -out file [-cp N] [-type keys] [-after text] -- program args…")
		os.Exit(2)
	}
	if !*inside {
		// 第一段：把自己重新啟動在一個新的、隱藏的主控台裡。
		self, err := os.Executable()
		if err != nil {
			fail(err)
		}
		args := append([]string{"-inside"}, os.Args[1:]...)
		cmd := exec.Command(self, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewConsole, HideWindow: true}
		if err := cmd.Run(); err != nil {
			fail(err)
		}
		return
	}
	if err := run(*out, uint32(*cp), unescape(*typed), *after, time.Duration(*timeout)*time.Second, flag.Args()); err != nil {
		os.WriteFile(*out, []byte("exit=error\n"+err.Error()+"\n"), 0o644)
		os.Exit(1)
	}
}

// freeze 把一個行程的所有執行緒暫停一段時間，然後放開。
func freeze(pid uint32, d time.Duration) error {
	const processSuspendResume = 0x0800
	h, err := windows.OpenProcess(processSuspendResume, false, pid)
	if err != nil {
		return fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(h)
	ntdll := windows.NewLazySystemDLL("ntdll.dll")
	if status, _, _ := ntdll.NewProc("NtSuspendProcess").Call(uintptr(h)); status != 0 {
		return fmt.Errorf("NtSuspendProcess: status %#x", status)
	}
	time.Sleep(d)
	if status, _, _ := ntdll.NewProc("NtResumeProcess").Call(uintptr(h)); status != 0 {
		return fmt.Errorf("NtResumeProcess: status %#x", status)
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "e2e-console:", err)
	os.Exit(1)
}

func unescape(s string) string {
	return strings.NewReplacer(`\r`, "\r", `\n`, "\r").Replace(s)
}

func run(out string, cp uint32, typed, after string, timeout time.Duration, args []string) error {
	conout, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	conin, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	h := windows.Handle(conout.Fd())
	if cp != 0 {
		procSetConsoleOutputCP.Call(uintptr(cp))
		procSetConsoleCP.Call(uintptr(cp))
	}
	// 緩衝區放大，長的輸出才不會捲出去；失敗也無妨，只是能讀回的列數變少。
	procSetConsoleScreenBufferSize.Call(uintptr(h), coord(160, 800))

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = conin, conout, conout
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline := time.Now().Add(timeout)

	exit := ""
	if typed != "" {
		for after != "" && time.Now().Before(deadline) {
			rows, _ := readScreen(h)
			if strings.Contains(strings.Join(rows, "\n"), after) {
				break
			}
			select {
			case err := <-done:
				done <- err
				after = ""
			case <-time.After(100 * time.Millisecond):
			}
		}
		if err := typeKeys(windows.Handle(conin.Fd()), typed); err != nil {
			return err
		}
	}
	select {
	case err := <-done:
		exit = "0"
		if ee, ok := err.(*exec.ExitError); ok {
			exit = fmt.Sprint(ee.ExitCode())
		} else if err != nil {
			return err
		}
	case <-time.After(time.Until(deadline)):
		cmd.Process.Kill()
		<-done
		exit = "timeout"
	}

	rows, cells := readScreen(h)
	var b strings.Builder
	fmt.Fprintf(&b, "exit=%s\n[text]\n%s\n[cells]\n%s\n", exit, strings.Join(rows, "\n"), strings.Join(cells, "\n"))
	return os.WriteFile(out, []byte(b.String()), 0o644)
}

// readScreen 傳回游標所在列以上每一列的文字與佔格圖。
func readScreen(h windows.Handle) (rows, cells []string) {
	var info windows.ConsoleScreenBufferInfo
	if windows.GetConsoleScreenBufferInfo(h, &info) != nil {
		return nil, nil
	}
	width := int(info.Size.X)
	buf := make([]charInfo, width)
	for y := 0; y <= int(info.CursorPosition.Y); y++ {
		region := smallRect{0, int16(y), int16(width - 1), int16(y)}
		ok, _, _ := procReadConsoleOutputW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])),
			coord(width, 1), coord(0, 0), uintptr(unsafe.Pointer(&region)))
		if ok == 0 {
			break
		}
		var text, cell strings.Builder
		for x := 0; x < width; x++ {
			c := buf[x]
			r := rune(c.Char)
			switch {
			case c.Attr&trailingByte != 0:
				cell.WriteByte('W')
			case c.Attr&leadingByte != 0:
				text.WriteRune(r)
				cell.WriteByte('W')
			case runewidth.RuneWidth(r) == 2 && x+1 < width && buf[x+1].Char == c.Char:
				// 有些主控台不標示全形字的前後半：全形字固定佔兩格，下一格是同一個字。
				text.WriteRune(r)
				cell.WriteString("WW")
				x++
			default:
				text.WriteRune(r)
				if r == ' ' || r == 0 {
					cell.WriteByte(' ')
				} else {
					cell.WriteByte('x')
				}
			}
		}
		rows = append(rows, strings.TrimRight(strings.ReplaceAll(text.String(), "\x00", " "), " "))
		cells = append(cells, strings.TrimRight(cell.String(), " "))
	}
	return rows, cells
}

// typeKeys 把文字當成鍵盤輸入塞進主控台的輸入緩衝區，每個字元一次按下與一次放開。
func typeKeys(h windows.Handle, text string) error {
	var records []inputRecord
	for _, u := range utf16Of(text) {
		vk := uint16(0)
		if u == '\r' {
			vk = vkReturn
		}
		records = append(records,
			inputRecord{EventType: keyEvent, KeyDown: 1, Repeat: 1, VK: vk, Char: u},
			inputRecord{EventType: keyEvent, KeyDown: 0, Repeat: 1, VK: vk, Char: u})
	}
	if len(records) == 0 {
		return nil
	}
	var written uint32
	ok, _, err := procWriteConsoleInputW.Call(uintptr(h), uintptr(unsafe.Pointer(&records[0])),
		uintptr(len(records)), uintptr(unsafe.Pointer(&written)))
	if ok == 0 {
		return fmt.Errorf("WriteConsoleInput: %w", err)
	}
	return nil
}

func utf16Of(s string) []uint16 {
	u, _ := windows.UTF16FromString(s)
	return u[:len(u)-1]
}
