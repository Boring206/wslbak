package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		args []string
		want options
	}{
		{nil, options{}},
		{[]string{"--help"}, options{help: true}},
		{[]string{"/?"}, options{help: true}},
		{[]string{"-v"}, options{version: true}},
		// --help 與 --version 不檢查其他參數對不對：使用者就是來查用法的。
		{[]string{"nonsense", "--help"}, options{help: true}},
		{[]string{"run"}, options{command: "run"}},
		{[]string{"run", "-d", "Ubuntu", "--no-verify", "-n"}, options{command: "run", distro: "Ubuntu", noVerify: true, dryRun: true}},
		{[]string{"--debug", "run", "--scheduled"}, options{command: "run", scheduled: true, debug: true}},
		{[]string{"init", "--dest=D:\\WSL Backup", "--keep", "3", "--at", "3:05", "--webhook", "https://ntfy.sh/x", "-y"},
			options{command: "init", dest: `D:\WSL Backup`, keep: 3, at: "03:05", webhook: "https://ntfy.sh/x", yes: true}},
		{[]string{"restore", "20261008T030000Z", "--name", "Ubuntu-2", "--to", `C:\WSL\Ubuntu-2`},
			options{command: "restore", id: "20261008T030000Z", name: "Ubuntu-2", to: `C:\WSL\Ubuntu-2`}},
		{[]string{"--home", `C:\tmp\sandbox`, "verify"}, options{command: "verify", home: `C:\tmp\sandbox`}},
		{[]string{"list", "--lang", "en"}, options{command: "list"}},
		// 值可以長得像旗標：distro 名稱不會以 - 開頭，但路徑或名稱由使用者決定。
		{[]string{"restore", "--name", "-odd"}, options{command: "restore", name: "-odd"}},
		{[]string{"init", "--all", "--keep-weekly", "4", "--keep-monthly=6"}, options{command: "init", all: true, keepWeekly: 4, keepMonthly: 6}},
		{[]string{"config"}, options{command: "config"}},
		// files [編號] [路徑]：第一個長得像編號才當成編號。
		{[]string{"files"}, options{command: "files"}},
		{[]string{"files", "/etc"}, options{command: "files", target: "/etc"}},
		{[]string{"files", "20261008T030000Z"}, options{command: "files", id: "20261008T030000Z"}},
		{[]string{"files", "20261008T030000Z", "/home/me", "-d", "Ubuntu"}, options{command: "files", id: "20261008T030000Z", target: "/home/me", distro: "Ubuntu"}},
		{[]string{"files", "--find", "notes.txt"}, options{command: "files", find: "notes.txt"}},
		{[]string{"doctor", "-d", "Ubuntu"}, options{command: "doctor", distro: "Ubuntu"}},
		// --exclude 可以重複，寫 / 或 ./ 開頭都行，存下來一律是 ./ 開頭。
		{[]string{"config", "-d", "Ubuntu", "--exclude", "/home/*/Downloads/*", "--exclude=./var/lib/docker/*", "--unexclude", "/tmp/*"},
			options{command: "config", distro: "Ubuntu", exclude: []string{"./home/*/Downloads/*", "./var/lib/docker/*"}, unexclude: []string{"./tmp/*"}}},
		{[]string{"config", "--notify", "always", "--verify", "none", "--webhook", "off", "--keep-weekly", "0", "--disable"},
			options{command: "config", notify: "always", verify: "none", webhook: "off", disable: true}},
	}
	for _, c := range cases {
		got, err := parseArgs(c.args)
		if err != nil {
			t.Errorf("parseArgs(%q): unexpected error %v", c.args, err)
			continue
		}
		// given 另外在 TestParseArgsGiven 檢查。
		got.given = nil
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseArgs(%q)\n  got  %+v\n  want %+v", c.args, got, c.want)
		}
	}
}

