package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// dueAfter：排程啟動時，上次成功的備份還沒超過這麼久就不再備份。
// 工作有「每天定時」與「登入後」兩個觸發，同一天可能被叫起來不只一次。
const dueAfter = 20 * time.Hour

// staleAfter：上次成功的備份超過這麼久，就算是過期了（排程是每天一次）。
const staleAfter = 48 * time.Hour

// backupErrorText 把備份失敗的種類換成給使用者看的說明。
func backupErrorText(err error) string {
	var be *backupError
	if !errors.As(err, &be) {
		return err.Error()
	}
	switch be.Kind {
	case failNotGNUTar:
		return T.BackupNotGNUTar
	case failNotTar, failStart:
		return fmt.Sprintf(T.BackupNoStream, be.Detail)
	case failWrite:
		return fmt.Sprintf(T.BackupWriteFailed, be.Detail)
	case failStalled:
		return T.BackupStalled
	case failTruncated:
		return T.BackupTruncated
	default:
		return fmt.Sprintf(T.BackupTarFailed, be.Detail)
	}
}

// reasonText 把試還原沒過（或沒做）的原因代碼換成給使用者看的說明。
func reasonText(reason string) string {
	switch reason {
	case reasonNoIndex:
		return T.ReasonNoIndex
	case reasonEtc:
		return T.ReasonEtc
	case reasonNoSpace:
		return T.ReasonNoSpace
	case reasonChanged:
		return T.ReasonChanged
	case reasonUnread:
		return T.ReasonUnread
	case reasonImport:
		return T.ReasonImport
	case reasonOverride, reasonNotInert:
		return T.ReasonNotInert
	case reasonUser:
		return T.ReasonUser
	case reasonSamples:
		return T.ReasonSamples
	}
	return T.ReasonCheck
}

func verifyLabel(m *manifest) string {
	switch {
	case m.Verify == nil:
		return T.LabelNotVerified
	case m.Verify.OK:
		return T.LabelVerified
	case m.Verify.Mode == verifyModeSkipped:
		return T.LabelNotVerified
	}
	return T.LabelVerifyFailed
}

// configuredTargets 回傳這次要處理的 distro 名稱：指定了 -d 就只有那一個，否則是所有啟用的。
func configuredTargets(cfg *config, distro string) ([]string, error) {
	if distro != "" {
		for name := range cfg.Distros {
			if strings.EqualFold(name, distro) {
				return []string{name}, nil
			}
		}
		return nil, fmt.Errorf(T.DistroNotConfigured, distro)
	}
	var names []string
	for name, d := range cfg.Distros {
		if d.Enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, errors.New(T.NothingConfigured)
	}
	return names, nil
}

func cmdRun(opts options) int {
	release, exit, ok := takeLock(opts)
	if !ok {
		return exit
	}
	defer release()

	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	if cfg == nil {
		return fail(errors.New(T.NotSetUp))
	}
	targets, err := configuredTargets(cfg, opts.distro)
	if err != nil {
		return fail(err)
	}
	distros, err := readLxss()
	if err != nil {
		return fail(err)
	}
	wsl := wslVersion()
	state := loadState()
	logf("run: version=%s wsl=%s scheduled=%v targets=%v", version, wsl, opts.scheduled, targets)
	if !opts.dryRun {
		defer keepAwake()()
	}

	worst := 0
	for _, name := range targets {
		worst = max(worst, runOne(opts, cfg, name, distros, wsl, state))
	}
	return worst
}

