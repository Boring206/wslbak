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
	}
	for _, c := range cases {
		got, err := parseArgs(c.args)
		if err != nil {
			t.Errorf("parseArgs(%q): unexpected error %v", c.args, err)
			continue
		}
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
		{[]string{"run", "--lang"}, "--lang 後面要接一個值"},
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
