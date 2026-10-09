package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// config 指令：不帶旗標時顯示目前的設定，帶旗標時修改，不用自己去編輯 JSON。

// settingFlags 是會修改設定的旗標；perDistro 的那些需要知道是哪個 distro。
var (
	settingFlags = []string{"--keep", "--keep-weekly", "--keep-monthly", "--at", "--webhook", "--notify", "--verify",
		"--exclude", "--unexclude", "--enable", "--disable"}
	perDistroFlags = []string{"--keep", "--keep-weekly", "--keep-monthly", "--exclude", "--unexclude", "--enable", "--disable"}
)

func anyGiven(o options, flags []string) bool {
	for _, f := range flags {
		if o.has(f) {
			return true
		}
	}
	return false
}

// settingChange 是一項實際發生的修改。Flag 是對應的旗標，兩種語言通用。
type settingChange struct{ Flag, Old, New string }

var errBuiltinExclude = errors.New("built-in exclude")

// applySettings 把 config 指令給的值套到 cfg 上（只改記憶體裡的內容），回傳實際有變的項目。
// distro 是設定裡的名稱；只改全域設定時可以是空字串。
func applySettings(cfg *config, distro string, o options) ([]settingChange, error) {
	var changes []settingChange
	note := func(flag, old, new string) { changes = append(changes, settingChange{flag, old, new}) }

	if o.has("--at") && cfg.At != o.at {
		note("--at", cfg.At, o.at)
		cfg.At = o.at
	}
	if o.has("--webhook") {
		want := o.webhook
		if strings.EqualFold(want, "off") {
			want = ""
		} else if !validWebhook(want) {
			return nil, fmt.Errorf(T.BadWebhook, want)
		}
		if cfg.Notify.Webhook != want {
			// 網址本身就是密碼：只顯示主機名稱。
			show := func(u string) string {
				if u == "" {
					return "off"
				}
				return webhookHost(u)
			}
			note("--webhook", show(cfg.Notify.Webhook), show(want))
			cfg.Notify.Webhook = want
		}
	}
	if o.has("--notify") && cfg.Notify.On != o.notify {
		note("--notify", cfg.Notify.On, o.notify)
		cfg.Notify.On = o.notify
	}
	if o.has("--verify") && cfg.Verify != o.verify {
		note("--verify", cfg.Verify, o.verify)
		cfg.Verify = o.verify
	}
	if !anyGiven(o, perDistroFlags) {
		return changes, nil
	}

	dc := cfg.Distros[distro]
	if dc == nil {
		return nil, fmt.Errorf(T.DistroNotConfigured, distro)
	}
	number := func(flag string, field *int, value int) {
		if o.has(flag) && *field != value {
			note(flag, strconv.Itoa(*field), strconv.Itoa(value))
			*field = value
		}
	}
	number("--keep", &dc.Keep, o.keep)
	number("--keep-weekly", &dc.KeepWeekly, o.keepWeekly)
	number("--keep-monthly", &dc.KeepMonthly, o.keepMonthly)

	for _, pattern := range o.exclude {
		if !slices.Contains(dc.excludes(), pattern) {
			dc.Exclude = append(dc.Exclude, pattern)
			note("--exclude", "", pattern)
		}
	}
	for _, pattern := range o.unexclude {
		switch {
		case slices.Contains(alwaysExclude, pattern):
			return nil, fmt.Errorf("%w: %s", errBuiltinExclude, pattern)
		case slices.Contains(dc.Exclude, pattern):
			dc.Exclude = slices.DeleteFunc(dc.Exclude, func(p string) bool { return p == pattern })
			note("--unexclude", pattern, "")
		case dc.DefaultExcludes && slices.Contains(defaultExcludes, pattern):
			// 要拿掉的是預設排除裡的一項：改成「不用預設」，把其餘的預設項目明確列出來。
			var rest []string
			for _, p := range defaultExcludes {
				if p != pattern && !slices.Contains(alwaysExclude, p) {
					rest = append(rest, p)
				}
			}
			dc.DefaultExcludes = false
			dc.Exclude = append(rest, dc.Exclude...)
			note("--unexclude", pattern, "")
		}
	}
	if o.enable && !dc.Enabled {
		dc.Enabled = true
		note("--enable", "disabled", "enabled")
	}
	if o.disable && dc.Enabled {
		dc.Enabled = false
		note("--disable", "enabled", "disabled")
	}
	return changes, nil
}