// runOne 備份一個 distro：備份、試還原、清掉過舊的、更新還原包，並記下結果、必要時通知。
func runOne(opts options, cfg *config, name string, distros []regDistro, wsl string, state *runState) int {
	dc := cfg.Distros[name]
	st := state.of(name)
	if opts.scheduled && !st.LastSuccess.IsZero() && time.Since(st.LastSuccess) < dueAfter {
		logf("%s: last success %s ago, not due", name, time.Since(st.LastSuccess).Round(time.Minute))
		return 0
	}
	dir := filepath.Join(dc.Dest, name)

	// 設定不對就不動手，並把原因告訴使用者（排程執行時是通知）。
	giveUp := func(message string) int {
		logf("%s: %s", name, message)
		fmt.Fprintln(os.Stderr, red(fmt.Sprintf(T.ErrorLine, message)))
		if !opts.dryRun {
			st.LastAttempt, st.LastResult, st.LastMessage = time.Now(), resultFailed, message
			saveState(state)
			notify(cfg, fmt.Sprintf(T.NotifyFailedTitle, name), message)
		}
		return 2
	}
	d := findDistro(distros, name)
	if d == nil {
		return giveUp(fmt.Sprintf(T.DistroGone, name))
	}
	if dc.ID != "" && !strings.EqualFold(d.GUID, dc.ID) {
		return giveUp(fmt.Sprintf(T.DistroReplaced, name))
	}
	if reason, ok := eligible(*d); !ok {
		return giveUp(fmt.Sprintf(T.DistroNotEligible, name, reason))
	}
	doVerify := cfg.Verify == verifyRestore && !opts.noVerify

	if opts.dryRun {
		fmt.Printf(T.RunBackingUp+"\n", bold(name), dir)
		if doVerify {
			fmt.Println("  " + T.DryRunWouldVerify)
		}
		// 這次備份如果通過試還原，會擠掉哪些舊的。
		planned := append(listBackups(dir), &manifest{ID: time.Now().UTC().Format(idLayout), Verify: &verifyResult{OK: doVerify}})
		for _, m := range planRetention(planned, dc.retention(), cfg.Verify) {
			fmt.Printf("  "+T.DryRunWouldPrune+"\n", m.ID)
		}
		fmt.Println(dim(T.DryRunNothingDone))
		return 0
	}

	st.LastAttempt = time.Now()
	saveState(state)
	fmt.Printf(T.RunBackingUp+"\n", bold(name), dir)
	bar := &progressLine{label: T.ProgressReading}
	m, err := runBackup(backupRequest{
		Distro: *d, Dir: dir, Excludes: dc.excludes(), WSL: wsl, Scheduled: opts.scheduled, OnProgress: bar.update,
	})
	bar.clear()
	if err != nil {
		logf("%s: backup failed: %v", name, err)
		return giveUp(backupErrorText(err))
	}
	fmt.Printf("  "+T.RunWritten+"\n", m.Archive, humanBytes(m.Size), humanDuration(time.Duration(m.Seconds*float64(time.Second))))
	if m.WarningCount > 0 {
		fmt.Println("  " + dim(fmt.Sprintf(T.RunWarnings, m.WarningCount)))
	}
	for _, mount := range m.SkippedMounts {
		fmt.Println("  " + yellow(fmt.Sprintf(T.RunSkippedMount, mount)))
	}
	for _, flag := range m.Dropped {
		fmt.Println("  " + yellow(fmt.Sprintf(T.RunDropped, flag)))
	}
	if m.Index != nil && m.Index.ACLs > 0 {
		fmt.Println("  " + dim(fmt.Sprintf(T.RunACLs, m.Index.ACLs)))
	}

	code, result, message := 0, resultOK, ""
	switch {
	case !doVerify && cfg.Verify == verifyRestore:
		// 使用者這一次自己跳過了試還原：備份留著，但不算一次完整的成功。
		result = resultUnverified
		fmt.Println("  " + dim(T.RunVerifySkipped))
	case doVerify:
		fmt.Println(T.RunVerifying)
		bar := &progressLine{label: T.ProgressImporting}
		res := runVerify(m, bar.update)
		bar.clear()
		switch {
		case res.OK:
			fmt.Println("  " + green(fmt.Sprintf(T.RunVerified, res.Samples, humanDuration(time.Duration(res.Seconds*float64(time.Second))))))
		case res.Mode == verifyModeSkipped:
			code, result, message = 1, resultUnverified, fmt.Sprintf(T.RunNotVerified, reasonText(res.Reason))
			fmt.Println("  " + yellow(message))
		default:
			code, result, message = 2, resultFailed, fmt.Sprintf(T.RunVerifyFailed, reasonText(res.Reason))
			fmt.Println("  " + red(message))
		}
	}

	if newerManifests(dir) {
		// 這個資料夾裡有較新版本的 wslbak 寫的備份：不確定哪些還被需要，一份都不刪。
		logf("%s: manifests from a newer wslbak are present; not pruning", name)
		fmt.Println("  " + yellow(T.RunPruneSkipped))
	} else if removed := applyPrune(planRetention(listBackups(dir), dc.retention(), cfg.Verify)); removed > 0 {
		fmt.Printf("  "+T.RunPruned+"\n", removed)
	}
	if err := refreshRestoreKit(dc.Dest); err != nil {
		logf("restore kit in %s: %v", dc.Dest, err)
	}

	st.LastResult, st.LastMessage, st.LastID = result, message, m.ID
	if result == resultOK {
		st.LastSuccess = time.Now()
	}
	saveState(state)
	switch {
	case result == resultFailed:
		notify(cfg, fmt.Sprintf(T.NotifyFailedTitle, name), message)
	case result == resultUnverified && message != "":
		notify(cfg, fmt.Sprintf(T.NotifyUnverifiedTitle, name), message)
	case result == resultOK && cfg.Notify.On == notifyAlways:
		notify(cfg, fmt.Sprintf(T.NotifyOKTitle, name), fmt.Sprintf(T.NotifyOKBody, m.ID, humanBytes(m.Size)))
	}
	if code == 0 {
		fmt.Println(green(T.RunDone))
	}
	return code
}

