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
	id      string // verify／restore／files 指定的備份編號；空的表示最新一份
	target  string // files 要看的路徑；空的表示根目錄

	distro      string
	dest        string
	keep        int
	keepWeekly  int
	keepMonthly int
	at          string
	webhook     string
	notify      string
	verify      string
	exclude     []string
	unexclude   []string
	name        string
	to          string
	into        string
	paths       []string
	find        string
	home        string // 測試用：把設定、紀錄與驗證用的暫存都放到這個資料夾

	dryRun    bool
	noVerify  bool
	scheduled bool // 由排程工作啟動：還沒到期就直接結束，也不詢問任何事
	yes       bool
	all       bool
	enable    bool
	disable   bool
	help      bool
	version   bool
	debug     bool

	// given 記下哪些旗標出現過（完整名稱）。有些值的零值本身有意義（例如 --keep-weekly 0），
	// 不能靠值是不是零來判斷使用者有沒有指定。
	given map[string]bool
}

// has 回報使用者有沒有給這個旗標。
func (o *options) has(flag string) bool { return o.given[flag] }

// commandFlags 是每個指令各自接受的旗標；全域旗標（globalFlags）不列在這裡。
var commandFlags = map[string][]string{
	"init": {"--distro", "--all", "--dest", "--keep", "--keep-weekly", "--keep-monthly", "--at", "--webhook", "--dry-run", "--yes"},
	"config": {"--distro", "--keep", "--keep-weekly", "--keep-monthly", "--at", "--webhook", "--notify", "--verify",
		"--exclude", "--unexclude", "--enable", "--disable", "--dry-run"},
	"run":       {"--distro", "--no-verify", "--dry-run", "--scheduled"},
	"list":      {"--distro"},
	"files":     {"--distro", "--find"},
	"status":    {},
	"doctor":    {"--distro"},
	"verify":    {"--distro"},
	"restore":   {"--distro", "--name", "--to", "--path", "--into", "--dry-run", "--yes"},
	"uninstall": {"--dry-run", "--yes"},
}

// takesID 是後面可以再接一個備份編號的指令。
var takesID = map[string]bool{"verify": true, "restore": true}

// conflicts 是不能同時出現的旗標。
var conflicts = [][2]string{{"--enable", "--disable"}, {"--all", "--distro"},
	{"--path", "--name"}, {"--path", "--to"}, {"--into", "--to"}}

// needs 是「有前者就一定要有後者」的旗標。
var needs = [][2]string{{"--path", "--into"}, {"--into", "--path"}}

var globalFlags = map[string]bool{"--lang": true, "--home": true, "--debug": true, "--help": true, "--version": true}