// keepText 把保留規則寫成一句話。
func keepText(dc *distroConfig) string {
	text := fmt.Sprintf(T.InitKeepLine, dc.Keep)
	if dc.KeepWeekly > 0 {
		text += fmt.Sprintf(T.KeepWeeklyMore, dc.KeepWeekly)
	}
	if dc.KeepMonthly > 0 {
		text += fmt.Sprintf(T.KeepMonthlyMore, dc.KeepMonthly)
	}
	return text
}

func notifyText(cfg *config) string {
	channel := T.InitNotifyToast
	if cfg.Notify.Webhook != "" {
		channel = fmt.Sprintf(T.InitNotifyWebhook, webhookHost(cfg.Notify.Webhook))
	}
	if cfg.Notify.On == notifyAlways {
		return fmt.Sprintf(T.ConfigNotifyAlways, channel)
	}
	return fmt.Sprintf(T.ConfigNotifyFailure, channel)
}

func printSettings(cfg *config) {
	fmt.Printf(T.StatusSchedule+"\n", cfg.At)
	if cfg.Verify == verifyRestore {
		fmt.Println(T.ConfigVerifyOn)
	} else {
		fmt.Println(T.ConfigVerifyOff)
	}
	fmt.Println(notifyText(cfg))
	names := make([]string, 0, len(cfg.Distros))
	for name := range cfg.Distros {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		dc := cfg.Distros[name]
		fmt.Printf("\n%s  %s\n", bold(name), dim(filepath.Join(dc.Dest, name)))
		if !dc.Enabled {
			fmt.Println("  " + yellow(T.StatusDisabled))
		}
		fmt.Printf("  "+T.ConfigKeep+"\n", keepText(dc))
		fmt.Printf("  "+T.ConfigExcludes+"\n", strings.Join(dc.excludes(), T.ListSep))
	}
}

func cmdConfig(opts options) int {
	if !anyGiven(opts, settingFlags) {
		cfg, err := loadConfig()
		if err != nil {
			return fail(err)
		}
		if cfg == nil || len(cfg.Distros) == 0 {
			return fail(errors.New(T.NotSetUp))
		}
		printSettings(cfg)
		return 0
	}

	release, exit, ok := takeLock(opts)
	if !ok {
		return exit
	}
	defer release()
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	if cfg == nil || len(cfg.Distros) == 0 {
		return fail(errors.New(T.NotSetUp))
	}
	distro := ""
	if anyGiven(opts, perDistroFlags) {
		// 改的是某個 distro 自己的設定：要知道是哪一個。
		names, err := configuredNames(cfg, opts.distro)
		if err != nil {
			return fail(err)
		}
		distro = names
	}
	oldAt := cfg.At
	changes, err := applySettings(cfg, distro, opts)
	if errors.Is(err, errBuiltinExclude) {
		return fail(fmt.Errorf(T.ExcludeBuiltin, strings.TrimPrefix(err.Error(), errBuiltinExclude.Error()+": ")))
	}
	if err != nil {
		return fail(err)
	}
	if len(changes) == 0 {
		fmt.Println(T.ConfigNoChange)
		return 0
	}
	for _, c := range changes {
		show := func(v string) string {
			if v == "" {
				return T.ConfigNone
			}
			return v
		}
		fmt.Printf("  "+T.ConfigChanged+"\n", c.Flag, show(c.Old), show(c.New))
	}
	if opts.dryRun {
		fmt.Println(dim(T.DryRunNothingDone))
		return 0
	}
	if err := saveConfig(cfg); err != nil {
		return fail(err)
	}
	logf("config: %d change(s)", len(changes))
	fmt.Println(green(T.ConfigSaved))

	if cfg.At != oldAt {
		// 排程的時間寫在工作排程器裡，要跟著重建。
		if problem := installedProblem(); problem != "" {
			fmt.Println(yellow(problem))
			return 1
		}
		if err := registerTask(cfg.At); err != nil {
			return fail(fmt.Errorf(T.TaskFailed, err))
		}
		fmt.Printf(T.ConfigTaskUpdated+"\n", cfg.At)
	}
	return 0
}

// configuredNames 找出 config 要改的那個 distro：指定了 -d 就用它，否則設定裡必須只有一個。
func configuredNames(cfg *config, distro string) (string, error) {
	if distro != "" {
		for name := range cfg.Distros {
			if strings.EqualFold(name, distro) {
				return name, nil
			}
		}
		return "", fmt.Errorf(T.DistroNotConfigured, distro)
	}
	if len(cfg.Distros) == 1 {
		for name := range cfg.Distros {
			return name, nil
		}
	}
	names := make([]string, 0, len(cfg.Distros))
	for name := range cfg.Distros {
		names = append(names, name)
	}
	sort.Strings(names)
	return "", fmt.Errorf(T.WhichDistro, strings.Join(names, T.ListSep))
}