func cmdVerify(opts options) int {
	release, exit, ok := takeLock(opts)
	if !ok {
		return exit
	}
	defer release()
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	source, code := pickSource(cfg, opts.distro)
	if source == nil {
		return code
	}
	var m *manifest
	if opts.id != "" {
		if m = readManifest(source.Dir, opts.id); m == nil {
			return fail(fmt.Errorf(T.NoSuchBackup, opts.id))
		}
	} else if m = newest(listBackups(source.Dir), false); m == nil {
		return fail(fmt.Errorf(T.NoBackups, source.Dir))
	}
	fmt.Printf(T.VerifyStart+"\n", bold(m.ID), source.Distro)
	bar := &progressLine{label: T.ProgressImporting}
	res := runVerify(m, bar.update)
	bar.clear()
	switch {
	case res.OK:
		fmt.Println("  " + green(fmt.Sprintf(T.RunVerified, res.Samples, humanDuration(time.Duration(res.Seconds*float64(time.Second))))))
		return 0
	case res.Mode == verifyModeSkipped:
		fmt.Println("  " + yellow(fmt.Sprintf(T.RunNotVerified, reasonText(res.Reason))))
		return 1
	}
	fmt.Println("  " + red(fmt.Sprintf(T.RunVerifyFailed, reasonText(res.Reason))))
	return 2
}

func cmdList(opts options) int {
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	sources := backupSources(cfg)
	if len(sources) == 0 {
		return fail(errors.New(T.NotSetUp))
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Distro < sources[j].Distro })
	shown := false
	for _, s := range sources {
		if opts.distro != "" && !strings.EqualFold(s.Distro, opts.distro) {
			continue
		}
		if shown {
			fmt.Println()
		}
		shown = true
		fmt.Printf("%s  %s\n", bold(s.Distro), dim(s.Dir))
		backups := listBackups(s.Dir)
		if len(backups) == 0 {
			fmt.Println("  " + fmt.Sprintf(T.NoBackups, s.Dir))
			continue
		}
		rows := [][]string{T.ListHeaders[:]}
		for i := len(backups) - 1; i >= 0; i-- {
			m := backups[i]
			rows = append(rows, []string{m.ID, m.Created.Local().Format("2006-01-02 15:04"), humanBytes(m.Size), verifyLabel(m)})
		}
		printTable(rows)
	}
	if !shown {
		return fail(fmt.Errorf(T.DistroNotConfigured, opts.distro))
	}
	return 0
}

func cmdStatus(opts options) int {
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	distros, err := readLxss()
	if err != nil {
		return fail(err)
	}
	if cfg == nil || len(cfg.Distros) == 0 {
		fmt.Println(T.NotSetUp)
		// 還沒設定時，列出這台電腦上有哪些 distro 可以備份、哪些不行以及原因。
		for _, d := range distros {
			if reason, ok := eligible(d); ok {
				fmt.Printf("  %s  %s\n", bold(d.Name), green(T.CanBackUp))
			} else {
				fmt.Printf("  %s  %s\n", d.Name, dim(reason))
			}
		}
		return 1
	}

	code := 0
	sid, _ := currentSID()
	if taskExists(sid) {
		fmt.Printf(T.StatusSchedule+"\n", cfg.At)
	} else {
		fmt.Println(yellow(T.StatusNoTask))
		code = 1
	}
	if problem := installedProblem(); problem != "" {
		fmt.Println(yellow(problem))
		code = 1
	}
	state := loadState()
	now := time.Now()
	names := make([]string, 0, len(cfg.Distros))
	for name := range cfg.Distros {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		dc, st := cfg.Distros[name], state.of(name)
		fmt.Printf("\n%s  %s\n", bold(name), dim(filepath.Join(dc.Dest, name)))
		if !dc.Enabled {
			fmt.Println("  " + dim(T.StatusDisabled))
			continue
		}
		if findDistro(distros, name) == nil {
			fmt.Println("  " + yellow(fmt.Sprintf(T.DistroGone, name)))
			code = 1
		}
		backups := listBackups(filepath.Join(dc.Dest, name))
		verified := 0
		for _, m := range backups {
			if m.verified() {
				verified++
			}
		}
		fmt.Printf("  "+T.StatusCounts+"\n", len(backups), verified, dc.Keep)
		switch {
		case st.LastSuccess.IsZero():
			fmt.Println("  " + yellow(T.StatusNeverSucceeded))
			code = 1
		case now.Sub(st.LastSuccess) > staleAfter:
			fmt.Println("  " + yellow(fmt.Sprintf(T.StatusStale, humanSince(st.LastSuccess, now))))
			code = 1
		default:
			fmt.Println("  " + green(fmt.Sprintf(T.StatusLastSuccess, humanSince(st.LastSuccess, now))))
		}
		if st.LastResult != "" && st.LastResult != resultOK && st.LastMessage != "" {
			fmt.Println("  " + yellow(fmt.Sprintf(T.StatusLastProblem, humanSince(st.LastAttempt, now), st.LastMessage)))
		}
	}
	return code
}

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

var procSetThreadExecutionState = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")

// keepAwake 請 Windows 在備份期間不要因為閒置而進入睡眠；回傳的函式把設定還原。
// 這個設定跟著執行緒走，所以把目前的 goroutine 固定在它的執行緒上。
// 使用者自己闔上筆電蓋或按下睡眠仍然會睡，那時備份會中斷並在下次補跑。
func keepAwake() (restore func()) {
	runtime.LockOSThread()
	procSetThreadExecutionState.Call(esContinuous | esSystemRequired)
	return func() {
		procSetThreadExecutionState.Call(esContinuous)
		runtime.UnlockOSThread()
	}
}
