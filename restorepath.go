package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/klauspost/compress/gzip"
)

// 只取回備份裡的某些檔案或資料夾（restore --path … --into …）。
//
// 做法：在 Windows 這邊讀備份檔，用 walkTar 把選中的項目原樣濾出來，
// 只把這一小段 tar 送進 distro，由那裡的 GNU tar 解開，擁有者、權限、延伸屬性都保留。
// 目的地一定是不存在或空的資料夾，完整的路徑會在它底下重建，所以不會蓋掉任何現有的檔案。

// stagedLinkRe 是 restorefiles.sh 回報的暫時連結。它會出現在命令列上，
// 所以只接受我們自己的格式，不照單全收腳本印出來的東西。
var stagedLinkRe = regexp.MustCompile(`^/(run|dev/shm|tmp)/wslbak-[0-9a-f]{16}$`)

// tarPathRe 是 restorefiles.sh 回報的 tar 的位置。它也會出現在命令列上，同樣只接受單純的絕對路徑。
var tarPathRe = regexp.MustCompile(`^(/[A-Za-z0-9_.+-]+){1,12}/tar$`)

// tarProgram 決定解開檔案時要執行的 tar：腳本回報了可用的絕對路徑就用它，否則讓 distro 自己從 PATH 找。
func tarProgram(reported []string) string {
	if len(reported) == 1 && len(reported[0]) <= 200 && tarPathRe.MatchString(reported[0]) {
		return reported[0]
	}
	return "tar"
}

// selection 是使用者要取回的那些路徑，已經整理成封存裡的名稱。
type selection struct {
	roots  []string        // 選中的路徑；目錄代表它底下的一切
	extras map[string]bool // 另外要帶上的項目：選中的硬連結所指向的檔案
}

func (s *selection) wants(name string) bool {
	name = cleanMember(name)
	if s.extras[name] {
		return true
	}
	for _, root := range s.roots {
		if name == root || strings.HasPrefix(name, root+"/") {
			return true
		}
	}
	return false
}

type selectionStats struct {
	Entries int
	Bytes   int64
	Missing []string // 索引裡找不到的路徑
}

// resolveSelection 把使用者給的路徑和索引對照。路徑只拿來比對，不會原樣交給任何指令；
// 索引裡沒有的路徑會列在 Missing 裡。
func resolveSelection(indexFile string, paths []string) (*selection, selectionStats, error) {
	sel := &selection{extras: map[string]bool{}}
	var stats selectionStats
	for _, p := range paths {
		member, err := normalizeMemberPath(p)
		if err != nil {
			return nil, stats, fmt.Errorf(T.BadPath, p)
		}
		if member == "." {
			return nil, stats, errors.New(T.PathRoot)
		}
		if !slices.Contains(sel.roots, member) {
			sel.roots = append(sel.roots, member)
		}
	}
	found := map[string]bool{}
	selected := map[string]bool{}
	var links []string
	err := scanIndex(indexFile, func(e indexEntry) bool {
		name := e.name()
		for _, root := range sel.roots {
			if name == root || strings.HasPrefix(name, root+"/") {
				found[root] = true
				selected[name] = true
				stats.Entries++
				stats.Bytes += e.Size
				if e.Type == typeHardlink {
					links = append(links, e.Link)
				}
				break
			}
		}
		return true
	})
	if err != nil {
		return nil, stats, err
	}
	// 硬連結在 tar 裡只記「和哪個檔案是同一個」，那個檔案沒有一起解開的話連結建不起來。
	for _, target := range links {
		if !selected[target] {
			sel.extras[target] = true
		}
	}
	for _, root := range sel.roots {
		if !found[root] {
			stats.Missing = append(stats.Missing, displayPath(root))
		}
	}
	return sel, stats, nil
}

