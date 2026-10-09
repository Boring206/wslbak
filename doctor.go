package main

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// doctor 指令：把「備份跑不起來」常見的原因逐項檢查一遍，每一項都說明狀況與修法。
// 判斷的部分寫成不碰系統的函式，方便測試；讀登錄檔、查磁碟、進 distro 的部分只負責把資料拿來。

type checkLevel int

const (
	levelOK checkLevel = iota
	levelNote
	levelWarn
	levelFail
)

type checkResult struct {
	Level checkLevel
	Text  string
	Fix   string // 怎麼處理；沒有要處理的就留空
}

// 快取要大到值得一提才列出來。
const cacheWorthMentioning = 200 << 20

// Smart App Control 的狀態（登錄檔 VerifiedAndReputablePolicyState）。
const (
	sacOff        = 0
	sacEnforce    = 1
	sacEvaluation = 2
)

// judgeSAC 判斷「智慧型應用程式控制」的狀態。開著的時候沒有簽章的程式一律被擋，
// 排程的備份會直接啟動失敗，而且連通知都發不出來。
func judgeSAC(state uint64, found bool) checkResult {
	switch {
	case found && state == sacEnforce:
		return checkResult{levelFail, T.DocSACOn, T.DocSACFix}
	case found && state == sacEvaluation:
		return checkResult{levelWarn, T.DocSACEval, ""}
	}
	return checkResult{levelOK, T.DocSACOff, ""}
}

// judgeCFA 判斷「受控資料夾存取」：1 是開啟，2 是只稽核。
func judgeCFA(state uint64, found bool) checkResult {
	if found && state == 1 {
		return checkResult{levelWarn, T.DocCFAOn, T.DocCFAFix}
	}
	return checkResult{levelOK, T.DocCFAOff, ""}
}

// antivirusScript 列出 Windows 資訊安全中心登記的防毒軟體，一行一個：「狀態|名稱」。
const antivirusScript = `
$ErrorActionPreference = 'SilentlyContinue'
$ProgressPreference = 'SilentlyContinue'
Get-CimInstance -Namespace root/SecurityCenter2 -ClassName AntiVirusProduct | ForEach-Object { "$($_.productState)|$($_.displayName)" }
`

// parseAntivirus 從 antivirusScript 的輸出挑出「啟用中」的防毒軟體名稱。
// productState 的第 12–15 位元是即時防護的狀態：1 是開啟，2 是暫時關閉（過一陣子會自己恢復），
// 兩種都算；0 是關閉。
func parseAntivirus(text string) (active []string) {
	for _, line := range strings.Split(text, "\n") {
		state, name, ok := strings.Cut(strings.TrimSpace(line), "|")
		n, err := strconv.ParseUint(state, 10, 32)
		if !ok || err != nil || name == "" {
			continue
		}
		if state := n >> 12 & 0xF; (state == 1 || state == 2) && !slices.Contains(active, name) {
			active = append(active, name)
		}
	}
	return active
}

// judgeAntivirus：啟用中的若不是 Windows 內建的防毒，提醒使用者新版程式第一次執行時可能被它扣住。
func judgeAntivirus(active []string, programDir string) (checkResult, bool) {
	var others []string
	for _, name := range active {
		if !strings.Contains(strings.ToLower(name), "defender") {
			others = append(others, name)
		}
	}
	if len(others) == 0 {
		return checkResult{}, false
	}
	return checkResult{levelNote, fmt.Sprintf(T.DocAntivirus, strings.Join(others, T.ListSep)), fmt.Sprintf(T.DocAntivirusFix, programDir)}, true
}

// judgeSpace 判斷可用空間夠不夠；need 是 0 表示還不知道需要多少。
func judgeSpace(free, need uint64) checkLevel {
	if need > 0 && free < need {
		return levelWarn
	}
	return levelOK
}

type cacheDir struct {
	Bytes   int64
	Pattern string
	Path    string
}

// parseCaches 解析 caches.sh 的輸出。docker 是 /var/lib/docker 的大小，沒有的話是 0。
func parseCaches(text string) (caches []cacheDir, docker int64, done bool) {
	for _, row := range parseProto(text) {
		switch {
		case row[0] == "cache" && len(row) >= 4:
			n, err := strconv.ParseInt(row[1], 10, 64)
			if err == nil {
				caches = append(caches, cacheDir{n, row[2], row[3]})
			}
		case row[0] == "docker" && len(row) >= 2:
			docker, _ = strconv.ParseInt(row[1], 10, 64)
		case row[0] == "done":
			done = true
		}
	}
	sort.Slice(caches, func(i, j int) bool { return caches[i].Bytes > caches[j].Bytes })
	return caches, docker, done
}

