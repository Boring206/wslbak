package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// 還原只做一件事：把封存匯入成一個「新的」distro。
// 它絕不覆蓋、移除或改動既有的 distro，也不啟動還原出來的那一個
// （啟動會讓它的服務跑起來；原本的 distro 如果還在，兩邊會同時執行同一批服務）。

// restoreName 決定還原出來的 distro 叫什麼：原本的名稱沒人用就沿用，
// 否則加上「-restored-日期」，還是撞名就再加序號。
func restoreName(original string, existing []string, now time.Time) string {
	taken := func(name string) bool {
		for _, e := range existing {
			if strings.EqualFold(e, name) {
				return true
			}
		}
		return false
	}
	if !taken(original) {
		return original
	}
	base := original + "-restored-" + now.Format("20060102")
	if len(base) > 60 {
		base = base[len(base)-60:]
	}
	name := base
	for i := 2; taken(name); i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

// emptyOrMissing 回報資料夾是不是不存在或是空的。
func emptyOrMissing(dir string) bool {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return true
	}
	return err == nil && len(entries) == 0
}

func userProfile() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_Profile, 0)
}

// fileSHA256 算整個檔案的 SHA-256。
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, bufio.NewReaderSize(f, 1<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// backupSource 是一個 distro 的備份資料夾。
type backupSource struct {
	Distro string
	Dir    string
}

// backupSources 找出備份放在哪裡。有設定檔就照設定；沒有的話（例如重灌之後，
// 直接執行備份資料夾裡的 wslbak.exe）把程式所在的資料夾當成備份的目的地。
func backupSources(cfg *config) []backupSource {
	var list []backupSource
	if cfg != nil {
		for name, d := range cfg.Distros {
			list = append(list, backupSource{name, filepath.Join(d.Dest, name)})
		}
		return list
	}
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	entries, _ := os.ReadDir(filepath.Dir(self))
	for _, e := range entries {
		dir := filepath.Join(filepath.Dir(self), e.Name())
		if e.IsDir() && len(listBackups(dir)) > 0 {
			list = append(list, backupSource{e.Name(), dir})
		}
	}
	return list
}

func cmdRestore(opts options) int {
	cfg, err := loadConfig()
	if err != nil {
		return fail(err)
	}
	source, code := pickSource(cfg, opts.distro)
	if source == nil {
		return code
	}
	backups := listBackups(source.Dir)
	if len(backups) == 0 {
		return fail(fmt.Errorf(T.NoBackups, source.Dir))
	}
	var m *manifest
	if opts.id != "" {
		if m = readManifest(source.Dir, opts.id); m == nil {
			return fail(fmt.Errorf(T.NoSuchBackup, opts.id))
		}
	} else if m = newest(backups, true); m == nil {
		// 沒有任何一份通過試還原：仍然可以還原最新的，但要講清楚。
		m = newest(backups, false)
	}

	distros, err := readLxss()
	if err != nil {
		return fail(err)
	}
	var existing []string
	for _, d := range distros {
		existing = append(existing, d.Name)
	}
	name := opts.name
	if name == "" {
		name = restoreName(m.Distro, existing, time.Now())
	}
	if !distroNameRe.MatchString(name) {
		return fail(fmt.Errorf(T.BadDistroName, name))
	}
	if findDistro(distros, name) != nil {
		return fail(fmt.Errorf(T.RestoreNameTaken, name))
	}
	target := opts.to
	if target == "" {
		profile, err := userProfile()
		if err != nil {
			return fail(err)
		}
		target = filepath.Join(profile, "WSL", name)
	}
	if target, err = filepath.Abs(target); err != nil {
		return fail(err)
	}
	if isWSLPath(target) {
		return fail(fmt.Errorf(T.PathInsideWSL, target))
	}
	if !emptyOrMissing(target) {
		return fail(fmt.Errorf(T.RestoreTargetUsed, target))
	}

	fmt.Printf(T.RestorePlan+"\n", bold(m.ID), m.Distro, humanBytes(m.Size), verifyLabel(m), bold(name), target)
	if !m.verified() {
		fmt.Println(yellow(T.RestoreUnverified))
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

	fmt.Println(T.RestoreChecking)
	sum, err := fileSHA256(m.archivePath())
	if err != nil {
		return fail(err)
	}
	if sum != m.SHA256 {
		return fail(fmt.Errorf(T.RestoreCorrupt, m.Archive))
	}
	fmt.Println(T.RestoreImporting)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fail(err)
	}
	began := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), importTimeout)
	defer cancel()
	if err := importDistro(ctx, name, target, m.archivePath(), nil); err != nil {
		return fail(err)
	}
	// 匯入的 distro 預設用 root 登入；設回備份當時的預設使用者。
	if m.DefaultUID != 0 {
		if err := wslConfigure(name, m.DefaultUID, m.Flags); err != nil {
			logf("WslConfigureDistribution(%s): %v", name, err)
			fmt.Println(yellow(fmt.Sprintf(T.RestoreUserHint, m.DefaultUID)))
		}
	}
	fmt.Println(green(fmt.Sprintf(T.RestoreDone, name, humanDuration(time.Since(began)))))
	fmt.Printf(T.RestoreNext+"\n", name)
	if findDistro(distros, m.Distro) != nil && !strings.EqualFold(name, m.Distro) {
		fmt.Println(yellow(fmt.Sprintf(T.RestoreTwins, m.Distro)))
	}
	return 0
}

// pickSource 依 -d 或「只有一個」選出要用哪個 distro 的備份；選不出來時印出原因並回傳結束碼。
func pickSource(cfg *config, distro string) (*backupSource, int) {
	sources := backupSources(cfg)
	if len(sources) == 0 {
		if cfg == nil {
			return nil, fail(fmt.Errorf("%s", T.NotSetUp))
		}
		return nil, fail(fmt.Errorf("%s", T.NothingConfigured))
	}
	if distro != "" {
		for i := range sources {
			if strings.EqualFold(sources[i].Distro, distro) {
				return &sources[i], 0
			}
		}
		return nil, fail(fmt.Errorf(T.DistroNotConfigured, distro))
	}
	if len(sources) > 1 {
		names := make([]string, len(sources))
		for i, s := range sources {
			names[i] = s.Distro
		}
		return nil, fail(fmt.Errorf(T.WhichDistro, strings.Join(names, T.ListSep)))
	}
	return &sources[0], 0
}
