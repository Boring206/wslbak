// wslbak 替 WSL distro 做排程備份：不停機、備份完自動試還原、失敗會通知，並能一行還原。
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// version 在建置時由 -ldflags "-X main.version=…" 注入。
var version = "dev"

type options struct {
	command string
	id      string // verify／restore 指定的備份編號；空的表示最新一份

	distro  string
	dest    string
	keep    int
	at      string
	webhook string
	name    string
	to      string
	home    string // 測試用：把設定、紀錄與驗證用的暫存都放到這個資料夾

	dryRun    bool
	noVerify  bool
	scheduled bool // 由排程工作啟動：還沒到期就直接結束，也不詢問任何事
	yes       bool
	help      bool
	version   bool
	debug     bool
}

// commandFlags 是每個指令各自接受的旗標；全域旗標（globalFlags）不列在這裡。
var commandFlags = map[string][]string{
	"init":      {"--distro", "--dest", "--keep", "--at", "--webhook", "--dry-run", "--yes"},
	"run":       {"--distro", "--no-verify", "--dry-run", "--scheduled"},
	"list":      {"--distro"},
	"status":    {},
	"verify":    {"--distro"},
	"restore":   {"--distro", "--name", "--to", "--dry-run", "--yes"},
	"uninstall": {"--dry-run", "--yes"},
}

// takesID 是後面可以再接一個備份編號的指令。
var takesID = map[string]bool{"verify": true, "restore": true}

var globalFlags = map[string]bool{"--lang": true, "--home": true, "--debug": true, "--help": true, "--version": true}

// valueFlags 後面要接一個值，boolFlags 不用。
var (
	valueFlags = map[string]bool{
		"--distro": true, "--dest": true, "--keep": true, "--at": true, "--webhook": true,
		"--name": true, "--to": true, "--home": true, "--lang": true,
	}
	boolFlags = map[string]bool{
		"--dry-run": true, "--no-verify": true, "--scheduled": true, "--yes": true,
		"--help": true, "--version": true, "--debug": true,
	}
	shortFlags = map[string]string{"-d": "--distro", "-n": "--dry-run", "-y": "--yes", "-h": "--help", "-v": "--version"}
)

var clockRe = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)

// normalizeClock 把 3:05 這類寫法整理成 03:05；不是合法的 24 小時制時間就回傳 false。
func normalizeClock(s string) (string, bool) {
	m := clockRe.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	hour, _ := strconv.Atoi(m[1])
	minute, _ := strconv.Atoi(m[2])
	if hour > 23 || minute > 59 {
		return "", false
	}
	return fmt.Sprintf("%02d:%02d", hour, minute), true
}

func (o *options) setValue(name, value string) error {
	switch name {
	case "--distro":
		o.distro = value
	case "--dest":
		o.dest = value
	case "--keep":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return fmt.Errorf(T.BadKeep, value)
		}
		o.keep = n
	case "--at":
		clock, ok := normalizeClock(value)
		if !ok {
			return fmt.Errorf(T.BadTime, value)
		}
		o.at = clock
	case "--webhook":
		o.webhook = value
	case "--name":
		o.name = value
	case "--to":
		o.to = value
	case "--home":
		o.home = value
	case "--lang":
		// 值在這裡只檢查對不對，實際套用由 pickLanguage 在更早的時候完成。
		if _, ok := parseLanguage(value); !ok {
			return fmt.Errorf(T.BadLang, value)
		}
	}
	return nil
}

func (o *options) setBool(name string) {
	switch name {
	case "--dry-run":
		o.dryRun = true
	case "--no-verify":
		o.noVerify = true
	case "--scheduled":
		o.scheduled = true
	case "--yes":
		o.yes = true
	case "--help":
		o.help = true
	case "--version":
		o.version = true
	case "--debug":
		o.debug = true
	}
}

