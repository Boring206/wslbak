package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
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
	for name, script := range map[string]string{"backup.sh": backupScript, "check.sh": checkScript, "probe.sh": probeScript} {
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
	if reason, detail := judgeCheck(parseCheck(good), 3); reason != "" {
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
		if got, _ := judgeCheck(c.report, 3); got != c.want {
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

func TestPlanPrune(t *testing.T) {
	// 編號越大越新。v = 通過試還原，u = 沒通過。
	v, u := true, false
	cases := []struct {
		label   string
		backups []*manifest
		keep    int
		verify  string
		want    string
	}{
		{"nothing to do", []*manifest{backup("1", v), backup("2", v)}, 3, verifyRestore, ""},
		{"oldest verified go first", []*manifest{backup("1", v), backup("2", v), backup("3", v), backup("4", v)}, 2, verifyRestore, "2 1"},
		// 連續失敗不會把好的備份擠掉：失敗的自己另外算，最多留兩份。
		{"failures do not push out good backups", []*manifest{backup("1", v), backup("2", u), backup("3", u), backup("4", u)}, 1, verifyRestore, "2"},
		{"the newest verified one survives any number of failures", []*manifest{backup("1", v), backup("2", u), backup("3", u), backup("4", u), backup("5", u)}, 1, verifyRestore, "3 2"},
		{"only unverified backups", []*manifest{backup("1", u), backup("2", u), backup("3", u)}, 5, verifyRestore, "1"},
		{"keep below one is treated as one", []*manifest{backup("1", v), backup("2", v)}, 0, verifyRestore, "1"},
		// 關掉試還原時不分通過與否，留最新的 keep 份。
		{"verification turned off", []*manifest{backup("1", u), backup("2", v), backup("3", u), backup("4", u)}, 2, verifyNone, "2 1"},
		{"unsorted input", []*manifest{backup("3", v), backup("1", v), backup("2", v)}, 1, verifyRestore, "2 1"},
		{"empty", nil, 3, verifyRestore, ""},
	}
	for _, c := range cases {
		if got := ids(planPrune(c.backups, c.keep, c.verify)); got != c.want {
			t.Errorf("%s: would delete %q, want %q", c.label, got, c.want)
		}
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
	if doc.Triggers.Calendar.StartBoundary != "2026-01-01T03:05:00" {
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