// judgeLastRun 依執行紀錄判斷某個 distro 的備份近況。
func judgeLastRun(name string, st *distroState, now time.Time) checkResult {
	switch {
	case st.LastResult == resultFailed && st.LastAttempt.After(st.LastSuccess):
		return checkResult{levelFail, fmt.Sprintf(T.DocLastProblem, name, st.LastMessage), fmt.Sprintf(T.DocSeeLog, logPath())}
	case st.LastSuccess.IsZero():
		return checkResult{levelWarn, fmt.Sprintf(T.DocLastNever, name), T.InitRunHint}
	case now.Sub(st.LastSuccess) > staleAfter:
		return checkResult{levelWarn, fmt.Sprintf(T.DocLastStale, name, humanSince(st.LastSuccess, now)), T.DocStaleFix}
	case st.LastResult == resultUnverified && st.LastMessage != "":
		return checkResult{levelWarn, fmt.Sprintf(T.DocLastProblem, name, st.LastMessage), ""}
	}
	return checkResult{levelOK, fmt.Sprintf(T.DocLastOK, name, humanSince(st.LastSuccess, now)), ""}
}

func logPath() string {
	state, err := stateDir()
	if err != nil {
		return "wslbak.log"
	}
	return filepath.Join(state, "wslbak.log")
}

func readPolicyDWORD(path, value string) (uint64, bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
	if err != nil {
		return 0, false
	}
	defer k.Close()
	n, _, err := k.GetIntegerValue(value)
	return n, err == nil
}

// compressedOrEncrypted 回報資料夾（或它最近的現存上層）是不是設了 NTFS 壓縮或加密。
func compressedOrEncrypted(dir string) bool {
	path, err := windows.UTF16PtrFromString(existingAncestor(dir))
	if err != nil {
		return false
	}
	attrs, err := windows.GetFileAttributes(path)
	return err == nil && attrs&(windows.FILE_ATTRIBUTE_COMPRESSED|windows.FILE_ATTRIBUTE_ENCRYPTED) != 0
}

type doctorReport struct {
	warnings, failures int
}

func (r *doctorReport) section(title string) {
	fmt.Println()
	fmt.Println(bold(title))
}

func (r *doctorReport) add(c checkResult) {
	label := green(pad(T.DoctorOK, labelWidth(T.DoctorLabels[:])))
	switch c.Level {
	case levelNote:
		label = dim(pad(T.DoctorLabels[0], labelWidth(T.DoctorLabels[:])))
	case levelWarn:
		r.warnings++
		label = yellow(pad(T.DoctorLabels[1], labelWidth(T.DoctorLabels[:])))
	case levelFail:
		r.failures++
		label = red(pad(T.DoctorLabels[2], labelWidth(T.DoctorLabels[:])))
	}
	fmt.Printf("  %s%s\n", label, c.Text)
	if c.Fix != "" {
		for _, line := range strings.Split(c.Fix, "\n") {
			fmt.Printf("  %s%s\n", pad("", labelWidth(T.DoctorLabels[:])), dim("→ "+line))
		}
	}
}