// parseArgs 解析參數；旗標可以放在指令前面或後面，值可以寫成 --flag 值 或 --flag=值。
func parseArgs(args []string) (options, error) {
	var o options
	var used, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, inline := a, "", false
		if long, ok := shortFlags[a]; ok {
			name = long
		} else if eq := strings.IndexByte(a, '='); eq > 0 && strings.HasPrefix(a, "--") {
			name, value, inline = a[:eq], a[eq+1:], true
		}
		switch {
		case valueFlags[name]:
			if !inline {
				if i+1 >= len(args) {
					return o, fmt.Errorf(T.NeedValue, a)
				}
				i++
				value = args[i]
			}
			if value == "" {
				return o, fmt.Errorf(T.NeedValue, name)
			}
			if err := o.setValue(name, value); err != nil {
				return o, err
			}
		case boolFlags[name] && !inline:
			o.setBool(name)
		case a == "/?":
			o.help = true
			continue
		case len(a) > 1 && a[0] == '-':
			return o, fmt.Errorf(T.UnknownFlag, a)
		default:
			positional = append(positional, a)
			continue
		}
		if !globalFlags[name] {
			used = append(used, name)
		}
	}
	if o.help || o.version {
		return o, nil
	}
	if len(positional) == 0 {
		if len(used) > 0 {
			return o, errors.New(T.NeedCommand)
		}
		return o, nil
	}
	o.command = positional[0]
	allowed, ok := commandFlags[o.command]
	if !ok {
		return o, fmt.Errorf(T.UnknownCommand, o.command)
	}
	for _, flag := range used {
		if !slices.Contains(allowed, flag) {
			return o, fmt.Errorf(T.FlagNotForCommand, flag, o.command)
		}
	}
	rest := positional[1:]
	if len(rest) > 0 && takesID[o.command] {
		o.id, rest = rest[0], rest[1:]
	}
	if len(rest) > 0 {
		return o, fmt.Errorf(T.UnexpectedArg, rest[0])
	}
	return o, nil
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	// 語言要在解析參數之前決定，參數錯誤的訊息才會用對語言。
	system := systemLanguageTags()
	setLanguage(pickLanguage(args, os.Getenv(envLang), system))

	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, T.ErrorWithHint, err)
		return 2
	}
	initConsole()
	debugEnabled = opts.debug
	if debugEnabled {
		// 從 Windows 建立行程到程式開始執行的時間：這段若很長，延遲發生在程式之外（例如防毒軟體掃描執行檔）。
		var created, exit, kernel, user windows.Filetime
		if windows.GetProcessTimes(windows.CurrentProcess(), &created, &exit, &kernel, &user) == nil {
			debugf("process creation to program start: %v", time.Since(time.Unix(0, created.Nanoseconds())).Round(time.Millisecond))
		}
		debugf("version %s, system UI languages %v, %s=%q", version, system, envLang, os.Getenv(envLang))
	}
	switch {
	case opts.version:
		fmt.Println("wslbak " + version)
		return 0
	case opts.help || opts.command == "":
		fmt.Print(T.Usage)
		return 0
	}
	if opts.home != "" {
		home, err := filepath.Abs(opts.home)
		if err != nil {
			return fail(err)
		}
		homeOverride = home
	}
	// 我們不管怎麼結束，啟動的 wsl.exe 都要跟著結束。
	initJob()
	// 會動到東西的指令才開紀錄檔。--dry-run 只看不動，連紀錄檔都不建立。
	if !opts.dryRun {
		switch opts.command {
		case "init", "run", "verify", "restore", "uninstall":
			openRunLog()
		}
		if opts.command != "uninstall" {
			ensureInstalledFresh()
		}
	}
	switch opts.command {
	case "init":
		return cmdInit(opts)
	case "run":
		return cmdRun(opts)
	case "list":
		return cmdList(opts)
	case "status":
		return cmdStatus(opts)
	case "verify":
		return cmdVerify(opts)
	case "restore":
		return cmdRestore(opts)
	}
	return cmdUninstall(opts)
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, red(fmt.Sprintf(T.ErrorLine, err)))
	return 2
}