// valueFlags 後面要接一個值，boolFlags 不用。
var (
	valueFlags = map[string]bool{
		"--distro": true, "--dest": true, "--keep": true, "--keep-weekly": true, "--keep-monthly": true,
		"--at": true, "--webhook": true, "--notify": true, "--verify": true, "--exclude": true, "--unexclude": true,
		"--name": true, "--to": true, "--path": true, "--into": true, "--find": true, "--home": true, "--lang": true,
	}
	boolFlags = map[string]bool{
		"--dry-run": true, "--no-verify": true, "--scheduled": true, "--yes": true, "--all": true,
		"--enable": true, "--disable": true, "--help": true, "--version": true, "--debug": true,
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

// normalizePattern 把排除樣式整理成 tar 看得懂的樣子：相對於 / 而且以 ./ 開頭。
// 使用者寫 /home/*/Downloads/* 或 ./home/*/Downloads/* 都可以。
func normalizePattern(s string) (string, bool) {
	if strings.ContainsAny(s, "\x00\n\r") {
		return "", false
	}
	switch {
	case strings.HasPrefix(s, "./"):
	case strings.HasPrefix(s, "/"):
		s = "." + s
	default:
		return "", false
	}
	if len(s) <= 2 {
		// 「/」或「./」會把整個 distro 都排除掉。
		return "", false
	}
	return s, true
}

func (o *options) setValue(name, value string) error {
	count := func(min int, bad string) (int, error) {
		n, err := strconv.Atoi(value)
		if err != nil || n < min {
			return 0, fmt.Errorf(bad, value)
		}
		return n, nil
	}
	var err error
	switch name {
	case "--distro":
		o.distro = value
	case "--dest":
		o.dest = value
	case "--keep":
		o.keep, err = count(1, T.BadKeep)
	case "--keep-weekly":
		o.keepWeekly, err = count(0, T.BadCount)
	case "--keep-monthly":
		o.keepMonthly, err = count(0, T.BadCount)
	case "--at":
		clock, ok := normalizeClock(value)
		if !ok {
			return fmt.Errorf(T.BadTime, value)
		}
		o.at = clock
	case "--webhook":
		o.webhook = value
	case "--notify":
		if value != notifyOnFailure && value != notifyAlways {
			return fmt.Errorf(T.BadNotify, value)
		}
		o.notify = value
	case "--verify":
		if value != verifyRestore && value != verifyNone {
			return fmt.Errorf(T.BadVerify, value)
		}
		o.verify = value
	case "--exclude", "--unexclude":
		pattern, ok := normalizePattern(value)
		if !ok {
			return fmt.Errorf(T.BadPattern, value)
		}
		if name == "--exclude" {
			o.exclude = append(o.exclude, pattern)
		} else {
			o.unexclude = append(o.unexclude, pattern)
		}
	case "--name":
		o.name = value
	case "--to":
		o.to = value
	case "--path":
		o.paths = append(o.paths, value)
	case "--into":
		o.into = value
	case "--find":
		o.find = value
	case "--home":
		o.home = value
	case "--lang":
		// 值在這裡只檢查對不對，實際套用由 pickLanguage 在更早的時候完成。
		if _, ok := parseLanguage(value); !ok {
			return fmt.Errorf(T.BadLang, value)
		}
	}
	return err
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
	case "--all":
		o.all = true
	case "--enable":
		o.enable = true
	case "--disable":
		o.disable = true
	case "--help":
		o.help = true
	case "--version":
		o.version = true
	case "--debug":
		o.debug = true
	}
}

// parseArgs 解析參數；旗標可以放在指令前面或後面，值可以寫成 --flag 值 或 --flag=值。
// --exclude 這類旗標可以重複出現。
func parseArgs(args []string) (options, error) {
	o := options{given: map[string]bool{}}
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
		o.given[name] = true
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
	for _, pair := range conflicts {
		if o.given[pair[0]] && o.given[pair[1]] {
			return o, fmt.Errorf(T.FlagConflict, pair[0], pair[1])
		}
	}
	for _, pair := range needs {
		if o.given[pair[0]] && !o.given[pair[1]] {
			return o, fmt.Errorf(T.FlagNeeds, pair[0], pair[1])
		}
	}
	rest := positional[1:]
	switch {
	case o.command == "files":
		// files [編號] [路徑]：第一個長得像編號就是編號，否則是路徑。
		if len(rest) > 0 && backupIDRe.MatchString(rest[0]) {
			o.id, rest = rest[0], rest[1:]
		}
		if len(rest) > 0 {
			o.target, rest = rest[0], rest[1:]
		}
	case len(rest) > 0 && takesID[o.command]:
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
		case "init", "config", "run", "verify", "restore", "uninstall":
			openRunLog()
		}
		if opts.command != "uninstall" {
			ensureInstalledFresh()
		}
	}
	switch opts.command {
	case "init":
		return cmdInit(opts)
	case "config":
		return cmdConfig(opts)
	case "run":
		return cmdRun(opts)
	case "list":
		return cmdList(opts)
	case "files":
		return cmdFiles(opts)
	case "status":
		return cmdStatus(opts)
	case "doctor":
		return cmdDoctor(opts)
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
