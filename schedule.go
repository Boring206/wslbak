package main

import (
	"context"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// 排程用 Windows 的工作排程器：每個 Windows 使用者一個工作，以使用者自己的登入身分執行，
// 不需要系統管理員權限，也不儲存密碼。工作的定義用 XML 交給 schtasks.exe，
// 因為「錯過時間後盡快補跑」與電池相關的設定沒有對應的命令列參數。

// taskPrefix 加上使用者的 SID 就是工作名稱：工作名稱是整台電腦共用的，不同使用者不能撞名。
const taskPrefix = "wslbak-"

// scheduledArgs 是排程執行時的參數，固定不變：會變的東西（distro、目的地）都在設定檔裡，
// 不經過工作排程器的命令列。
const scheduledArgs = "run --scheduled"

// taskArguments 回傳排程工作的參數。測試用的沙箱要多帶 --home，
// 排程啟動的程式才會讀沙箱裡的設定，而不是使用者真正的那一份。
func taskArguments() string {
	if homeOverride != "" {
		return "--home " + syscall.EscapeArg(homeOverride) + " " + scheduledArgs
	}
	return scheduledArgs
}

type taskSpec struct {
	SID       string
	Command   string // 要執行的程式，完整路徑
	Arguments string
	At        string // 每天的 HH:MM
	// Now 是註冊工作的時間，用來算出第一次執行是哪一天。
	Now         time.Time
	LogonDelay  bool // 加上「登入後過一段時間」的觸發
	Description string
}

func currentSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

func taskName(sid string) string {
	if homeOverride != "" {
		// 測試用的沙箱有自己的工作，不會蓋掉真正的那一個。
		return taskPrefix + "sandbox-" + sid
	}
	return taskPrefix + sid
}

func xmlText(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// firstStart 回傳工作第一次該執行的時間：now 之後第一個 at（HH:MM，當地時間）。
//
// 開始時間不能設在過去。工作設了「錯過排定的時間就盡快補跑」，如果開始時間早於現在，
// 工作排程器會認為今天那一次已經錯過，在註冊後一兩分鐘自己把它跑起來；
// 使用者才剛回答「現在先不要備份」，備份卻自己開始了。
func firstStart(at string, now time.Time) time.Time {
	hour, minute := 3, 0
	if clock, ok := normalizeClock(at); ok {
		fmt.Sscanf(clock, "%d:%d", &hour, &minute)
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !start.After(now) {
		start = start.AddDate(0, 0, 1)
	}
	return start
}

// taskXML 產生工作的定義。
func taskXML(spec taskSpec) string {
	logon := ""
	if spec.LogonDelay {
		// 電腦在排定的時間是關著或睡著的，登入後補跑一次；是否真的需要備份由程式自己判斷。
		logon = `
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + xmlText(spec.SID) + `</UserId>
      <Delay>PT10M</Delay>
    </LogonTrigger>`
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>` + xmlText(spec.Description) + `</Description>
  </RegistrationInfo>
  <Triggers>
    <CalendarTrigger>
      <StartBoundary>` + firstStart(spec.At, spec.Now).Format("2006-01-02T15:04:05") + `</StartBoundary>
      <Enabled>true</Enabled>
      <ScheduleByDay>
        <DaysInterval>1</DaysInterval>
      </ScheduleByDay>
    </CalendarTrigger>` + logon + `
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + xmlText(spec.SID) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT12H</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlText(spec.Command) + `</Command>
      <Arguments>` + xmlText(spec.Arguments) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

// utf16File 把文字編成帶 BOM 的 UTF-16LE：schtasks 讀 XML 檔時只有這種編碼不會把非英文字元弄壞。
func utf16File(text string) []byte {
	units := utf16.Encode([]rune(text))
	out := make([]byte, 2, 2+2*len(units))
	out[0], out[1] = 0xFF, 0xFE
	for _, u := range units {
		out = binary.LittleEndian.AppendUint16(out, u)
	}
	return out
}

func schtasks(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := runSystem(ctx, "", system32("schtasks.exe"), args...)
	// schtasks 的輸出用的是主控台字碼頁，內容只拿來附在錯誤訊息裡，不做判斷。
	return strings.TrimSpace(string(out)), err
}

// createTask 建立或取代排程工作。
func createTask(spec taskSpec) error {
	state, err := stateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(state, 0o755); err != nil {
		return err
	}
	file := filepath.Join(state, "task.xml")
	if err := os.WriteFile(file, utf16File(taskXML(spec)), 0o600); err != nil {
		return err
	}
	defer os.Remove(file)
	if out, err := schtasks("/Create", "/TN", taskName(spec.SID), "/XML", file, "/F"); err != nil {
		return fmt.Errorf("schtasks /Create: %w: %s", err, out)
	}
	return nil
}

// taskExists 回報工作在不在。只看結束碼，不解析輸出的文字。
func taskExists(sid string) bool {
	_, err := schtasks("/Query", "/TN", taskName(sid))
	return err == nil
}

func deleteTask(sid string) error {
	if !taskExists(sid) {
		return nil
	}
	if out, err := schtasks("/Delete", "/TN", taskName(sid), "/F"); err != nil {
		return fmt.Errorf("schtasks /Delete: %w: %s", err, out)
	}
	return nil
}
