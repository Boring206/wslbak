package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 這個檔案負責 init 與 uninstall，以及把程式放到一個固定的位置。
//
// 排程工作不能指向 npm 的資料夾（切換 Node 版本或更新套件就會變）、npx 的快取，
// 更不能指向 distro 裡面的路徑（那正是要被備份的東西）。所以 init 會把執行檔複製到
// %LOCALAPPDATA%\Programs\wslbak\，排程只認這一份；之後每次從 npm 那一份執行時再順手更新它。

const (
	installedCLI = "wslbak.exe"
	installedGUI = "wslbakw.exe"
	kitReadme    = "README-RESTORE.txt"
)

// copyFile 把 src 複製成 dst：先寫到旁邊的暫存檔再改名，dst 不會出現寫到一半的內容。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func installDir() (string, error) {
	if homeOverride != "" {
		return filepath.Join(homeOverride, "program"), nil
	}
	base, err := localAppData()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "Programs", "wslbak"), nil
}

// selfExes 回傳目前這支程式的兩個版本各在哪裡：有主控台的（給人用）與沒有視窗的（給排程用）。
// 兩個放在同一個資料夾，檔名只差一個 w：wslbak-x64.exe／wslbakw-x64.exe，或 wslbak.exe／wslbakw.exe。
func selfExes() (cli, gui string, err error) {
	self, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	return selfExesAt(self, filepath.EvalSymlinks)
}

