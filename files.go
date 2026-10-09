package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/gzip"
)

// files 指令：看一份備份裡有什麼，或用檔名的一部分去找。

func (m *manifest) indexPath() string { return filepath.Join(m.dir, m.ID+indexSuffix) }

// ensureIndex 確認這份備份有檔案索引；沒有的話把封存掃一遍建立起來（只需要做一次）。
func ensureIndex(m *manifest) error {
	if fileExists(m.indexPath()) {
		return nil
	}
	fmt.Fprintln(os.Stderr, dim(T.FilesBuildingIndex))
	file, err := os.Open(m.archivePath())
	if err != nil {
		return err
	}
	defer file.Close()
	zr, err := gzip.NewReader(bufio.NewReaderSize(file, 1<<20))
	if err != nil {
		return err
	}
	defer zr.Close()
	w, err := newIndexWriter(m.indexPath(), m.ID)
	if err != nil {
		return err
	}
	_, scanErr := scanTar(zr, w.add)
	if err := w.close(scanErr == nil); err != nil {
		return err
	}
	return scanErr
}

// pickBackup 依 -d 與編號選出一份備份；沒有給編號就是最新的一份。
func pickBackup(opts options) (*manifest, int) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, fail(err)
	}
	source, code := pickSource(cfg, opts.distro)
	if source == nil {
		return nil, code
	}
	if opts.id != "" {
		if m := readManifest(source.Dir, opts.id); m != nil {
			return m, 0
		}
		return nil, fail(fmt.Errorf(T.NoSuchBackup, opts.id))
	}
	if m := newest(listBackups(source.Dir), false); m != nil {
		return m, 0
	}
	return nil, fail(fmt.Errorf(T.NoBackups, source.Dir))
}

func fileRow(e indexEntry, name string) []string {
	size := ""
	if e.Type == typeFile {
		size = humanBytes(e.Size)
	}
	switch e.Type {
	case typeSymlink:
		name += " -> " + e.Link
	case typeHardlink:
		name += " = " + displayPath(e.Link)
	case typeDir:
		name += "/"
	}
	return []string{modeString(e), fmt.Sprintf("%d:%d", e.UID, e.GID), size, time.Unix(e.MTime, 0).Format("2006-01-02 15:04"), name}
}

func cmdFiles(opts options) int {
	m, code := pickBackup(opts)
	if m == nil {
		return code
	}
	if err := ensureIndex(m); err != nil {
		return fail(fmt.Errorf(T.FilesNoIndex, m.ID, err))
	}

	if opts.find != "" {
		needle := strings.ToLower(opts.find)
		var rows [][]string
		err := scanIndex(m.indexPath(), func(e indexEntry) bool {
			if strings.Contains(strings.ToLower(e.name()), needle) {
				rows = append(rows, fileRow(e, displayPath(e.name())))
			}
			return true
		})
		if err != nil {
			return fail(err)
		}
		if len(rows) == 0 {
			fmt.Printf(T.FilesFindNone+"\n", opts.find, m.ID)
			return 1
		}
		printRows(rows)
		return 0
	}

	target, err := normalizeMemberPath(opts.target)
	if opts.target == "" {
		target, err = ".", nil
	}
	if errors.Is(err, errBadMemberPath) {
		return fail(fmt.Errorf(T.BadPath, opts.target))
	}
	// 要列的是 target 自己（如果它是檔案）或它底下的那一層（如果它是目錄）。
	var self *indexEntry
	var children []indexEntry
	err = scanIndex(m.indexPath(), func(e indexEntry) bool {
		name := e.name()
		switch {
		case name == target:
			copy := e
			self = &copy
		case name != "." && path.Dir(name) == target:
			children = append(children, e)
		}
		return true
	})
	if err != nil {
		return fail(err)
	}
	if self == nil && len(children) == 0 {
		fmt.Fprintln(os.Stderr, red(fmt.Sprintf(T.FilesNotFound, displayPath(target), m.ID)))
		return 1
	}
	fmt.Println(dim(fmt.Sprintf(T.FilesTitle, m.ID, m.Distro, displayPath(target))))
	if self != nil && self.Type != typeDir {
		printRows([][]string{fileRow(*self, displayPath(target))})
		return 0
	}
	sort.Slice(children, func(i, j int) bool { return children[i].name() < children[j].name() })
	rows := make([][]string, len(children))
	for i, e := range children {
		rows[i] = fileRow(e, path.Base(e.name()))
	}
	printRows(rows)
	return 0
}

// printRows 印出對齊的清單，大小那一欄靠右。
func printRows(rows [][]string) {
	widthOf := make([]int, 4)
	for _, row := range rows {
		for c := 0; c < 4; c++ {
			widthOf[c] = max(widthOf[c], widths.StringWidth(row[c]))
		}
	}
	for _, row := range rows {
		fmt.Printf("%s  %s  %s%s  %s  %s\n", row[0], pad(row[1], widthOf[1]),
			strings.Repeat(" ", widthOf[2]-widths.StringWidth(row[2])), row[2], row[3], row[4])
	}
}
