package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

func utf16le(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

const distroListText = "  NAME                   STATE           VERSION\r\n" +
	"* Ubuntu-26.04           Running         2\r\n" +
	"  docker-desktop         Stopped         2\r\n" +
	"  legacy                 Stopped         1\r\n"

func TestParseDistroList(t *testing.T) {
	want := []distroInfo{{"Ubuntu-26.04", true, 2}, {"docker-desktop", false, 2}, {"legacy", false, 1}}
	// wsl.exe 預設輸出 UTF-16LE，設了 WSL_UTF8=1 才是 UTF-8；兩種都要解得出來。
	for label, raw := range map[string][]byte{"utf-8": []byte(distroListText), "utf-16le": utf16le(distroListText)} {
		if got := parseDistroList(decodeWSLText(raw)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %+v", label, got)
		}
	}
	// 標題列被翻譯也不影響：只認最後一欄是數字的列。
	localized := "  名稱                   狀態            版本\n* Ubuntu-26.04           Running         2\n"
	if got := parseDistroList(localized); len(got) != 1 || got[0].Name != "Ubuntu-26.04" {
		t.Errorf("localized header: got %+v", got)
	}
}

func TestParseWSLVersion(t *testing.T) {
	cases := map[string]string{
		"WSL version: 2.7.13.0\r\nKernel version: 6.18.33.2-2\r\n": "2.7.13.0",
		"WSL 版本: 2.7.13.0\n核心版本: 6.18.33.2-2\n":                    "2.7.13.0",
		"WSL-Version: 2.4.4.0\nKernelversion: 5.15.167.4-1\n":      "2.4.4.0",
		"\n\nWSL のバージョン: 2.3.26\n":                                 "2.3.26",
		// 內建的舊版 WSL 不認得 --version，印出的是說明文字。
		"Invalid command line option: --version\n": "",
		"": "",
	}
	for in, want := range cases {
		if got := parseWSLVersion(in); got != want {
			t.Errorf("parseWSLVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEligible(t *testing.T) {
	ok := regDistro{Name: "Ubuntu-26.04", Version: 2, State: lxssInstalled}
	cases := []struct {
		d    regDistro
		want bool
	}{
		{ok, true},
		{regDistro{Name: "wslbak-e2e-0123abcd", Version: 2, State: lxssInstalled}, true},
		{regDistro{Name: "legacy", Version: 1, State: lxssInstalled}, false},
		{regDistro{Name: "Ubuntu", Version: 2, State: 3}, false},
		{regDistro{Name: "docker-desktop", Version: 2, State: lxssInstalled}, false},
		{regDistro{Name: "docker-desktop-data", Version: 2, State: lxssInstalled}, false},
		{regDistro{Name: "Rancher-Desktop", Version: 2, State: lxssInstalled}, false},
		{regDistro{Name: "podman-machine-default", Version: 2, State: lxssInstalled}, false},
		{regDistro{Name: "wslbak-verify-20261008T130403Z-23668d5a8d5ab13e", Version: 2, State: lxssInstalled}, false},
		{regDistro{Name: "my distro", Version: 2, State: lxssInstalled}, false},
		{regDistro{Name: "x;rm", Version: 2, State: lxssInstalled}, false},
	}
	for _, c := range cases {
		reason, got := eligible(c.d)
		if got != c.want || (got == (reason != "")) {
			t.Errorf("eligible(%q) = %q, %v; want %v", c.d.Name, reason, got, c.want)
		}
	}
}

func TestShellQuoting(t *testing.T) {
	cases := map[string]string{
		"":              `''`,
		"plain":         `'plain'`,
		"two words":     `'two words'`,
		"it's":          `'it'\''s'`,
		"$(reboot) `x`": "'$(reboot) `x`'",
		"a\nb":          "'a\nb'",
	}
	for in, want := range cases {
		if got := shQuote(in); got != want {
			t.Errorf("shQuote(%q) = %s, want %s", in, got, want)
		}
	}
	got := withVars("main\n", scriptVar{"A", "x'y"}, scriptVar{"B", "has\x00nul"})
	if want := "A='x'\\''y'\nB='hasnul'\nmain\n"; got != want {
		t.Errorf("withVars = %q, want %q", got, want)
	}
}

// 內嵌的腳本要整個包在 main 裡，最後一行才呼叫：腳本是從 stdin 讀的，
// 中間的指令如果去讀 stdin，會把腳本的後半段吃掉。也不能帶 CR。
func TestScriptsShape(t *testing.T) {
	for name, script := range map[string]string{"backup.sh": backupScript, "check.sh": checkScript, "probe.sh": probeScript, "caches.sh": cachesScript, "restorefiles.sh": restoreFilesScript} {
		// 內嵌的腳本是在使用者的 distro 裡以 root 執行的，不可以有遞迴刪除。
		if regexp.MustCompile(`\brm\s+-[a-zA-Z]*[rR]`).MatchString(script) {
			t.Errorf("%s contains a recursive rm", name)
		}
		// 腳本只能用 shell 內建的功能與 coreutils：精簡的 distro 沒有 awk（Fedora 的 WSL 映像就沒有），
		// sed 與 grep 也不保證有。註解不算。
		code := regexp.MustCompile(`(?m)^\s*#.*$`).ReplaceAllString(script, "")
		if m := regexp.MustCompile(`\b(awk|gawk|sed|grep|perl|python3?)\b`).FindString(code); m != "" {
			t.Errorf("%s uses %s, which a minimal distro may not have", name, m)
		}
		if strings.Contains(script, "\r") {
			t.Errorf("%s contains a carriage return", name)
		}
		if !strings.HasSuffix(strings.TrimSpace(script), `main "$@" </dev/null`) {
			t.Errorf(`%s must end with: main "$@" </dev/null`, name)
		}
		if !strings.HasPrefix(script, "#!/bin/sh\n") {
			t.Errorf("%s must start with #!/bin/sh", name)
		}
	}
	if lfOnly("a\r\nb\r\n") != "a\nb\n" {
		t.Error("lfOnly should strip carriage returns")
	}
}

func TestParseProbe(t *testing.T) {
	text := "wsl: a localized warning that must be ignored\n" +
		"@wslbak\ttar\tgnu\ttar (GNU tar) 1.35\n@wslbak\tused-kb\t1024\n@wslbak\tos\tDebian GNU/Linux 13 (trixie)\n@wslbak\tsha256sum\tyes\n@wslbak\tdone\n"
	got, err := parseProbe(text)
	want := probeInfo{TarKind: "gnu", TarVersion: "tar (GNU tar) 1.35", UsedBytes: 1 << 20, OS: "Debian GNU/Linux 13 (trixie)", HasSHA256: true}
	if err != nil || got != want {
		t.Errorf("parseProbe = %+v, %v", got, err)
	}
	if got, _ := parseProbe("@wslbak\ttar\tother\ttar (busybox) 1.36.1\n@wslbak\tdone\n"); got.TarKind != "other" {
		t.Errorf("busybox tar: %+v", got)
	}
	// 腳本沒有跑完：把 wsl.exe 印出來的第一行當成錯誤訊息。
	if _, err := parseProbe("\nThe distro could not be started.\nmore\n"); err == nil || err.Error() != "The distro could not be started." {
		t.Errorf("unfinished probe: err = %v", err)
	}
}

func TestClassifyTarStderr(t *testing.T) {
	lines := []string{
		"tar: ./home/user: file changed as we read it",
		"tar: ./home/user/.cache/x: File removed before we read it",
		"tar: ./var/lib/app/tmp123: Cannot stat: No such file or directory",
		"tar: ./home/user/db.sqlite-wal: File shrank by 4096 bytes; padding with zeros",
		"tar: ./home/user/mnt: Cannot stat: Permission denied",
		"tar: Exiting with failure status due to previous errors",
	}
	warnings, fatal := classifyTarStderr(lines)
	if len(warnings) != 5 || len(fatal) != 0 {
		t.Errorf("benign lines: %d warnings, fatal %q", len(warnings), fatal)
	}
	_, fatal = classifyTarStderr([]string{
		"tar: ./home/user/data.bin: Read error at byte 0, while reading 10240 bytes: Input/output error",
		"tar: ./etc/shadow: Cannot open: Permission denied",
		"tar: unrecognized option '--acls'",
		"something that is not from tar at all",
	})
	if len(fatal) != 4 {
		t.Errorf("real errors: fatal = %q", fatal)
	}

	benign, bad := []string{"tar: ./x: file changed as we read it"}, []string{"tar: ./x: Read error"}
	cases := []struct {
		code            int
		warnings, fatal []string
		want            bool
	}{
		{0, nil, nil, true},
		{1, benign, nil, true},
		{1, nil, bad, false},
		{0, nil, bad, false},
		// 結束碼 2 要有一行已知無害的訊息解釋它才放行。
		{2, benign, nil, true},
		{2, nil, nil, false},
		{2, benign, bad, false},
		{3, nil, nil, false},
		{127, nil, nil, false},
		{-1, nil, nil, false},
	}
	for _, c := range cases {
		if got := tarSucceeded(c.code, c.warnings, c.fatal); got != c.want {
			t.Errorf("tarSucceeded(%d, %d warnings, %d fatal) = %v, want %v", c.code, len(c.warnings), len(c.fatal), got, c.want)
		}
	}
}

func TestParseAndJudgeCheck(t *testing.T) {
	sum := sha256.Sum256([]byte(inertWSLConf))
	good := "@wslbak\tpid1\tinit(wslbak-ver\n@wslbak\tsystemd\tno\n@wslbak\twindows-drives\t0\n@wslbak\tinterop\tno\n" +
		"@wslbak\twslconf\t" + hex.EncodeToString(sum[:]) + "\n@wslbak\tuser\tok\tuser\t/home/user\n@wslbak\tsamples\t3\t0\n@wslbak\tdone\n"
	if reason, detail := judgeCheck(parseCheck(good), 3, 0); reason != "" {
		t.Errorf("a clean report was rejected: %s (%s)", reason, detail)
	}
	swap := func(old, new string) checkReport { return parseCheck(strings.Replace(good, old, new, 1)) }
	cases := []struct {
		label  string
		report checkReport
		want   string
	}{
		{"the script did not finish", swap("@wslbak\tdone\n", ""), reasonCheck},
		{"systemd is PID 1", swap("pid1\tinit(wslbak-ver", "pid1\tsystemd"), reasonNotInert},
		{"systemd runtime directory exists", swap("systemd\tno", "systemd\tyes"), reasonNotInert},
		{"a Windows drive is mounted", swap("windows-drives\t0", "windows-drives\t2"), reasonNotInert},
		{"interop is on", swap("interop\tno", "interop\tyes"), reasonNotInert},
		{"wsl.conf is not ours", swap(hex.EncodeToString(sum[:]), strings.Repeat("0", 64)), reasonNotInert},
		{"no report about wsl.conf", swap("@wslbak\twslconf\t"+hex.EncodeToString(sum[:])+"\n", ""), reasonNotInert},
		{"the default user is gone", swap("user\tok\tuser\t/home/user", "user\tmissing"), reasonUser},
		{"the home directory is gone", swap("user\tok", "user\tno-home"), reasonUser},
		{"a file differs", swap("samples\t3\t0", "samples\t2\t1"), reasonSamples},
		{"fewer files were checked than recorded", swap("samples\t3\t0", "samples\t2\t0"), reasonSamples},
		{"empty output", parseCheck(""), reasonCheck},
	}
	for _, c := range cases {
		if got, _ := judgeCheck(c.report, 3, 0); got != c.want {
			t.Errorf("%s: reason = %q, want %q", c.label, got, c.want)
		}
	}
	for _, reason := range []string{reasonNoIndex, reasonEtc, reasonNoSpace} {
		if !skippedReason(reason) {
			t.Errorf("%s should count as skipped", reason)
		}
	}
	for _, reason := range []string{reasonChanged, reasonImport, reasonNotInert, reasonSamples, reasonCheck} {
		if skippedReason(reason) {
			t.Errorf("%s should count as a failure, not as skipped", reason)
		}
	}
}

func backup(id string, verified bool) *manifest {
	m := &manifest{ID: id}
	if verified {
		m.Verify = &verifyResult{OK: true}
	}
	return m
}

func ids(list []*manifest) string {
	var out []string
	for _, m := range list {
		out = append(out, m.ID)
	}
	return strings.Join(out, " ")
}

func TestPlanRetention(t *testing.T) {
	// 編號越大越新。v = 通過試還原，u = 沒通過。
	v, u := true, false
	last := func(n int) retention { return retention{Last: n} }
	cases := []struct {
		label   string
		backups []*manifest
		pol     retention
		verify  string
		want    string
	}{
		{"nothing to do", []*manifest{backup("1", v), backup("2", v)}, last(3), verifyRestore, ""},
		{"oldest verified go first", []*manifest{backup("1", v), backup("2", v), backup("3", v), backup("4", v)}, last(2), verifyRestore, "2 1"},
		// 連續失敗不會把好的備份擠掉：失敗的自己另外算，最多留兩份。
		{"failures do not push out good backups", []*manifest{backup("1", v), backup("2", u), backup("3", u), backup("4", u)}, last(1), verifyRestore, "2"},
		{"the newest verified one survives any number of failures", []*manifest{backup("1", v), backup("2", u), backup("3", u), backup("4", u), backup("5", u)}, last(1), verifyRestore, "3 2"},
		{"only unverified backups", []*manifest{backup("1", u), backup("2", u), backup("3", u)}, last(5), verifyRestore, "1"},
		{"keep below one is treated as one", []*manifest{backup("1", v), backup("2", v)}, last(0), verifyRestore, "1"},
		// 關掉試還原時不分通過與否，留最新的幾份。
		{"verification turned off", []*manifest{backup("1", u), backup("2", v), backup("3", u), backup("4", u)}, last(2), verifyNone, "2 1"},
		{"unsorted input", []*manifest{backup("3", v), backup("1", v), backup("2", v)}, last(1), verifyRestore, "2 1"},
		{"empty", nil, last(3), verifyRestore, ""},
	}
	for _, c := range cases {
		if got := ids(planRetention(c.backups, c.pol, c.verify)); got != c.want {
			t.Errorf("%s: would delete %q, want %q", c.label, got, c.want)
		}
	}
}

// 每天 03:00（UTC）一份，從 from 往回 days 天。
func daily(from time.Time, days int) []*manifest {
	var list []*manifest
	for i := 0; i < days; i++ {
		list = append(list, backup(from.AddDate(0, 0, -i).Format(idLayout), true))
	}
	return list
}

func TestPlanRetentionWeeklyMonthly(t *testing.T) {
	// 2026-10-09 是星期五。
	today := time.Date(2026, 10, 9, 3, 0, 0, 0, time.UTC)
	kept := func(all, doomed []*manifest) []string {
		gone := map[string]bool{}
		for _, m := range doomed {
			gone[m.ID] = true
		}
		var out []string
		for _, m := range all {
			if !gone[m.ID] {
				out = append(out, m.ID[:8])
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(out)))
		return out
	}
	all := daily(today, 100)

	// 最新 3 份，加上最近 4 個有備份的週各一份（每週最新的那一份）。
	// 這一週最新的就是今天，已經在「最新 3 份」裡；往前三週各取星期日那一份。
	got := kept(all, planRetention(all, retention{Last: 3, Weekly: 4}, verifyRestore))
	want := []string{"20261009", "20261008", "20261007", "20261004", "20260927", "20260920"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("weekly: kept %v, want %v", got, want)
	}

	// 每月：最近 3 個月各留最新的一份。
	got = kept(all, planRetention(all, retention{Last: 1, Monthly: 3}, verifyRestore))
	want = []string{"20261009", "20260930", "20260831"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("monthly: kept %v, want %v", got, want)
	}

	// 三種規則一起用：同一份可以同時滿足好幾條，不會重複計算。
	got = kept(all, planRetention(all, retention{Last: 2, Weekly: 2, Monthly: 2}, verifyRestore))
	want = []string{"20261009", "20261008", "20261004", "20260930"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("combined: kept %v, want %v", got, want)
	}

	// 沒有備份的週不佔名額：電腦關了三週之後，每週的名額仍然用在有備份的週上。
	gap := append(daily(today, 2), daily(today.AddDate(0, 0, -30), 20)...)
	got = kept(gap, planRetention(gap, retention{Last: 1, Weekly: 3}, verifyRestore))
	want = []string{"20261009", "20260909", "20260906"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("gap: kept %v, want %v", got, want)
	}

	// 沒通過試還原的不能拿來當每週或每月的那一份。
	mixed := daily(today, 20)
	for _, m := range mixed {
		if m.ID[:8] == "20261004" {
			m.Verify = nil
		}
	}
	got = kept(mixed, planRetention(mixed, retention{Last: 1, Weekly: 2}, verifyRestore))
	want = []string{"20261009", "20261004", "20261003"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unverified weekly candidate: kept %v, want %v", got, want)
	}

	// 不管規則怎麼設，最新一份好的永遠留著。
	for _, pol := range []retention{{}, {Last: 1}, {Weekly: 5}, {Monthly: 5}} {
		doomed := planRetention(all, pol, verifyRestore)
		for _, m := range doomed {
			if m.ID == all[0].ID {
				t.Errorf("%+v would delete the newest verified backup", pol)
			}
		}
	}
}

func TestNewerManifests(t *testing.T) {
	dir := t.TempDir()
	if newerManifests(dir) {
		t.Error("an empty folder has no newer manifests")
	}
	os.WriteFile(filepath.Join(dir, "20261001T030000Z.json"), []byte(`{"schema": 1}`), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.json"), []byte(`{"schema": 99}`), 0o644)
	if newerManifests(dir) {
		t.Error("a file that is not named like a backup must be ignored")
	}
	os.WriteFile(filepath.Join(dir, "20261002T030000Z.json"), []byte(`{"schema": 2, "kind": "diff"}`), 0o644)
	if !newerManifests(dir) {
		t.Error("a manifest with a newer schema should be noticed")
	}
}

func TestRestoreName(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		original string
		existing []string
		want     string
	}{
		{"Ubuntu", nil, "Ubuntu"},
		{"Ubuntu", []string{"Debian"}, "Ubuntu"},
		{"Ubuntu", []string{"Ubuntu"}, "Ubuntu-restored-20261008"},
		// distro 名稱不分大小寫。
		{"Ubuntu", []string{"ubuntu"}, "Ubuntu-restored-20261008"},
		{"Ubuntu", []string{"Ubuntu", "Ubuntu-restored-20261008"}, "Ubuntu-restored-20261008-2"},
		{"Ubuntu", []string{"Ubuntu", "Ubuntu-restored-20261008", "ubuntu-RESTORED-20261008-2"}, "Ubuntu-restored-20261008-3"},
	}
	for _, c := range cases {
		got := restoreName(c.original, c.existing, now)
		if got != c.want {
			t.Errorf("restoreName(%q, %q) = %q, want %q", c.original, c.existing, got, c.want)
		}
		if !distroNameRe.MatchString(got) {
			t.Errorf("restoreName(%q) = %q is not a valid distro name", c.original, got)
		}
	}
	long := strings.Repeat("a", 64)
	if got := restoreName(long, []string{long}, now); !distroNameRe.MatchString(got) {
		t.Errorf("a long name produced %q (%d characters), which WSL would reject", got, len(got))
	}
}

func TestTaskXML(t *testing.T) {
	spec := taskSpec{
		SID:         "S-1-5-21-1-2-3-1001",
		Command:     `C:\Users\王 小明 & <Co>\AppData\Local\Programs\wslbak\wslbakw.exe`,
		Arguments:   scheduledArgs,
		At:          "03:05",
		Now:         time.Date(2026, 10, 9, 16, 0, 0, 0, time.UTC),
		LogonDelay:  true,
		Description: `每天備份 "WSL" <distro> & more`,
	}
	text := taskXML(spec)
	var doc struct {
		Triggers struct {
			Calendar struct {
				StartBoundary string
			} `xml:"CalendarTrigger"`
			Logon *struct {
				UserId string
				Delay  string
			} `xml:"LogonTrigger"`
		}
		Principal struct {
			UserId    string
			LogonType string
			RunLevel  string
		} `xml:"Principals>Principal"`
		Settings struct {
			MultipleInstancesPolicy    string
			DisallowStartIfOnBatteries string
			StopIfGoingOnBatteries     string
			StartWhenAvailable         string
		}
		Exec struct {
			Command   string
			Arguments string
		} `xml:"Actions>Exec"`
		Description string `xml:"RegistrationInfo>Description"`
	}
	// Go 的解析器不認 UTF-16 的宣告；內容本身是一般的字串，把宣告拿掉再解析。
	if err := xml.Unmarshal([]byte(strings.Replace(text, ` encoding="UTF-16"`, "", 1)), &doc); err != nil {
		t.Fatalf("the task definition is not well-formed XML: %v\n%s", err, text)
	}
	// 路徑與說明裡的 & < > " 與中文、空白都要原樣回得來。
	if doc.Exec.Command != spec.Command || doc.Exec.Arguments != "run --scheduled" || doc.Description != spec.Description {
		t.Errorf("round trip changed the text: %+v / %q", doc.Exec, doc.Description)
	}
	if doc.Principal.UserId != spec.SID || doc.Principal.LogonType != "InteractiveToken" || doc.Principal.RunLevel != "LeastPrivilege" {
		t.Errorf("principal = %+v", doc.Principal)
	}
	// 註冊的時候今天的 03:05 已經過了，所以第一次是明天；開始時間絕不在過去。
	if doc.Triggers.Calendar.StartBoundary != "2026-10-10T03:05:00" {
		t.Errorf("start boundary = %q", doc.Triggers.Calendar.StartBoundary)
	}
	if doc.Triggers.Logon == nil || doc.Triggers.Logon.UserId != spec.SID || doc.Triggers.Logon.Delay == "" {
		t.Errorf("logon trigger = %+v", doc.Triggers.Logon)
	}
	s := doc.Settings
	if s.MultipleInstancesPolicy != "IgnoreNew" || s.DisallowStartIfOnBatteries != "false" || s.StopIfGoingOnBatteries != "false" || s.StartWhenAvailable != "true" {
		t.Errorf("settings = %+v", s)
	}

	spec.LogonDelay = false
	if strings.Contains(taskXML(spec), "LogonTrigger") {
		t.Error("the logon trigger should be left out when it is not requested")
	}

	// 檔案要是帶 BOM 的 UTF-16LE。
	raw := utf16File("A王")
	if want := []byte{0xFF, 0xFE, 'A', 0, 0x8B, 0x73}; !reflect.DeepEqual(raw, want) {
		t.Errorf("utf16File = % x, want % x", raw, want)
	}
}

func TestTaskName(t *testing.T) {
	if got := taskName("S-1-5-21-1"); got != "wslbak-S-1-5-21-1" {
		t.Errorf("taskName = %q", got)
	}
	homeOverride = `C:\sandbox`
	defer func() { homeOverride = "" }()
	// 測試用的沙箱不能和真正的排程工作同名。
	if got := taskName("S-1-5-21-1"); got != "wslbak-sandbox-S-1-5-21-1" {
		t.Errorf("sandbox taskName = %q", got)
	}
	if got := taskArguments(); got != `--home C:\sandbox run --scheduled` {
		t.Errorf("sandbox taskArguments = %q", got)
	}
	homeOverride = `C:\Users\Some One\sand box`
	if got := taskArguments(); got != `--home "C:\Users\Some One\sand box" run --scheduled` {
		t.Errorf("sandbox taskArguments with spaces = %q", got)
	}
	homeOverride = ""
	if got := taskArguments(); got != "run --scheduled" {
		t.Errorf("taskArguments = %q", got)
	}
}

func TestWebhookRequest(t *testing.T) {
	read := func(raw string) (contentType, title, body string) {
		t.Helper()
		req, err := webhookRequest(raw, "wslbak：備份失敗", "磁碟滿了")
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(req.Body)
		if req.Method != "POST" || req.URL.String() != raw {
			t.Errorf("%s: %s %s", raw, req.Method, req.URL)
		}
		return req.Header.Get("Content-Type"), req.Header.Get("Title"), string(data)
	}
	if ct, _, body := read("https://discord.com/api/webhooks/1/secret"); ct != "application/json" || body != `{"content":"wslbak：備份失敗\n磁碟滿了"}` {
		t.Errorf("discord: %s %s", ct, body)
	}
	if ct, _, body := read("https://hooks.slack.com/services/T/B/secret"); ct != "application/json" || body != `{"text":"wslbak：備份失敗\n磁碟滿了"}` {
		t.Errorf("slack: %s %s", ct, body)
	}
	ct, title, body := read("https://ntfy.sh/my-topic")
	if ct != "text/plain; charset=utf-8" || body != "磁碟滿了" {
		t.Errorf("ntfy: %s %q", ct, body)
	}
	// 標頭裡不能有非 ASCII 的位元組。
	for _, r := range title {
		if r > 0x7e {
			t.Errorf("ntfy title header is not ASCII: %q", title)
			break
		}
	}
	if !strings.HasPrefix(title, "=?utf-8?") {
		t.Errorf("ntfy title should be RFC 2047 encoded, got %q", title)
	}
	if webhookHost("https://ntfy.sh/secret-topic?auth=token") != "ntfy.sh" {
		t.Error("webhookHost should return only the host")
	}
	for raw, want := range map[string]bool{"https://ntfy.sh/x": true, "http://nas.local:8080/hook": true, "ntfy.sh/x": false, "file:///c:/x": false, "": false} {
		if validWebhook(raw) != want {
			t.Errorf("validWebhook(%q) should be %v", raw, want)
		}
	}
}

func TestSmallHelpers(t *testing.T) {
	for in, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1 KB", 5 << 20: "5 MB", 7687933328: "7.2 GB"} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[time.Duration]string{3 * time.Second: "3 秒", 108 * time.Second: "1 分 48 秒", 3725 * time.Second: "1 小時 2 分"} {
		if got := humanDuration(in); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", in, got, want)
		}
	}
	versions := []struct {
		a, b string
		want int
	}{{"0.1.0", "0.1.0", 0}, {"0.1.0", "0.2.0", -1}, {"1.0.0", "0.9.9", 1}, {"0.10.0", "0.9.0", 1}, {"dev", "0.1.0", -1}, {"1.0", "1.0.0", 0}}
	for _, c := range versions {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	paths := map[string]string{
		`C:\Users\Me\AppData\`:      `c:\users\me\appdata`,
		`\\?\C:\Users\Me`:           `c:\users\me`,
		`C:/Users/Me/x/../y`:        `c:\users\me\y`,
		`\\wsl.localhost\Ubuntu\x\`: `\\wsl.localhost\ubuntu\x`,
	}
	for in, want := range paths {
		if got := normPath(in); got != want {
			t.Errorf("normPath(%q) = %q, want %q", in, got, want)
		}
	}
	for path, want := range map[string]bool{`\\wsl.localhost\Ubuntu\home\me`: true, `\\wsl$\Ubuntu\x`: true, `D:\WSLBackup`: false, `\\nas\share\wsl`: false} {
		if isWSLPath(path) != want {
			t.Errorf("isWSLPath(%q) should be %v", path, want)
		}
	}
	if !isFAT("FAT32") || !isFAT("fat") || isFAT("NTFS") || isFAT("exFAT") {
		t.Error("isFAT: only FAT and FAT32 have the 4 GB file limit")
	}
}

func TestConfigAndManifestOnDisk(t *testing.T) {
	homeOverride = t.TempDir()
	defer func() { homeOverride = "" }()

	if cfg, err := loadConfig(); cfg != nil || err != nil {
		t.Fatalf("no configuration yet: got %+v, %v", cfg, err)
	}
	cfg := newConfig()
	cfg.At = "04:30"
	cfg.Distros["Ubuntu"] = &distroConfig{ID: "{guid}", Dest: `D:\WSLBackup`, Keep: 3, DefaultExcludes: true, Exclude: []string{"./home/*/Downloads/*"}, Enabled: true}
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfig()
	if err != nil || !reflect.DeepEqual(got, cfg) {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
	if ex := got.Distros["Ubuntu"].excludes(); ex[0] != "./init" || ex[len(ex)-1] != "./home/*/Downloads/*" {
		t.Errorf("excludes = %q", ex)
	}
	// 關掉預設排除，/init 還是要排除：它不是 distro 的檔案。
	got.Distros["Ubuntu"].DefaultExcludes = false
	if ex := got.Distros["Ubuntu"].excludes(); !reflect.DeepEqual(ex, []string{"./init", "./home/*/Downloads/*"}) {
		t.Errorf("excludes without defaults = %q", ex)
	}

	// 更新版本寫的設定檔：舊版不能讀，也不能覆寫。
	path, _ := configPath()
	os.WriteFile(path, []byte(`{"schema": 99, "future": true}`), 0o644)
	if _, err := loadConfig(); err != errNewerConfig {
		t.Errorf("newer schema: err = %v", err)
	}
	if err := saveConfig(cfg); err != errNewerConfig {
		t.Errorf("saving over a newer schema: err = %v", err)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "future") {
		t.Error("the newer configuration file was overwritten")
	}

	// manifest：要和旁邊的封存對得上才算數。
	dir := filepath.Join(homeOverride, "backups")
	os.MkdirAll(dir, 0o755)
	write := func(id string, m manifest, withArchive bool) {
		m.dir = dir
		if err := writeJSON(filepath.Join(dir, id+".json"), &m); err != nil {
			t.Fatal(err)
		}
		if withArchive {
			os.WriteFile(filepath.Join(dir, id+archiveSuffix), []byte("x"), 0o644)
		}
	}
	good := func(id string) manifest {
		return manifest{Schema: manifestSchema, ID: id, Archive: id + archiveSuffix, Distro: "Ubuntu"}
	}
	write("20261001T030000Z", good("20261001T030000Z"), true)
	write("20261002T030000Z", good("20261002T030000Z"), true)
	write("20261003T030000Z", good("20261003T030000Z"), false)                                                              // 封存不見了
	write("20261004T030000Z", manifest{Schema: manifestSchema, ID: "20261004T030000Z", Archive: `..\..\evil.tar.gz`}, true) // 封存檔名被改過
	write("20261005T030000Z", manifest{Schema: 99, ID: "20261005T030000Z", Archive: "20261005T030000Z" + archiveSuffix}, true)
	write("20261006T030000Z", good("20261001T030000Z"), true) // 內容和檔名不一致
	write("notes", good("notes"), true)                       // 不是我們的編號格式
	os.WriteFile(filepath.Join(dir, "20261007T030000Z.json"), []byte("{broken"), 0o644)
	os.WriteFile(filepath.Join(dir, "20261007T030000Z"+archiveSuffix), []byte("x"), 0o644)

	if got := ids(listBackups(dir)); got != "20261001T030000Z 20261002T030000Z" {
		t.Errorf("listBackups = %q", got)
	}
	if m := newest(listBackups(dir), false); m == nil || m.ID != "20261002T030000Z" {
		t.Errorf("newest = %+v", m)
	}
	if m := newest(listBackups(dir), true); m != nil {
		t.Errorf("no backup is verified, but newest(verified) = %+v", m)
	}

	// 刪除只動 manifest 與它的封存，旁邊的東西都留著。
	if removed := applyPrune(listBackups(dir)[:1]); removed != 1 {
		t.Errorf("applyPrune removed %d", removed)
	}
	left, _ := os.ReadDir(dir)
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	for _, gone := range []string{"20261001T030000Z.json", "20261001T030000Z.tar.gz"} {
		if strings.Contains(strings.Join(names, " "), gone) {
			t.Errorf("%s should have been deleted", gone)
		}
	}
	if len(names) != 13 {
		t.Errorf("exactly two files should be gone; left: %q", names)
	}
}

func TestCleanPartials(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"20261001T030000Z.tar.gz.partial", // 我們的：刪
		"20261001T030000Z.tar.gz",         // 完成的備份：留
		"important.tar.gz.partial",        // 別人的：留
		"20261001T030000Z.tar.gz.partial.bak",
	} {
		os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644)
	}
	cleanPartials(dir)
	left, _ := os.ReadDir(dir)
	if len(left) != 3 {
		t.Errorf("only the leftover partial should be removed; left %d entries", len(left))
	}
	if _, err := os.Stat(filepath.Join(dir, "20261001T030000Z.tar.gz.partial")); err == nil {
		t.Error("the leftover partial is still there")
	}
}

func TestSelfExesAt(t *testing.T) {
	same := func(p string) (string, error) { return p, nil }
	cases := []struct {
		label    string
		self     string
		resolve  func(string) (string, error)
		cli, gui string
	}{
		{"npm package, console", `C:\npm\wslbak\bin\wslbak-x64.exe`, same, `C:\npm\wslbak\bin\wslbak-x64.exe`, `C:\npm\wslbak\bin\wslbakw-x64.exe`},
		{"npm package, windowless", `C:\npm\wslbak\bin\wslbakw-arm64.exe`, same, `C:\npm\wslbak\bin\wslbak-arm64.exe`, `C:\npm\wslbak\bin\wslbakw-arm64.exe`},
		{"release zip or installed copy", `C:\Tools\wslbak\wslbak.exe`, same, `C:\Tools\wslbak\wslbak.exe`, `C:\Tools\wslbak\wslbakw.exe`},
		{"started by the scheduler", `C:\Tools\wslbak\WSLBAKW.EXE`, same, `C:\Tools\wslbak\wslbak.exe`, `C:\Tools\wslbak\wslbakw.exe`},
		// winget 在 Links 資料夾放的是連結；另一個執行檔在連結指向的地方。
		{"through a symlink", `C:\Users\me\AppData\Local\Microsoft\WinGet\Links\wslbak.exe`,
			func(string) (string, error) { return `C:\Pkgs\Boring206.wslbak\wslbak.exe`, nil },
			`C:\Pkgs\Boring206.wslbak\wslbak.exe`, `C:\Pkgs\Boring206.wslbak\wslbakw.exe`},
		{"a link that cannot be resolved is used as it is", `C:\Links\wslbak.exe`,
			func(string) (string, error) { return "", os.ErrNotExist }, `C:\Links\wslbak.exe`, `C:\Links\wslbakw.exe`},
	}
	for _, c := range cases {
		cli, gui, err := selfExesAt(c.self, c.resolve)
		if err != nil || cli != c.cli || gui != c.gui {
			t.Errorf("%s: got %q, %q, %v", c.label, cli, gui, err)
		}
	}
	if _, _, err := selfExesAt(`C:\Tools\renamed.exe`, same); err == nil {
		t.Error("a program with an unexpected name should be an error")
	}
}

func given(o options, flags ...string) options {
	o.given = map[string]bool{}
	for _, f := range flags {
		o.given[f] = true
	}
	return o
}

func TestApplySettings(t *testing.T) {
	fresh := func() *config {
		cfg := newConfig()
		cfg.Notify.Webhook = "https://ntfy.sh/secret-topic"
		cfg.Distros["Ubuntu"] = &distroConfig{Dest: `D:\WSLBackup`, Keep: 7, DefaultExcludes: true, Exclude: []string{"./opt/big/*"}, Enabled: true}
		cfg.Distros["Debian"] = &distroConfig{Dest: `D:\WSLBackup`, Keep: 3, DefaultExcludes: true, Enabled: true}
		return cfg
	}
	flags := func(changes []settingChange) string {
		var out []string
		for _, c := range changes {
			out = append(out, c.Flag)
		}
		return strings.Join(out, " ")
	}

	// 全域設定不需要指定 distro。
	cfg := fresh()
	changes, err := applySettings(cfg, "", given(options{at: "02:30", notify: notifyAlways, verify: verifyNone, webhook: "off"},
		"--at", "--notify", "--verify", "--webhook"))
	if err != nil || flags(changes) != "--at --webhook --notify --verify" {
		t.Fatalf("global settings: %v, %v", changes, err)
	}
	if cfg.At != "02:30" || cfg.Notify.On != notifyAlways || cfg.Verify != verifyNone || cfg.Notify.Webhook != "" {
		t.Errorf("global settings were not applied: %+v", cfg)
	}
	// webhook 的網址是機密：變更紀錄裡只有主機名稱。
	for _, c := range changes {
		if strings.Contains(c.Old+c.New, "secret-topic") {
			t.Errorf("the webhook URL leaked into the change list: %+v", c)
		}
	}

	// 和現在一樣的值不算變更。
	cfg = fresh()
	changes, err = applySettings(cfg, "Ubuntu", given(options{at: defaultAt, keep: 7, exclude: []string{"./opt/big/*", "./tmp/*"}, enable: true},
		"--at", "--keep", "--exclude", "--enable"))
	if err != nil || len(changes) != 0 {
		t.Errorf("unchanged values were reported as changes: %v, %v", changes, err)
	}

	// 單一 distro 的設定，包含 0 這種有意義的零值。
	cfg = fresh()
	cfg.Distros["Ubuntu"].KeepWeekly = 4
	changes, err = applySettings(cfg, "Ubuntu", given(options{keep: 3, keepWeekly: 0, keepMonthly: 6, exclude: []string{"./home/*/Downloads/*"}, disable: true},
		"--keep", "--keep-weekly", "--keep-monthly", "--exclude", "--disable"))
	u := cfg.Distros["Ubuntu"]
	if err != nil || flags(changes) != "--keep --keep-weekly --keep-monthly --exclude --disable" {
		t.Fatalf("per-distro settings: %v, %v", changes, err)
	}
	if u.Keep != 3 || u.KeepWeekly != 0 || u.KeepMonthly != 6 || u.Enabled || !slices.Contains(u.Exclude, "./home/*/Downloads/*") {
		t.Errorf("per-distro settings were not applied: %+v", u)
	}
	if cfg.Distros["Debian"].Keep != 3 || !cfg.Distros["Debian"].Enabled {
		t.Errorf("another distro was changed: %+v", cfg.Distros["Debian"])
	}

	// 取消自己加的排除。
	cfg = fresh()
	if _, err = applySettings(cfg, "Ubuntu", given(options{unexclude: []string{"./opt/big/*"}}, "--unexclude")); err != nil || len(cfg.Distros["Ubuntu"].Exclude) != 0 {
		t.Errorf("removing an own exclude: %v, %+v", err, cfg.Distros["Ubuntu"])
	}
	// 取消預設排除裡的一項：其餘的預設項目要原樣留著。
	cfg = fresh()
	_, err = applySettings(cfg, "Ubuntu", given(options{unexclude: []string{"./tmp/*"}}, "--unexclude"))
	got := cfg.Distros["Ubuntu"].excludes()
	if err != nil || slices.Contains(got, "./tmp/*") {
		t.Errorf("./tmp/* is still excluded: %v, %q", err, got)
	}
	for _, keep := range []string{"./init", "./var/tmp/*", "./home/*/.cache/*", "./root/.cache/*", "./opt/big/*"} {
		if !slices.Contains(got, keep) {
			t.Errorf("%s should still be excluded, got %q", keep, got)
		}
	}
	// /init 不是 distro 的檔案，永遠排除。
	if _, err = applySettings(fresh(), "Ubuntu", given(options{unexclude: []string{"./init"}}, "--unexclude")); !errors.Is(err, errBuiltinExclude) {
		t.Errorf("removing ./init: err = %v", err)
	}
	// 不在清單裡的樣式：沒有變更，也不是錯誤。
	if changes, err = applySettings(fresh(), "Ubuntu", given(options{unexclude: []string{"./nope"}}, "--unexclude")); err != nil || len(changes) != 0 {
		t.Errorf("removing an unknown pattern: %v, %v", changes, err)
	}

	if _, err = applySettings(fresh(), "Fedora", given(options{keep: 2}, "--keep")); err == nil {
		t.Error("a distro that is not set up should be an error")
	}
	if _, err = applySettings(fresh(), "", given(options{webhook: "not a url"}, "--webhook")); err == nil {
		t.Error("an invalid webhook should be an error")
	}
}

func TestParsePick(t *testing.T) {
	cases := []struct {
		answer string
		count  int
		want   []int
	}{
		{"1", 3, []int{0}}, {" 3 ", 3, []int{2}}, {"a", 3, []int{0, 1, 2}}, {"ALL", 2, []int{0, 1}},
		{"0", 3, nil}, {"4", 3, nil}, {"", 3, nil}, {"two", 3, nil}, {"1,2", 3, nil}, {"a", 0, nil},
	}
	for _, c := range cases {
		got, ok := parsePick(c.answer, c.count)
		if !reflect.DeepEqual(got, c.want) || ok != (c.want != nil) {
			t.Errorf("parsePick(%q, %d) = %v, %v; want %v", c.answer, c.count, got, ok, c.want)
		}
	}
}

func TestDoctorJudges(t *testing.T) {
	if got := judgeSAC(sacEnforce, true); got.Level != levelFail || got.Fix == "" {
		t.Errorf("Smart App Control enforcing: %+v", got)
	}
	if got := judgeSAC(sacEvaluation, true); got.Level != levelWarn {
		t.Errorf("Smart App Control evaluating: %+v", got)
	}
	for _, c := range []struct {
		state uint64
		found bool
	}{{sacOff, true}, {0, false}, {sacEnforce, false}} {
		if got := judgeSAC(c.state, c.found); got.Level != levelOK {
			t.Errorf("judgeSAC(%d, %v) = %+v", c.state, c.found, got)
		}
	}
	if got := judgeCFA(1, true); got.Level != levelWarn || got.Fix == "" {
		t.Errorf("Controlled folder access on: %+v", got)
	}
	// 2 是只稽核、不擋。
	for _, state := range []uint64{0, 2} {
		if got := judgeCFA(state, true); got.Level != levelOK {
			t.Errorf("judgeCFA(%d) = %+v", state, got)
		}
	}
	if judgeCFA(1, false).Level != levelOK {
		t.Error("a missing value means the feature is off")
	}

	if judgeSpace(10, 20) != levelWarn || judgeSpace(20, 20) != levelOK || judgeSpace(0, 0) != levelOK {
		t.Error("judgeSpace: warn only when the need is known and exceeds what is free")
	}

	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	cases := []struct {
		label string
		st    distroState
		want  checkLevel
	}{
		{"fresh success", distroState{LastSuccess: ago(time.Hour), LastAttempt: ago(time.Hour), LastResult: resultOK}, levelOK},
		{"never ran", distroState{}, levelWarn},
		{"stale", distroState{LastSuccess: ago(72 * time.Hour), LastAttempt: ago(72 * time.Hour), LastResult: resultOK}, levelWarn},
		{"failed after the last success", distroState{LastSuccess: ago(30 * time.Hour), LastAttempt: ago(time.Hour), LastResult: resultFailed, LastMessage: "disk full"}, levelFail},
		{"failed, never succeeded", distroState{LastAttempt: ago(time.Hour), LastResult: resultFailed, LastMessage: "x"}, levelFail},
		{"written but not verified", distroState{LastSuccess: ago(20 * time.Hour), LastAttempt: ago(time.Hour), LastResult: resultUnverified, LastMessage: "no space"}, levelWarn},
	}
	for _, c := range cases {
		if got := judgeLastRun("Ubuntu", &c.st, now); got.Level != c.want {
			t.Errorf("%s: level %d, want %d (%s)", c.label, got.Level, c.want, got.Text)
		}
	}
}

func TestParseCaches(t *testing.T) {
	text := "wsl: a warning to ignore\n" +
		"@wslbak\tcache\t1048576\t./var/cache/apt/archives/*\t/var/cache/apt/archives\n" +
		"@wslbak\tcache\t5368709120\t./home/*/.npm/_cacache/*\t/home/me/.npm/_cacache\n" +
		"@wslbak\tcache\tnot-a-number\t./x/*\t/x\n" +
		"@wslbak\tdocker\t2147483648\n@wslbak\tdone\n"
	caches, docker, done := parseCaches(text)
	if !done || docker != 2<<30 || len(caches) != 2 {
		t.Fatalf("parseCaches: %+v, docker=%d, done=%v", caches, docker, done)
	}
	// 最大的排前面。
	if caches[0].Pattern != "./home/*/.npm/_cacache/*" || caches[0].Bytes != 5<<30 || caches[0].Path != "/home/me/.npm/_cacache" {
		t.Errorf("largest cache = %+v", caches[0])
	}
	if _, _, done := parseCaches("@wslbak\tcache\t1\t./a/*\t/a\n"); done {
		t.Error("a scan that did not reach the end must not count as done")
	}
}

func TestAntivirus(t *testing.T) {
	// 真實的輸出：Defender 沒有在執行（393472），Avast 是啟用的（266240）。
	text := "393472|Windows Defender\r\n266240|Avast Antivirus\r\n\r\nnot a line\r\n266240|Avast Antivirus\r\n"
	if got := parseAntivirus(text); !reflect.DeepEqual(got, []string{"Avast Antivirus"}) {
		t.Errorf("parseAntivirus = %q", got)
	}
	// 暫時關閉防護（270336）的也算：它過一陣子會自己恢復。
	if got := parseAntivirus("393472|Windows Defender\n270336|Avast Antivirus\n"); !reflect.DeepEqual(got, []string{"Avast Antivirus"}) {
		t.Errorf("snoozed antivirus: parseAntivirus = %q", got)
	}
	// 關閉的（262144）不算。
	if got := parseAntivirus("262144|Some AV\n"); len(got) != 0 {
		t.Errorf("disabled antivirus: parseAntivirus = %q", got)
	}
	// Defender 啟用時的狀態值是 397568。
	if got := parseAntivirus("397568|Windows Defender\n"); !reflect.DeepEqual(got, []string{"Windows Defender"}) {
		t.Errorf("parseAntivirus = %q", got)
	}
	if _, ok := judgeAntivirus([]string{"Windows Defender"}, `C:\x`); ok {
		t.Error("the built-in antivirus needs no note")
	}
	if _, ok := judgeAntivirus(nil, `C:\x`); ok {
		t.Error("no antivirus, no note")
	}
	c, ok := judgeAntivirus([]string{"Windows Defender", "Avast Antivirus"}, `C:\Programs\wslbak`)
	if !ok || c.Level != levelNote || !strings.Contains(c.Text, "Avast Antivirus") || !strings.Contains(c.Fix, `C:\Programs\wslbak`) {
		t.Errorf("third-party antivirus: %+v, %v", c, ok)
	}
}

func TestStallWatch(t *testing.T) {
	start := time.Date(2026, 10, 9, 3, 0, 0, 0, time.UTC)
	at := func(d time.Duration) time.Time { return start.Add(d) }

	// 資料一直在動：不算停住。
	w := newStallWatch(start)
	for i := 1; i <= 5; i++ {
		if moved, stuck := w.observe(at(time.Duration(i)*time.Second), int64(i)*100); !moved || stuck {
			t.Fatalf("tick %d: moved=%v stuck=%v", i, moved, stuck)
		}
	}
	// 之後每秒檢查一次、位元組數不變：過了上限才算停住。
	stuckAt := time.Duration(0)
	for d := 6 * time.Second; d <= 5*time.Second+stallLimit+3*time.Second; d += time.Second {
		if moved, stuck := w.observe(at(d), 500); moved {
			t.Fatalf("no new data, but moved at %v", d)
		} else if stuck {
			stuckAt = d
			break
		}
	}
	if stuckAt <= 5*time.Second+stallLimit || stuckAt > 5*time.Second+stallLimit+2*time.Second {
		t.Errorf("declared stuck at %v, expected just after %v", stuckAt, 5*time.Second+stallLimit)
	}

	// 電腦睡了兩個小時再醒來：醒來的那一刻不能被當成停住，要重新開始計時。
	w = newStallWatch(start)
	w.observe(at(time.Second), 100)
	w.observe(at(2*time.Second), 100)
	wake := 2*time.Second + 2*time.Hour
	if _, stuck := w.observe(at(wake), 100); stuck {
		t.Error("declared stuck immediately after a two-hour suspension")
	}
	// 醒來之後每秒照常檢查，但真的一直沒有資料：要從醒來那一刻起再過完整的上限才判定停住。
	stuckAfter := time.Duration(0)
	for d := time.Second; d <= stallLimit+3*time.Second; d += time.Second {
		if _, stuck := w.observe(at(wake+d), 100); stuck {
			stuckAfter = d
			break
		}
	}
	if stuckAfter <= stallLimit || stuckAfter > stallLimit+2*time.Second {
		t.Errorf("after waking, declared stuck after %v; expected just after %v", stuckAfter, stallLimit)
	}
}

func TestBackupErrorBrief(t *testing.T) {
	secret := "/home/me/clients/acme-merger/plan.docx"
	for _, kind := range []backupFailure{failStart, failNotGNUTar, failNotTar, failWrite, failStalled, failTruncated, failTar} {
		err := &backupError{Kind: kind, Detail: "tar: ." + secret + ": Read error at byte 0"}
		brief := backupErrorBrief(err) + T.BriefSeeLog
		if brief == "" || strings.Contains(brief, "acme") || strings.Contains(brief, "Read error") {
			t.Errorf("kind %d: the notification text carries details: %q", kind, brief)
		}
	}
	// 顯示在本機的訊息仍然帶著細節。
	if text := backupErrorText(&backupError{Kind: failTar, Detail: "tar exit 2: tar: ." + secret + ": Read error"}); !strings.Contains(text, "acme") {
		t.Errorf("the local message lost its detail: %q", text)
	}
	if backupErrorBrief(errors.New("something else")) == "" {
		t.Error("an unknown error still needs a notification text")
	}
}

func TestValidInto(t *testing.T) {
	for _, p := range []string{"/home/me/restored", "/var/tmp/wslbak restore", "/root/回復"} {
		if !validInto(p) {
			t.Errorf("validInto(%q) should be accepted", p)
		}
	}
	for _, p := range []string{"", "/", "//", "home/me/x", "./x", `C:\Users\me`, "/home/../etc", "/a\nb", "/a\x00b"} {
		if validInto(p) {
			t.Errorf("validInto(%q) should be rejected", p)
		}
	}
}

func TestSelection(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "20261009T030000Z"+indexSuffix)
	w, err := newIndexWriter(file, "20261009T030000Z")
	if err != nil {
		t.Fatal(err)
	}
	add := func(name, kind string, size int64, link string) {
		w.add(indexEntry{Path: name, Type: kind, Size: size, Link: link})
	}
	add(".", typeDir, 0, "")
	add("./etc", typeDir, 0, "")
	add("./etc/hosts", typeFile, 200, "")
	add("./home", typeDir, 0, "")
	add("./home/me", typeDir, 0, "")
	add("./home/me/shared.bin", typeFile, 1000, "")
	add("./home/me/project", typeDir, 0, "")
	add("./home/me/project/main.go", typeFile, 300, "")
	add("./home/me/project/data", typeDir, 0, "")
	add("./home/me/project/data/copy.bin", typeHardlink, 0, "./home/me/shared.bin")
	add("./home/me/project/inner-link", typeHardlink, 0, "./home/me/project/main.go")
	add("./home/me/project-old", typeDir, 0, "")
	add("./home/me/project-old/main.go", typeFile, 50, "")
	if err := w.close(true); err != nil {
		t.Fatal(err)
	}

	sel, stats, err := resolveSelection(file, []string{"/home/me/project/", "/etc/hosts", "home/me/project"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.roots) != 2 {
		t.Errorf("the same path given twice should be kept once: %q", sel.roots)
	}
	// 目錄、它底下的五個項目，加上 /etc/hosts。
	if stats.Entries != 6 || stats.Bytes != 500 || len(stats.Missing) != 0 {
		t.Errorf("stats = %+v", stats)
	}
	// 選中的硬連結指向選擇範圍外的檔案：那個檔案要一起帶上；指向範圍內的不用另外帶。
	if !sel.extras["./home/me/shared.bin"] || len(sel.extras) != 1 {
		t.Errorf("extras = %v", sel.extras)
	}
	for name, want := range map[string]bool{
		"./home/me/project":               true,
		"./home/me/project/":              true, // tar 裡的目錄名稱帶結尾斜線
		"./home/me/project/main.go":       true,
		"./home/me/project/data/copy.bin": true,
		"./etc/hosts":                     true,
		"./home/me/shared.bin":            true,  // 硬連結的對象
		"./home/me/project-old":           false, // 只是名稱的開頭相同
		"./home/me/project-old/main.go":   false,
		"./home/me":                       false,
		"./etc":                           false,
		"./etc/hostsfile":                 false,
		".":                               false,
	} {
		if sel.wants(name) != want {
			t.Errorf("wants(%q) = %v, want %v", name, !want, want)
		}
	}

	_, stats, err = resolveSelection(file, []string{"/etc/hosts", "/etc/shadow", "/nope/x"})
	if err != nil || !reflect.DeepEqual(stats.Missing, []string{"/etc/shadow", "/nope/x"}) {
		t.Errorf("missing paths: %v, %v", stats.Missing, err)
	}
	if _, _, err = resolveSelection(file, []string{"/"}); err == nil {
		t.Error("selecting the root should be refused")
	}
	if _, _, err = resolveSelection(file, []string{"/etc/../home"}); err == nil {
		t.Error("a path with .. should be refused")
	}
}

func TestExtractArgs(t *testing.T) {
	link := "/.wslbak-restore-0123456789abcdef"
	args := extractArgs("Ubuntu", "tar", link, nil)
	joined := strings.Join(args, " ")
	for _, want := range []string{"-d Ubuntu -u root -e env LC_ALL=C tar", "--extract", "--file=-", "--directory=" + link,
		"--numeric-owner", "--keep-old-files", "--xattrs", "--xattrs-include=*", "--acls"} {
		if !strings.Contains(joined, want) {
			t.Errorf("extract arguments lack %q: %s", want, joined)
		}
	}
	// 備份時這個 distro 的 tar 不支援的選項，解開時也不能用。
	if joined := strings.Join(extractArgs("Ubuntu", "tar", link, []string{"--acls"}), " "); strings.Contains(joined, "--acls") || !strings.Contains(joined, "--xattrs") {
		t.Errorf("with --acls dropped: %s", joined)
	}
	if joined := strings.Join(extractArgs("Ubuntu", "tar", link, []string{"--acls", "--xattrs"}), " "); strings.Contains(joined, "--acls") || strings.Contains(joined, "--xattrs") {
		t.Errorf("with --xattrs dropped: %s", joined)
	}
	// 腳本回報的連結要是我們自己的格式才會被用在命令列上。
	for value, want := range map[string]bool{
		"/.wslbak-restore-0123456789abcdef":         true,
		"/tmp/.wslbak-restore-0123456789abcdef":     false,
		"/.wslbak-restore-0123456789abcde":          false,
		"/.wslbak-restore-0123456789abcdef/../etc":  false,
		"/.wslbak-restore-0123456789ABCDEF":         false,
		"/home/me/wslbak-0123456789abcdef":          false,
		"/.wslbak-restore-0123456789abcdef --force": false,
		"--directory=/etc":                          false,
		"":                                          false,
	} {
		if stagingRe.MatchString(value) != want {
			t.Errorf("stagingRe.MatchString(%q) should be %v", value, want)
		}
	}
}

func TestFirstStart(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 10, 9, 16, 0, 0, 0, loc)
	cases := map[string]string{
		"03:00": "2026-10-10T03:00:00", // 今天的已經過了
		"16:00": "2026-10-10T16:00:00", // 剛好是現在：也算過了
		"16:01": "2026-10-09T16:01:00", // 今天稍後
		"23:59": "2026-10-09T23:59:00",
		"bogus": "2026-10-10T03:00:00", // 不合法的值退回預設的 03:00
	}
	for at, want := range cases {
		got := firstStart(at, now)
		if got.Format("2006-01-02T15:04:05") != want || !got.After(now) {
			t.Errorf("firstStart(%q) = %s, want %s (and always in the future)", at, got.Format("2006-01-02T15:04:05"), want)
		}
	}
	// 月底跨到下個月。
	if got := firstStart("03:00", time.Date(2026, 10, 31, 12, 0, 0, 0, loc)); got.Format("2006-01-02") != "2026-11-01" {
		t.Errorf("month end: %v", got)
	}
}

// wsl.exe 自己印在 stderr 的警告不是 tar 的訊息。
func TestStderrLogIgnoresWSLNotes(t *testing.T) {
	var l stderrLog
	for _, line := range []string{
		"wsl: Failed to start the systemd user session for 'root'. See journalctl for more details.",
		"wsl: 偵測到 localhost Proxy 設定，但未鏡像到 WSL。",
		"@wslbak\ttar-version\ttar (GNU tar) 1.35",
		"tar: ./home/me: file changed as we read it",
		"Total bytes written: 10240 (10KiB, 5.4MiB/s)",
		"@wslbak\ttar-exit\t1",
		"@wslbak\tdone",
	} {
		l.add(line)
	}
	if len(l.notes) != 2 || len(l.tar) != 1 || l.totals != 10240 || len(l.proto) != 3 {
		t.Errorf("notes=%d tar=%q totals=%d proto=%d", len(l.notes), l.tar, l.totals, len(l.proto))
	}
	warnings, fatal := classifyTarStderr(l.tar)
	if len(warnings) != 1 || len(fatal) != 0 {
		t.Errorf("warnings=%q fatal=%q", warnings, fatal)
	}
}

// 放進備份資料夾的說明檔是給人照著打的：指令裡不能出現寫錯的反斜線。
func TestRestoreReadme(t *testing.T) {
	for _, c := range []catalog{zhTW, enUS} {
		if strings.Contains(c.RestoreReadme, `\\`) {
			t.Errorf("the restore instructions contain a doubled backslash:\n%s", c.RestoreReadme)
		}
		for _, want := range []string{`.\wslbak.exe restore`, `.\wslbak.exe files`, "wsl --import", `.tar.gz --version 2`, "--path", "--into"} {
			if !strings.Contains(c.RestoreReadme, want) {
				t.Errorf("the restore instructions lack %q", want)
			}
		}
	}
}

// 測試用的故障只在沙箱（--home）裡生效，而且恰好在指定的位元組數之後回報磁碟已滿。
func TestTestFaults(t *testing.T) {
	t.Setenv(envTestDiskFullAfter, "5")
	t.Setenv(envTestToast, "1")
	defer func(old string) { homeOverride = old }(homeOverride)

	homeOverride = ""
	var plain bytes.Buffer
	if w := withTestFaults(&plain); w != io.Writer(&plain) {
		t.Errorf("without --home the writer must be returned unchanged")
	}
	if !toastAllowed() {
		t.Errorf("without --home notifications are always allowed")
	}

	homeOverride = `C:\sandbox`
	var out bytes.Buffer
	w := withTestFaults(&out)
	if n, err := w.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("first write: %d, %v", n, err)
	}
	n, err := w.Write([]byte("defgh"))
	if n != 2 || !errors.Is(err, windows.ERROR_DISK_FULL) {
		t.Errorf("second write: %d, %v; want 2 bytes and a full disk", n, err)
	}
	if out.String() != "abcde" {
		t.Errorf("wrote %q, want %q", out.String(), "abcde")
	}
	if n, err := w.Write([]byte("x")); n != 0 || !errors.Is(err, windows.ERROR_DISK_FULL) {
		t.Errorf("later writes must keep failing: %d, %v", n, err)
	}
	if !toastAllowed() {
		t.Errorf("the sandbox must allow notifications when asked to")
	}
	t.Setenv(envTestToast, "")
	if toastAllowed() {
		t.Errorf("the sandbox must not raise notifications by default")
	}
	t.Setenv(envTestDiskFullAfter, "nonsense")
	if w := withTestFaults(&plain); w != io.Writer(&plain) {
		t.Errorf("a value that is not a number must be ignored")
	}
}

// 來自 distro 的文字在顯示之前，控制字元要換成看得見的寫法。
func TestPlain(t *testing.T) {
	for in, want := range map[string]string{
		"notes.md":         "notes.md",
		"中文檔名 with space":  "中文檔名 with space",
		"a\tb":             "a\tb",
		"evil\x1b[2Jname":  `evil\x1b[2Jname`,
		"two\nlines":       `two\x0alines`,
		"bell\x07":         `bell\x07`,
		"c1\u009bcsi":      `c1\x9bcsi`,
		"rtl\u202egnp.exe": `rtl\u202egnp.exe`,
		"del\x7f":          `del\x7f`,
	} {
		if got := plain(in); got != want {
			t.Errorf("plain(%q) = %q, want %q", in, got, want)
		}
	}
}

// 備份資料夾的權限：其他帳號讀得到時要提醒。
func TestOthersCanRead(t *testing.T) {
	for sddl, want := range map[string]bool{
		// 資料磁碟的預設：已驗證的使用者可以修改。
		"O:BAG:S-1-5-21-1-2-3-513D:AI(A;OICIID;FA;;;BA)(A;OICIID;FA;;;SY)(A;OICIID;0x1301bf;;;AU)(A;OICIID;0x1200a9;;;BU)": true,
		// 使用者自己的設定檔資料夾底下。
		"O:S-1-5-21-1-2-3-1001G:S-1-5-21-1-2-3-513D:AI(A;OICIID;FA;;;SY)(A;OICIID;FA;;;BA)(A;OICIID;FA;;;S-1-5-21-1-2-3-1001)": false,
		"D:(A;;FR;;;WD)":                         true,
		"D:(A;OICI;FA;;;S-1-5-11)":               true,
		"D:(D;;FA;;;WD)(A;;FA;;;BA)":             false,
		"D:PAI(A;OICI;FA;;;BA)S:(AU;SA;FA;;;WD)": false,
		"D:NO_ACCESS_CONTROL":                    true,
		"O:BAG:BA":                               false,
		"":                                       false,
	} {
		if got := othersCanRead(sddl); got != want {
			t.Errorf("othersCanRead(%q) = %v, want %v", sddl, got, want)
		}
	}
}

// 最大的幾個檔案：試還原要確認它們的大小，少一個或短了都算沒過。
func TestLargestFiles(t *testing.T) {
	var idx tarIndex
	for i, size := range []int64{5, 90, 30, 70, 10, 80, 20, 60, 50, 40, 100} {
		idx.noteLargest(fmt.Sprintf("./f%d", i), size)
	}
	var got []int64
	for _, f := range idx.Largest {
		got = append(got, f.Size)
	}
	if want := []int64{100, 90, 80, 70, 60, 50, 40, 30}; !slices.Equal(got, want) {
		t.Errorf("largest = %v, want %v", got, want)
	}

	base := "@wslbak\tpid1\tinit\n@wslbak\tsystemd\tno\n@wslbak\twindows-drives\t0\n@wslbak\tinterop\tno\n" +
		"@wslbak\twslconf\t" + func() string { s := sha256.Sum256([]byte(inertWSLConf)); return hex.EncodeToString(s[:]) }() + "\n" +
		"@wslbak\tsamples\t3\t0\n"
	if reason, detail := judgeCheck(parseCheck(base+"@wslbak\tsizes\t2\t0\n@wslbak\tdone\n"), 3, 2); reason != "" {
		t.Errorf("all sizes right: %s (%s)", reason, detail)
	}
	reason, detail := judgeCheck(parseCheck(base+"@wslbak\tsize-bad\t./big.bin\texpected 9663676416, found 0\n@wslbak\tsizes\t1\t1\n@wslbak\tdone\n"), 3, 2)
	if reason != reasonSamples || !strings.Contains(detail, "./big.bin") {
		t.Errorf("a file that came back empty: reason=%q detail=%q", reason, detail)
	}
	if reason, _ := judgeCheck(parseCheck(base+"@wslbak\tdone\n"), 3, 2); reason != reasonSamples {
		t.Errorf("sizes that were never checked must not pass: %q", reason)
	}

	// 匯入端抱怨封存壞掉時，就算 wsl.exe 回報成功也不算數；它的輸出只留前面一段。
	if !importComplained("bsdtar: Damaged tar archive\nbsdtar: Retrying...") || importComplained("The operation completed successfully.") {
		t.Errorf("importComplained is wrong")
	}
	out := &cappedBuffer{limit: 10}
	out.Write([]byte("0123456"))
	out.Write([]byte("789abcdef"))
	out.Write([]byte("more"))
	if out.buf.String() != "0123456789" || out.dropped != 10 {
		t.Errorf("capped buffer kept %q and dropped %d", out.buf.String(), out.dropped)
	}
}

// 解開檔案用的 tar：腳本回報的位置要是單純的絕對路徑才會被放上命令列。
func TestTarProgram(t *testing.T) {
	for _, c := range []struct {
		reported []string
		want     string
	}{
		{[]string{"/usr/bin/tar"}, "/usr/bin/tar"},
		{[]string{"/run/current-system/sw/bin/tar"}, "/run/current-system/sw/bin/tar"},
		{[]string{"/nix/store/0c6kd9zgyw0kq5vkjm1hsg2r0qk3mv29-gnutar-1.35/bin/tar"}, "/nix/store/0c6kd9zgyw0kq5vkjm1hsg2r0qk3mv29-gnutar-1.35/bin/tar"},
		{nil, "tar"},
		{[]string{""}, "tar"},
		{[]string{"tar"}, "tar"},
		{[]string{"/usr/bin/tar", "extra"}, "tar"},
		{[]string{"/usr/bin/tar --to-command=evil"}, "tar"},
		{[]string{"/usr/bin/evil"}, "tar"},
		{[]string{"/tmp/my dir/tar"}, "tar"},
		{[]string{"/tmp/$(id)/tar"}, "tar"},
		{[]string{"/" + strings.Repeat("a", 300) + "/tar"}, "tar"},
	} {
		if got := tarProgram(c.reported); got != c.want {
			t.Errorf("tarProgram(%q) = %q, want %q", c.reported, got, c.want)
		}
	}
	args := extractArgs("NixOS", "/run/current-system/sw/bin/tar", "/.wslbak-restore-0123456789abcdef", nil)
	if !slices.Contains(args, "/run/current-system/sw/bin/tar") || slices.Contains(args, "tar") {
		t.Errorf("the reported tar is not what gets started: %q", args)
	}
}

// 該給 distro 裡的路徑卻收到 Windows 路徑（Git Bash 改寫過的）時，要說得出原因與解法。
func TestWindowsPathGiven(t *testing.T) {
	defer func(old *catalog) { T = old }(T)
	T = &enUS
	for _, args := range [][]string{
		{"restore", "--path", "C:/Program Files/Git/home/me/notes.md", "--into", "/home/me/back"},
		{"restore", "--path", "/home/me/notes.md", "--into", `C:\Users\me\AppData\Local\Temp\back`},
		{"config", "--exclude", "C:/Program Files/Git/home/me/Downloads"},
	} {
		_, err := parseArgs(args)
		if err == nil || !strings.Contains(err.Error(), "MSYS_NO_PATHCONV") {
			t.Errorf("%q: error = %v, want one that explains Git Bash", args, err)
		}
	}
	for _, ok := range []string{"/home/me", "//home/me", "./home/me", "relative/C:/x"} {
		if windowsPathRe.MatchString(ok) {
			t.Errorf("%q is not a Windows path", ok)
		}
	}
}

// config --private：權限設定的寫法，以及「資料夾裡有別人的東西就不動」的判斷。
func TestPrivate(t *testing.T) {
	sddl := privateSDDL("S-1-5-21-1-2-3-1001")
	if othersCanRead(sddl) {
		t.Errorf("the private permissions still let other accounts in: %s", sddl)
	}
	if !strings.HasPrefix(sddl, "D:P") || !strings.Contains(sddl, ";;;S-1-5-21-1-2-3-1001)") {
		t.Errorf("unexpected permissions: %s", sddl)
	}
	dirs := map[string]bool{"Ubuntu": true, "old-distro": true, "Photos": true}
	ours := map[string]bool{"Ubuntu": true, "old-distro": true}
	got := foreignEntries([]string{"Ubuntu", "old-distro", "WSLBAK.EXE", "README-RESTORE.txt", "Photos", "notes.txt"},
		func(n string) bool { return dirs[n] }, func(n string) bool { return ours[n] })
	if want := []string{"Photos", "notes.txt"}; !slices.Equal(got, want) {
		t.Errorf("foreign entries = %q, want %q", got, want)
	}
}

// 結束不掉的子行程：等到期限就放手，不能跟著永遠等下去。
func TestWaitBounded(t *testing.T) {
	boom := errors.New("boom")
	// 自己結束的，原樣回傳它的結果。
	if err := waitBounded(context.Background(), func() error { return boom }, time.Hour); err != boom {
		t.Errorf("a program that ends by itself: got %v, want its own error", err)
	}
	// 被要求結束之後在期限內結束的，也回傳它的結果。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitBounded(ctx, func() error { time.Sleep(20 * time.Millisecond); return boom }, 5*time.Second); err != boom {
		t.Errorf("a program that ends soon after being stopped: got %v, want its own error", err)
	}
	// 一直不結束的：過了期限就回傳 errWontEnd，而且確實等了那麼久。
	never := make(chan struct{})
	defer close(never)
	began := time.Now()
	err := waitBounded(ctx, func() error { <-never; return nil }, 80*time.Millisecond)
	if !errors.Is(err, errWontEnd) {
		t.Errorf("a program that never ends: got %v, want errWontEnd", err)
	}
	if waited := time.Since(began); waited < 80*time.Millisecond || waited > 3*time.Second {
		t.Errorf("waited %v for a program that never ends, want about 80ms", waited)
	}
	// 還沒被要求結束的，不管多久都繼續等（備份本來就可以跑很久）。
	slow := make(chan error, 1)
	go func() {
		slow <- waitBounded(context.Background(), func() error { time.Sleep(150 * time.Millisecond); return nil }, time.Millisecond)
	}()
	select {
	case err := <-slow:
		if err != nil {
			t.Errorf("a slow program that was never stopped: got %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("waitBounded did not return after the program ended")
	}
}

// 鎖檔裡的開始時間：只認我們自己寫的格式，其他一律當作不知道。
func TestLockStamp(t *testing.T) {
	began := time.Date(2026, 10, 10, 19, 30, 0, 0, time.UTC)
	stamp := []byte(began.Format(time.RFC3339))
	if len(stamp) != lockStampLen {
		t.Fatalf("a stamp is %d bytes, lockStampLen says %d", len(stamp), lockStampLen)
	}
	if got, ok := parseLockStamp(stamp); !ok || !got.Equal(began) {
		t.Errorf("parseLockStamp(%q) = %v, %v", stamp, got, ok)
	}
	for _, bad := range [][]byte{nil, {}, make([]byte, lockStampLen), []byte("2026-10-10T19:30:00"), []byte("not a time at all!!!")} {
		if _, ok := parseLockStamp(bad); ok {
			t.Errorf("parseLockStamp(%q) was accepted", bad)
		}
	}
	// 20 小時的門檻要比一天短，隔天同一時間的排程才看得到。
	if stuckAfter >= 24*time.Hour {
		t.Errorf("stuckAfter is %v; the next day's run starts just under 24 hours later and would not notice", stuckAfter)
	}
}