// selfExesAt 是 selfExes 的本體。resolve 把符號連結換成它真正指向的檔案：
// 有些安裝方式（例如 winget）是在 PATH 上的資料夾放一個連結，另一個執行檔並不在連結旁邊。
func selfExesAt(self string, resolve func(string) (string, error)) (cli, gui string, err error) {
	if real, err := resolve(self); err == nil && real != "" {
		self = real
	}
	dir, base := filepath.Split(self)
	lower := strings.ToLower(base)
	rest, ok := strings.CutPrefix(lower, "wslbakw")
	if !ok {
		if rest, ok = strings.CutPrefix(lower, "wslbak"); !ok {
			return "", "", fmt.Errorf("unexpected program name %q", base)
		}
	}
	return dir + "wslbak" + rest, dir + "wslbakw" + rest, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func sameContent(a, b string) bool {
	x, errX := fileSHA256(a)
	y, errY := fileSHA256(b)
	return errX == nil && errY == nil && x == y
}

// compareVersions 比較 1.2.3 這種版本號；不是數字的部分（例如開發中的 dev）當成 0。
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func installedVersionPath(dir string) string { return filepath.Join(dir, "version.txt") }

// installExes 把兩個執行檔複製到固定的位置。
func installExes() error {
	cli, gui, err := selfExes()
	if err != nil {
		return err
	}
	dir, err := installDir()
	if err != nil {
		return err
	}
	if err := copyFile(cli, filepath.Join(dir, installedCLI)); err != nil {
		return err
	}
	if err := copyFile(gui, filepath.Join(dir, installedGUI)); err != nil {
		return err
	}
	return os.WriteFile(installedVersionPath(dir), []byte(version+"\n"), 0o644)
}

// ensureInstalledFresh 在「從 npm 那一份執行」時，把固定位置的那一份更新成同一版。
// 沒有安裝過、自己就是固定位置的那一份、或自己是備份資料夾裡的還原用程式（旁邊沒有無視窗版）時什麼都不做。
// 不降版：固定位置的版本比較新就不動它。
func ensureInstalledFresh() {
	cli, gui, err := selfExes()
	if err != nil || !fileExists(gui) {
		return
	}
	dir, err := installDir()
	if err != nil {
		return
	}
	target := filepath.Join(dir, installedCLI)
	if !fileExists(target) || normPath(cli) == normPath(target) {
		return
	}
	if sameContent(cli, target) && sameContent(gui, filepath.Join(dir, installedGUI)) {
		return
	}
	if data, err := os.ReadFile(installedVersionPath(dir)); err == nil && compareVersions(version, strings.TrimSpace(string(data))) < 0 {
		return
	}
	// 排程的備份正在跑的時候不換檔案。
	release, held, err := acquireLock()
	if err != nil || held {
		return
	}
	defer release()
	if err := installExes(); err != nil {
		logf("could not refresh the installed copy in %s: %v", dir, err)
		return
	}
	logf("refreshed the installed copy in %s to %s", dir, version)
}

// installedProblem 檢查固定位置的程式還在不在；有問題時回傳給使用者看的說明。
// 防毒軟體把它隔離的話，排程會直接啟動失敗，連通知都發不出來，只能靠這裡發現。
func installedProblem() string {
	dir, err := installDir()
	if err != nil {
		return ""
	}
	for _, name := range []string{installedCLI, installedGUI} {
		if !fileExists(filepath.Join(dir, name)) {
			return fmt.Sprintf(T.StatusInstallMissing, filepath.Join(dir, name))
		}
	}
	return ""
}

// refreshRestoreKit 在備份的目的地放一份程式與說明：重灌之後，那裡就有還原需要的一切。
func refreshRestoreKit(dest string) error {
	source, _, err := selfExes()
	if err != nil {
		return err
	}
	if dir, err := installDir(); err == nil && fileExists(filepath.Join(dir, installedCLI)) {
		source = filepath.Join(dir, installedCLI)
	}
	target := filepath.Join(dest, installedCLI)
	if normPath(source) != normPath(target) && !sameContent(source, target) {
		if err := copyFile(source, target); err != nil {
			return err
		}
	}
	readme := []byte(enUS.RestoreReadme + "\n\n" + zhTW.RestoreReadme)
	path := filepath.Join(dest, kitReadme)
	if old, err := os.ReadFile(path); err == nil && string(old) == string(readme) {
		return nil
	}
	return writeFileAtomic(path, readme)
}

// diskNumber 回傳磁碟區所在的實體磁碟編號。跨多顆磁碟的磁碟區、網路磁碟查不到，ok 會是 false。
func diskNumber(root string) (number uint32, ok bool) {
	if len(root) < 2 || root[1] != ':' {
		return 0, false
	}
	path, err := windows.UTF16PtrFromString(`\\.\` + root[:2])
	if err != nil {
		return 0, false
	}
	// 存取權限給 0：只查詢裝置資訊，不需要系統管理員。
	h, err := windows.CreateFile(path, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(h)
	const ioctlStorageGetDeviceNumber = 0x2D1080
	var device struct{ DeviceType, DeviceNumber, PartitionNumber uint32 }
	var returned uint32
	if err := windows.DeviceIoControl(h, ioctlStorageGetDeviceNumber, nil, 0,
		(*byte)(unsafe.Pointer(&device)), uint32(unsafe.Sizeof(device)), &returned, nil); err != nil {
		return 0, false
	}
	return device.DeviceNumber, true
}

func isFAT(fs string) bool {
	return strings.HasPrefix(strings.ToUpper(fs), "FAT")
}

// defaultDest 挑一個預設的備份位置：distro 所在磁碟區以外、空間最多的內接磁碟區。
// 放在另一個磁碟區，Windows 重灌（格式化系統磁碟區）之後備份才會還在。
// 沒有別的磁碟區時退回使用者資料夾。
func defaultDest(basePath string) string {
	var best volumeInfo
	for _, v := range fixedVolumes() {
		if sameVolume(v.Root, basePath) || isFAT(v.FS) {
			continue
		}
		if v.Free > best.Free {
			best = v
		}
	}
	if best.Root != "" {
		return filepath.Join(best.Root, "WSLBackup")
	}
	profile, err := userProfile()
	if err != nil {
		return `C:\WSLBackup`
	}
	return filepath.Join(profile, "WSLBackup")
}

// chooseDistro 決定要設定哪個 distro：有指定就用指定的，否則必須剛好只有一個可以備份。
func chooseDistro(distros []regDistro, name string) (*regDistro, error) {
	if name != "" {
		d := findDistro(distros, name)
		if d == nil {
			return nil, fmt.Errorf(T.NoSuchDistro, name)
		}
		if reason, ok := eligible(*d); !ok {
			return nil, fmt.Errorf(T.DistroNotEligible, d.Name, reason)
		}
		return d, nil
	}
	var candidates []*regDistro
	for i := range distros {
		// 開發時用的測試 distro 不自動選；要備份它就明確指定。
		if _, ok := eligible(distros[i]); ok && !strings.HasPrefix(strings.ToLower(distros[i].Name), "wslbak-e2e-") {
			candidates = append(candidates, &distros[i])
		}
	}
	switch len(candidates) {
	case 0:
		return nil, errors.New(T.NoEligibleDistro)
	case 1:
		return candidates[0], nil
	}
	names := make([]string, len(candidates))
	for i, d := range candidates {
		names[i] = d.Name
	}
	return nil, fmt.Errorf(T.WhichDistro, strings.Join(names, T.ListSep))
}

// writable 確認資料夾真的寫得進去（「受控資料夾存取」會擋下沒被允許的程式）。
func writable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	probe := filepath.Join(dir, ".wslbak-write-test")
	if err := os.WriteFile(probe, []byte("wslbak"), 0o644); err != nil {
		return err
	}
	return os.Remove(probe)
}

func validWebhook(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

func cmdInit(opts options) int {
	release, exit, ok := takeLock(opts)
	if !ok {
		return exit
	}
	defer func() {
		if release != nil {
			release()
		}
	}()

	// —— 只讀不寫的檢查 ——
	wsl := wslVersion()
	if wsl == "" {
		return fail(errors.New(T.NeedStoreWSL))
	}
	distros, err := readLxss()
	if err != nil {
		return fail(err)
	}
	d, err := chooseDistro(distros, opts.distro)
	if err != nil {
		return fail(err)
	}
	cli, gui, err := selfExes()
	if err != nil {
		return fail(err)
	}
	if !fileExists(gui) {
		return fail(errors.New(T.InitNeedsPackage))
	}
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	if cfg == nil {
		cfg = newConfig()
	}
	existing := cfg.Distros[d.Name]

	fmt.Printf(T.InitProbing+"\n", d.Name)
	info, err := probeDistro(d.Name)
	if err != nil {
		return fail(fmt.Errorf(T.ProbeFailed, d.Name, err))
	}
	if info.TarKind != "gnu" {
		return fail(errors.New(T.BackupNotGNUTar))
	}

	dest := opts.dest
	if dest == "" && existing != nil {
		dest = existing.Dest
	}
	if dest == "" {
		dest = defaultDest(d.BasePath)
		if !opts.yes && !opts.dryRun {
			if ans, ok := ask(fmt.Sprintf(T.AskDest, dest)); ok && ans != "" {
				dest = ans
			}
		}
	}
	if dest, err = filepath.Abs(dest); err != nil {
		return fail(err)
	}
	if isWSLPath(dest) {
		return fail(fmt.Errorf(T.PathInsideWSL, dest))
	}
	destVol, err := volumeOf(dest)
	if err != nil {
		return fail(fmt.Errorf(T.DestUnreachable, dest, err))
	}
	if isFAT(destVol.FS) {
		return fail(fmt.Errorf(T.DestFAT, destVol.Root, destVol.FS))
	}
	keep, at, webhook := defaultKeep, cfg.At, cfg.Notify.Webhook
	if existing != nil {
		keep = existing.Keep
	}
	if opts.keep > 0 {
		keep = opts.keep
	}
	if opts.at != "" {
		at = opts.at
	}
	if opts.webhook != "" {
		webhook = opts.webhook
	}
	if webhook != "" && !validWebhook(webhook) {
		return fail(fmt.Errorf(T.BadWebhook, webhook))
	}
	sid, err := currentSID()
	if err != nil {
		return fail(err)
	}
	programDir, err := installDir()
	if err != nil {
		return fail(err)
	}
	configFile, _ := configPath()
	verifyDir, _ := verifyRoot()
	task := taskSpec{SID: sid, Command: filepath.Join(programDir, installedGUI), Arguments: taskArguments(),
		At: at, LogonDelay: true, Description: T.TaskDescription}

	// —— 把要做的事攤開來 ——
	fmt.Println()
	fmt.Println(bold(T.InitPlanTitle))
	row := func(label, value string) { fmt.Printf("  %s %s\n", pad(label, labelWidth(T.InitLabels[:])), value) }
	row(T.InitLabels[0], fmt.Sprintf(T.InitDistroLine, d.Name, info.OS, humanBytes(info.UsedBytes)))
	row(T.InitLabels[1], filepath.Join(dest, d.Name))
	row(T.InitLabels[2], fmt.Sprintf(T.InitKeepLine, keep))
	row(T.InitLabels[3], fmt.Sprintf(T.InitAtLine, at))
	if cfg.Verify == verifyRestore {
		row(T.InitLabels[4], fmt.Sprintf(T.InitVerifyLine, volumeRoot(verifyDir), humanBytes(int64(verifySpaceNeeded(info.UsedBytes)))))
	} else {
		row(T.InitLabels[4], T.InitVerifyOff)
	}
	if webhook != "" {
		row(T.InitLabels[5], fmt.Sprintf(T.InitNotifyWebhook, webhookHost(webhook)))
	} else {
		row(T.InitLabels[5], T.InitNotifyToast)
	}
	fmt.Println()
	fmt.Println(bold(T.InitChangesTitle))
	change := func(label, value string) {
		fmt.Printf("  %s %s\n", pad(label, labelWidth(T.InitChangeLabels[:])), value)
	}
	change(T.InitChangeLabels[0], filepath.Join(programDir, installedCLI)+T.ListSep+installedGUI)
	change(T.InitChangeLabels[1], configFile)
	change(T.InitChangeLabels[2], fmt.Sprintf(T.InitTaskLine, taskName(sid), task.Command, task.Arguments))
	if homeOverride == "" {
		change(T.InitChangeLabels[3], `HKCU\`+toastAppKey)
	}
	change(T.InitChangeLabels[4], filepath.Join(dest, installedCLI)+T.ListSep+kitReadme)
	fmt.Println()
	fmt.Println(T.InitWhatHappens)
	fmt.Println(T.InitNever)

	// —— 使用者應該先知道的事 ——
	if sameVolume(dest, d.BasePath) {
		fmt.Println(yellow(fmt.Sprintf(T.WarnSameVolume, destVol.Root)))
	} else if a, okA := diskNumber(destVol.Root); okA {
		if b, okB := diskNumber(volumeRoot(d.BasePath)); okB && a == b {
			fmt.Println(yellow(fmt.Sprintf(T.WarnSameDisk, destVol.Root, volumeRoot(d.BasePath))))
		}
	}
	if !destVol.Fixed {
		fmt.Println(yellow(fmt.Sprintf(T.WarnRemovable, destVol.Root)))
	}
	if one := os.Getenv("OneDrive"); one != "" && strings.HasPrefix(normPath(dest)+`\`, normPath(one)+`\`) {
		fmt.Println(yellow(T.WarnOneDrive))
	}
	if uint64(info.UsedBytes) > destVol.Free {
		fmt.Println(yellow(fmt.Sprintf(T.WarnDestSpace, destVol.Root, humanBytes(int64(destVol.Free)))))
	}
	if cfg.Verify == verifyRestore {
		if v, err := volumeOf(verifyDir); err == nil && v.Free < verifySpaceNeeded(info.UsedBytes) {
			fmt.Println(yellow(fmt.Sprintf(T.WarnVerifySpace, v.Root, humanBytes(int64(v.Free)))))
		}
	}

	if opts.dryRun {
		fmt.Println(dim(T.DryRunNothingDone))
		return 0
	}
	if !opts.yes {
		if ans, _ := ask(T.AskProceed); !isYes(ans) {
			return 0
		}
	}

	// —— 動手 ——
	if err := writable(filepath.Join(dest, d.Name)); err != nil {
		return fail(fmt.Errorf(T.DestNotWritable, dest, err))
	}
	if normPath(cli) != normPath(filepath.Join(programDir, installedCLI)) {
		if err := installExes(); err != nil {
			return fail(fmt.Errorf(T.InstallFailed, programDir, err))
		}
	}
	dc := &distroConfig{DefaultExcludes: true}
	if existing != nil {
		dc = existing
	}
	dc.ID, dc.Dest, dc.Keep, dc.Enabled = d.GUID, dest, keep, true
	cfg.Distros[d.Name] = dc
	cfg.At, cfg.Notify.Webhook = at, webhook
	if homeOverride != "" {
		// 測試用的沙箱不跳通知。
		cfg.Notify.Toast = false
	}
	if err := saveConfig(cfg); err != nil {
		return fail(err)
	}
	if homeOverride == "" {
		if err := registerToastApp(); err != nil {
			logf("register toast app: %v", err)
		}
	}
	if err := createTask(task); err != nil {
		// 有些環境不讓一般使用者建立「登入時」的觸發；退回只有每天定時。
		logf("create task with a logon trigger: %v", err)
		task.LogonDelay = false
		if err := createTask(task); err != nil {
			return fail(fmt.Errorf(T.TaskFailed, err))
		}
	}
	if err := refreshRestoreKit(dest); err != nil {
		logf("restore kit in %s: %v", dest, err)
	}
	logf("init: distro=%s dest=%s keep=%d at=%s task=%s", d.Name, dest, keep, at, taskName(sid))
	fmt.Println(green(fmt.Sprintf(T.InitDone, at)))

	if opts.yes {
		fmt.Println(T.InitRunHint)
		return 0
	}
	if ans, _ := ask(T.AskRunNow); !isYes(ans) {
		fmt.Println(T.InitRunHint)
		return 0
	}
	// 交給 run 自己去拿鎖。
	release()
	release = nil
	return cmdRun(options{command: "run", distro: d.Name})
}

func cmdUninstall(opts options) int {
	release, exit, ok := takeLock(opts)
	if !ok {
		return exit
	}
	locked := true
	defer func() {
		if locked {
			release()
		}
	}()

	sid, err := currentSID()
	if err != nil {
		return fail(err)
	}
	programDir, err := installDir()
	if err != nil {
		return fail(err)
	}
	state, err := stateDir()
	if err != nil {
		return fail(err)
	}
	cfg, _ := loadConfig()

	fmt.Println(bold(T.UninstallPlanTitle))
	fmt.Printf("  "+T.UninstallTask+"\n", taskName(sid))
	fmt.Printf("  "+T.UninstallProgram+"\n", programDir)
	if homeOverride == "" {
		fmt.Printf("  "+T.UninstallRegistry+"\n", `HKCU\`+toastAppKey)
	}
	fmt.Println(T.UninstallKeeps)
	if cfg != nil {
		for name, dc := range cfg.Distros {
			fmt.Printf("  %s\n", filepath.Join(dc.Dest, name))
		}
	}
	if opts.dryRun {
		fmt.Println(dim(T.DryRunNothingDone))
		return 0
	}
	if !opts.yes {
		if ans, _ := ask(T.AskProceed); !isYes(ans) {
			return 0
		}
	}

	if err := deleteTask(sid); err != nil {
		return fail(err)
	}
	for _, name := range []string{installedCLI, installedGUI, "version.txt"} {
		os.Remove(filepath.Join(programDir, name))
	}
	// 只移除空的資料夾：裡面如果還有別的東西，那不是我們放的。
	os.Remove(programDir)
	if homeOverride == "" {
		if err := unregisterToastApp(); err != nil {
			logf("unregister toast app: %v", err)
		}
	}
	if root, err := verifyRoot(); err == nil {
		sweepStale(root)
	}
	logf("uninstall: task and program removed")
	fmt.Println(green(T.UninstallDone))

	// 設定與紀錄預設留著：重新 init 時可以接著用。
	if opts.yes {
		return 0
	}
	if ans, _ := ask(fmt.Sprintf(T.AskRemoveConfig, state)); !isYes(ans) {
		return 0
	}
	if runLog != nil {
		runLog.Close()
		runLog = nil
	}
	release()
	locked = false
	for _, name := range []string{"config.json", "state.json", "wslbak.log", "wslbak.log.1", "wslbak.lock"} {
		os.Remove(filepath.Join(state, name))
	}
	os.Remove(filepath.Join(state, "verify"))
	os.Remove(state)
	return 0
}