func cmdDoctor(opts options) int {
	var r doctorReport
	now := time.Now()

	// —— WSL 本身 ——
	r.section(T.DocSectionWSL)
	wsl := wslVersion()
	if wsl == "" {
		r.add(checkResult{levelFail, T.NeedStoreWSL, ""})
	} else {
		r.add(checkResult{levelOK, fmt.Sprintf(T.DocWSL, wsl), ""})
	}
	distros, err := readLxss()
	if err != nil {
		return fail(err)
	}
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	running := map[string]bool{}
	if list, err := listDistros(); err == nil {
		for _, d := range list {
			running[strings.ToLower(d.Name)] = d.Running
		}
	}
	configured := func(name string) *distroConfig {
		if cfg == nil {
			return nil
		}
		for n, dc := range cfg.Distros {
			if strings.EqualFold(n, name) {
				return dc
			}
		}
		return nil
	}

	// —— 每個 distro ——
	r.section(T.DocSectionDistros)
	var checked []regDistro // 進去看過、tar 沒問題的
	for _, d := range distros {
		if opts.distro != "" && !strings.EqualFold(d.Name, opts.distro) {
			continue
		}
		reason, ok := eligible(d)
		if !ok {
			// 不能備份的 distro 不是「問題」，只是讓使用者知道為什麼沒有列進來。
			r.add(checkResult{levelNote, fmt.Sprintf(T.DocDistroSkip, d.Name, reason), ""})
			continue
		}
		dc := configured(d.Name)
		// 沒有在執行、也沒設定備份的 distro 不去啟動它。
		if !running[strings.ToLower(d.Name)] && (dc == nil || !dc.Enabled) {
			r.add(checkResult{levelNote, fmt.Sprintf(T.DocDistroNotRunning, d.Name), ""})
			continue
		}
		info, err := probeDistro(d.Name)
		switch {
		case err != nil:
			r.add(checkResult{levelFail, fmt.Sprintf(T.ProbeFailed, d.Name, err), ""})
		case info.TarKind != "gnu":
			r.add(checkResult{levelFail, fmt.Sprintf(T.DocTarBad, d.Name), T.DocTarFix})
		case !info.HasSHA256:
			r.add(checkResult{levelWarn, fmt.Sprintf(T.DocNoSHA, d.Name), T.DocNoSHAFix})
			checked = append(checked, d)
		default:
			r.add(checkResult{levelOK, fmt.Sprintf(T.DocDistroOK, d.Name, info.OS, humanBytes(info.UsedBytes)), ""})
			checked = append(checked, d)
		}
	}

	// —— 這台電腦的設定 ——
	r.section(T.DocSectionWindows)
	r.add(judgeSAC(readPolicyDWORD(`SYSTEM\CurrentControlSet\Control\CI\Policy`, "VerifiedAndReputablePolicyState")))
	r.add(judgeCFA(readPolicyDWORD(`SOFTWARE\Microsoft\Windows Defender\Windows Defender Exploit Guard\Controlled Folder Access`, "EnableControlledFolderAccess")))
	{
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, _ := runSystem(ctx, antivirusScript, system32(`WindowsPowerShell\v1.0\powershell.exe`), "-NoProfile", "-NonInteractive", "-Command", "-")
		cancel()
		programDir, _ := installDir()
		if c, ok := judgeAntivirus(parseAntivirus(string(out)), programDir); ok {
			r.add(c)
		}
	}
	if root, err := verifyRoot(); err == nil && compressedOrEncrypted(root) {
		r.add(checkResult{levelWarn, fmt.Sprintf(T.DocVerifyAttr, existingAncestor(root)), T.DocVerifyAttrFix})
	}

	if cfg == nil || len(cfg.Distros) == 0 {
		r.section(T.DocSectionBackups)
		r.add(checkResult{levelWarn, T.NotSetUp, ""})
		return r.finish()
	}

	// —— 排程與程式 ——
	r.section(T.DocSectionSchedule)
	sid, _ := currentSID()
	if taskExists(sid) {
		r.add(checkResult{levelOK, fmt.Sprintf(T.StatusSchedule, cfg.At), ""})
	} else {
		r.add(checkResult{levelFail, T.StatusNoTask, ""})
	}
	if problem := installedProblem(); problem != "" {
		r.add(checkResult{levelFail, problem, ""})
	} else if dir, err := installDir(); err == nil {
		r.add(checkResult{levelOK, fmt.Sprintf(T.DocInstalled, dir), ""})
	}

	// —— 每個設定好的備份 ——
	r.section(T.DocSectionBackups)
	state := loadState()
	names := make([]string, 0, len(cfg.Distros))
	for name := range cfg.Distros {
		if opts.distro == "" || strings.EqualFold(name, opts.distro) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	verifyDir, _ := verifyRoot()
	for _, name := range names {
		dc := cfg.Distros[name]
		if !dc.Enabled {
			r.add(checkResult{levelNote, fmt.Sprintf("%s: %s", name, T.StatusDisabled), ""})
			continue
		}
		d := findDistro(distros, name)
		if d == nil {
			r.add(checkResult{levelFail, fmt.Sprintf(T.DistroGone, name), ""})
			continue
		}
		if dc.ID != "" && !strings.EqualFold(d.GUID, dc.ID) {
			r.add(checkResult{levelFail, fmt.Sprintf(T.DistroReplaced, name), ""})
			continue
		}
		dir := filepath.Join(dc.Dest, name)
		backups := listBackups(dir)
		// 下一份備份大概多大：用最新一份的大小估。
		var nextArchive, nextRestore uint64
		if m := newest(backups, false); m != nil {
			nextArchive = uint64(m.Size) + uint64(m.Size)/5
			if m.Index != nil {
				nextRestore = verifySpaceNeeded(m.Index.Size)
			}
		}
		vol, err := volumeOf(dir)
		switch {
		case err != nil:
			r.add(checkResult{levelFail, fmt.Sprintf(T.DocDestMissing, dir), T.DocDestMissingFix})
			continue
		case !fileExists(filepath.Join(dc.Dest, installedCLI)) && len(backups) == 0 && existingAncestor(dir) != dir:
			// 資料夾還沒建立（還沒備份過）：只確認磁碟在。
			r.add(checkResult{levelOK, fmt.Sprintf(T.DocDestOK, dir, vol.Root, humanBytes(int64(vol.Free))), ""})
		default:
			if existingAncestor(dir) == dir {
				if werr := writable(dir); werr != nil {
					r.add(checkResult{levelFail, fmt.Sprintf(T.DestNotWritable, dir, werr), ""})
					continue
				}
			}
			if judgeSpace(vol.Free, nextArchive) != levelOK {
				r.add(checkResult{levelWarn, fmt.Sprintf(T.DocDestLow, vol.Root, humanBytes(int64(vol.Free)), humanBytes(int64(nextArchive))), ""})
			} else {
				r.add(checkResult{levelOK, fmt.Sprintf(T.DocDestOK, dir, vol.Root, humanBytes(int64(vol.Free))), ""})
			}
		}
		if sameVolume(dir, d.BasePath) {
			r.add(checkResult{levelWarn, fmt.Sprintf(T.WarnSameVolume, vol.Root), ""})
		} else if a, okA := diskNumber(vol.Root); okA {
			if b, okB := diskNumber(volumeRoot(d.BasePath)); okB && a == b {
				r.add(checkResult{levelNote, fmt.Sprintf(T.WarnSameDisk, vol.Root, volumeRoot(d.BasePath)), ""})
			}
		}
		if cfg.Verify == verifyRestore {
			if v, err := volumeOf(verifyDir); err == nil && judgeSpace(v.Free, nextRestore) != levelOK {
				r.add(checkResult{levelWarn, fmt.Sprintf(T.WarnVerifySpace, v.Root, humanBytes(int64(v.Free))), ""})
			}
		}
		r.add(judgeLastRun(name, state.of(name), now))
	}

	// —— 可以省空間的地方 ——
	// 只看設定了備份、而且剛才進得去的 distro。
	r.section(T.DocSectionSpace)
	suggested := false
	for _, d := range checked {
		dc := configured(d.Name)
		if dc == nil || !dc.Enabled {
			continue
		}
		out, err := runInDistro(d.Name, cachesScript, 2*time.Minute)
		caches, docker, done := parseCaches(decodeWSLText(out))
		if !done {
			logf("doctor: cache scan in %s did not finish: %v", d.Name, err)
			continue
		}
		excluded := dc.excludes()
		for _, c := range caches {
			if c.Bytes < cacheWorthMentioning || slices.Contains(excluded, c.Pattern) {
				continue
			}
			suggested = true
			r.add(checkResult{levelNote, fmt.Sprintf(T.DocCache, d.Name, humanBytes(c.Bytes), c.Path),
				fmt.Sprintf(T.DocCacheFix, d.Name, strings.TrimPrefix(c.Pattern, "."))})
		}
		if docker >= cacheWorthMentioning && !slices.Contains(excluded, "./var/lib/docker/*") {
			suggested = true
			r.add(checkResult{levelNote, fmt.Sprintf(T.DocDocker, d.Name, humanBytes(docker)), ""})
		}
	}
	if !suggested {
		r.add(checkResult{levelOK, T.DocNoCaches, ""})
	}
	return r.finish()
}

// finish 印出總結並決定結束碼：有問題是 2，只有要注意的是 1，都沒有是 0。
func (r *doctorReport) finish() int {
	fmt.Println()
	switch {
	case r.failures > 0:
		fmt.Println(red(fmt.Sprintf(T.DocSummaryFail, r.failures)))
		fmt.Printf(T.DocSeeLog+"\n", logPath())
		return 2
	case r.warnings > 0:
		fmt.Println(yellow(fmt.Sprintf(T.DocSummaryWarn, r.warnings)))
		return 1
	}
	fmt.Println(green(T.DocSummaryOK))
	return 0
}