// validInto 檢查 --into 的值：distro 裡的絕對路徑，不能是根目錄，不能有 .. 或換行。
func validInto(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\n\r") || strings.Trim(p, "/") == "" {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// extractArgs 是在 distro 裡解開檔案用的命令列。除了我們自己產生的連結與檢查過的 tar 位置以外，全部是固定的字。
func extractArgs(distro, tar, link string, dropped []string) []string {
	args := []string{"-d", distro, "-u", "root", "-e", "env", "LC_ALL=C", tar,
		"--extract", "--file=-", "--directory=" + link,
		"--numeric-owner", "--same-owner", "--same-permissions",
		// 目的地是空的，本來就沒有東西可以蓋；這個選項確保萬一有，也是報錯而不是覆蓋。
		"--keep-old-files"}
	if !slices.Contains(dropped, "--xattrs") {
		// GNU tar 解開時預設只還原 user.* 的延伸屬性；capabilities 在 security.* 底下。
		args = append(args, "--xattrs", "--xattrs-include=*")
	}
	if !slices.Contains(dropped, "--acls") && !slices.Contains(dropped, "--xattrs") {
		args = append(args, "--acls")
	}
	return args
}

func prepareError(code string, target string) error {
	switch code {
	case "not-empty":
		return fmt.Errorf(T.PathTargetUsed, target)
	case "not-a-directory":
		return fmt.Errorf(T.PathTargetNotDir, target)
	case "no-parent":
		return fmt.Errorf(T.PathTargetNoParent, target)
	}
	return fmt.Errorf(T.PathPrepareFailed, target, code)
}

func cmdRestorePath(opts options) int {
	if !validInto(opts.into) {
		return fail(fmt.Errorf(T.BadInto, opts.into))
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
	source, code := pickSource(cfg, opts.distro)
	if source == nil {
		return code
	}
	var m *manifest
	if opts.id != "" {
		if m = readManifest(source.Dir, opts.id); m == nil {
			return fail(fmt.Errorf(T.NoSuchBackup, opts.id))
		}
	} else {
		backups := listBackups(source.Dir)
		if m = newest(backups, true); m == nil {
			if m = newest(backups, false); m == nil {
				return fail(fmt.Errorf(T.NoBackups, source.Dir))
			}
		}
	}
	// 檔案只放回這份備份所屬的 distro。備份資料夾裡的紀錄寫著別的名字時，寧可不做。
	if !strings.EqualFold(m.Distro, source.Distro) {
		return fail(fmt.Errorf(T.PathWrongDistro, m.ID, m.Distro, source.Distro))
	}
	if err := ensureIndex(m); err != nil {
		return fail(fmt.Errorf(T.FilesNoIndex, m.ID, err))
	}
	sel, stats, err := resolveSelection(m.indexPath(), opts.paths)
	if err != nil {
		return fail(err)
	}
	if len(stats.Missing) > 0 {
		return fail(fmt.Errorf(T.FilesNotFound, strings.Join(stats.Missing, T.ListSep), m.ID))
	}

	distros, err := readLxss()
	if err != nil {
		return fail(err)
	}
	d := findDistro(distros, m.Distro)
	if d == nil {
		return fail(fmt.Errorf(T.PathDistroGone, m.Distro))
	}
	if reason, ok := eligible(*d); !ok {
		return fail(fmt.Errorf(T.DistroNotEligible, d.Name, reason))
	}

	fmt.Printf(T.PathPlan+"\n", stats.Entries, humanBytes(stats.Bytes), bold(m.ID), d.Name, bold(opts.into))
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

	// 1. 在 distro 裡準備目的地，並拿到指向它的暫時連結。
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return fail(err)
	}
	out, err := runInDistro(d.Name, withVars(restoreFilesScript,
		scriptVar{"WSLBAK_TARGET", opts.into},
		scriptVar{"WSLBAK_TOKEN", hex.EncodeToString(token[:])},
	), time.Minute)
	rows := parseProto(decodeWSLText(out))
	if f, ok := protoValue(rows, "error"); ok && len(f) > 0 {
		return fail(prepareError(f[0], opts.into))
	}
	staged, _ := protoValue(rows, "staged")
	if _, done := protoValue(rows, "done"); !done || len(staged) != 1 || !stagedLinkRe.MatchString(staged[0]) {
		logf("restore --path: preparing %s in %s failed: %v: %s", opts.into, d.Name, err, firstLine(decodeWSLText(out)))
		return fail(prepareError("unexpected", opts.into))
	}
	link := staged[0]
	reportedTar, _ := protoValue(rows, "tar")
	defer func() {
		// 把暫時的連結拿掉。經由同一份腳本來做：直接執行 rm 的話，工具不在一般位置的 distro（NixOS）找不到它。
		runInDistro(d.Name, withVars(restoreFilesScript, scriptVar{"WSLBAK_REMOVE", link}), 30*time.Second)
	}()

	// 2. 讀備份檔、濾出選中的項目、送進 distro 解開。
	fmt.Println(T.PathExtracting)
	file, err := os.Open(m.archivePath())
	if err != nil {
		return fail(err)
	}
	defer file.Close()
	sum := sha256.New()
	hashed := io.TeeReader(bufio.NewReaderSize(file, 1<<20), sum)
	zr, err := gzip.NewReader(hashed)
	if err != nil {
		return fail(fmt.Errorf(T.RestoreCorrupt, m.Archive))
	}
	defer zr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), importTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, system32("wsl.exe"), extractArgs(d.Name, tarProgram(reportedTar), link, m.Dropped)...)
	cmd.Dir = systemRoot()
	cmd.Env = append(os.Environ(), "WSL_UTF8=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	cmd.WaitDelay = waitDelay
	pipeR, pipeW := io.Pipe()
	cmd.Stdin = pipeR
	var tarOut strings.Builder
	cmd.Stdout, cmd.Stderr = &tarOut, &tarOut

	progress := &progressReader{r: zr}
	bar := &progressLine{label: T.ProgressScanning}
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				bar.update(progress.n.Load())
			}
		}
	}()
	walked := make(chan error, 1)
	go func() {
		_, err := walkTar(progress, pipeW, func(e rawEntry) bool { return sel.wants(e.Name) })
		if err == nil {
			// 接上結尾標記，解開的那一端才知道結束了。
			_, err = pipeW.Write(make([]byte, 2*tarBlock))
		}
		pipeW.CloseWithError(err)
		walked <- err
	}()
	runErr := cmd.Run()
	// tar 先結束的話（例如出錯），讓還在寫的那一端跟著停下來。
	pipeR.CloseWithError(io.ErrClosedPipe)
	walkErr := <-walked
	close(stop)
	bar.clear()

	detail := firstLine(tarOut.String())
	switch {
	case runErr != nil:
		logf("restore --path: tar in %s failed: %v: %s", d.Name, runErr, tarOut.String())
		return fail(fmt.Errorf(T.PathFailed, detail))
	case walkErr != nil:
		logf("restore --path: reading %s failed: %v", m.Archive, walkErr)
		return fail(fmt.Errorf(T.RestoreCorrupt, m.Archive))
	}
	// 3. 整個備份檔都讀過了，順便確認它和備份當時一樣。
	if _, err := io.Copy(io.Discard, hashed); err != nil || hex.EncodeToString(sum.Sum(nil)) != m.SHA256 {
		fmt.Fprintln(os.Stderr, yellow(fmt.Sprintf(T.PathArchiveChanged, m.Archive)))
		return 2
	}
	logf("restore --path: %d entries from %s into %s:%s", stats.Entries, m.ID, d.Name, opts.into)
	fmt.Println(green(fmt.Sprintf(T.PathDone, stats.Entries, opts.into)))
	fmt.Printf(T.PathExplorer+"\n", `\\wsl.localhost\`+d.Name+strings.ReplaceAll(opts.into, "/", `\`))
	return 0
}