func TestParseArgsErrors(t *testing.T) {
	cases := []struct {
		args []string
		want string // 錯誤訊息裡要出現的片段
	}{
		{[]string{"frobnicate"}, "不認得的指令：frobnicate"},
		{[]string{"run", "--bogus"}, "不認得的參數：--bogus"},
		{[]string{"run", "-x"}, "不認得的參數：-x"},
		{[]string{"run", "--dry-run=1"}, "不認得的參數：--dry-run=1"},
		{[]string{"--dry-run"}, "要指定一個指令"},
		{[]string{"run", "--dest", `D:\x`}, "--dest 不能用在 run 指令"},
		{[]string{"status", "-d", "Ubuntu"}, "--distro 不能用在 status 指令"},
		{[]string{"list", "extra"}, "多出來的參數：extra"},
		{[]string{"restore", "a", "b"}, "多出來的參數：b"},
		{[]string{"init", "--keep"}, "--keep 後面要接一個值"},
		{[]string{"init", "--dest="}, "--dest 後面要接一個值"},
		{[]string{"run", "-d"}, "-d 後面要接一個值"},
		{[]string{"init", "--keep", "0"}, "保留份數"},
		{[]string{"init", "--keep", "many"}, "保留份數"},
		{[]string{"init", "--at", "24:00"}, "HH:MM"},
		{[]string{"init", "--at", "3pm"}, "HH:MM"},
		{[]string{"run", "--lang", "fr"}, "不支援的語言：fr"},
		{[]string{"config", "--keep-weekly", "-1"}, "0 以上的整數"},
		{[]string{"config", "--keep-monthly", "x"}, "0 以上的整數"},
		{[]string{"config", "--notify", "sometimes"}, "failure 或 always"},
		{[]string{"config", "--verify", "maybe"}, "restore 或 none"},
		{[]string{"config", "--exclude", "node_modules"}, "排除樣式"},
		{[]string{"config", "--exclude", "/"}, "排除樣式"},
		{[]string{"config", "--exclude", "/a\nb"}, "排除樣式"},
		{[]string{"config", "--enable", "--disable"}, "--enable 和 --disable 不能同時使用"},
		{[]string{"init", "--all", "-d", "Ubuntu"}, "--all 和 --distro 不能同時使用"},
		{[]string{"run", "--exclude", "/tmp/x"}, "--exclude 不能用在 run 指令"},
		{[]string{"init", "--notify", "always"}, "--notify 不能用在 init 指令"},
		{[]string{"run", "--lang"}, "--lang 後面要接一個值"},
		{[]string{"files", "/etc", "/home"}, "多出來的參數：/home"},
		{[]string{"files", "20261008T030000Z", "/etc", "extra"}, "多出來的參數：extra"},
		{[]string{"list", "--find", "x"}, "--find 不能用在 list 指令"},
	}
	for _, c := range cases {
		_, err := parseArgs(c.args)
		if err == nil {
			t.Errorf("parseArgs(%q): expected an error", c.args)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("parseArgs(%q): error %q does not contain %q", c.args, err, c.want)
		}
	}
}

func TestNormalizeClock(t *testing.T) {
	good := map[string]string{"03:00": "03:00", "3:00": "03:00", "0:00": "00:00", "23:59": "23:59"}
	for in, want := range good {
		if got, ok := normalizeClock(in); !ok || got != want {
			t.Errorf("normalizeClock(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "24:00", "12:60", "12", "12:5", "ab:cd", "1:2:3", " 3:00"} {
		if _, ok := normalizeClock(in); ok {
			t.Errorf("normalizeClock(%q) should be rejected", in)
		}
	}
}

// 有些值的零值有意義，所以要另外知道使用者有沒有給那個旗標。
func TestParseArgsGiven(t *testing.T) {
	o, err := parseArgs([]string{"config", "--keep-weekly", "0", "-d", "Ubuntu", "--lang=en"})
	if err != nil {
		t.Fatal(err)
	}
	for flag, want := range map[string]bool{"--keep-weekly": true, "--distro": true, "--lang": true, "--keep": false, "--keep-monthly": false} {
		if o.has(flag) != want {
			t.Errorf("has(%s) = %v, want %v", flag, o.has(flag), want)
		}
	}
	if o.keepWeekly != 0 {
		t.Errorf("keepWeekly = %d", o.keepWeekly)
	}
}

func TestNormalizePattern(t *testing.T) {
	good := map[string]string{
		"/home/*/Downloads/*": "./home/*/Downloads/*",
		"./var/cache/*":       "./var/cache/*",
		"/opt/big file.iso":   "./opt/big file.iso",
		"/a":                  "./a",
	}
	for in, want := range good {
		if got, ok := normalizePattern(in); !ok || got != want {
			t.Errorf("normalizePattern(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	// 沒有開頭斜線的樣式會比對到任何一層的同名檔案，範圍難以預期；整個根目錄也不能排除。
	for _, in := range []string{"", "node_modules", "*.log", "/", "./", "/a\nb", "/a\x00b", `C:\Users`} {
		if _, ok := normalizePattern(in); ok {
			t.Errorf("normalizePattern(%q) should be rejected", in)
		}
	}
}
