package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNormalizeMemberPath(t *testing.T) {
	good := map[string]string{
		"/":                   ".",
		".":                   ".",
		"./":                  ".",
		"/etc/passwd":         "./etc/passwd",
		"etc/passwd":          "./etc/passwd",
		"./etc/passwd":        "./etc/passwd",
		"/home/me/project/":   "./home/me/project",
		"//home///me":         "./home/me",
		"/home/me/a b/中文.txt": "./home/me/a b/中文.txt",
		"/home/me/..hidden":   "./home/me/..hidden",
		"/home/./me":          "./home/me",
	}
	for in, want := range good {
		if got, err := normalizeMemberPath(in); err != nil || got != want {
			t.Errorf("normalizeMemberPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// 帶 .. 的路徑一律拒絕，不去猜它整理之後會指到哪裡。
	for _, in := range []string{"", "..", "/..", "/etc/../shadow", "/home/me/../../etc", "a/..", "/a\x00b"} {
		if got, err := normalizeMemberPath(in); !errors.Is(err, errBadMemberPath) {
			t.Errorf("normalizeMemberPath(%q) = %q, %v; want an error", in, got, err)
		}
	}
	for member, want := range map[string]string{"./etc/passwd": "./etc", "./etc": ".", "./home/me/a b/c": "./home/me/a b", ".": "."} {
		if got := parentOf(member); got != want {
			t.Errorf("parentOf(%q) = %q, want %q", member, got, want)
		}
	}
	for member, want := range map[string]string{".": "/", "./etc": "/etc", "./home/me/x y": "/home/me/x y"} {
		if got := displayPath(member); got != want {
			t.Errorf("displayPath(%q) = %q, want %q", member, got, want)
		}
	}
}

func TestModeString(t *testing.T) {
	cases := []struct {
		e    indexEntry
		want string
	}{
		{indexEntry{Type: typeFile, Mode: 0o644}, "-rw-r--r--"},
		{indexEntry{Type: typeDir, Mode: 0o755}, "drwxr-xr-x"},
		{indexEntry{Type: typeSymlink, Mode: 0o777}, "lrwxrwxrwx"},
		{indexEntry{Type: typeFile, Mode: 0o4755}, "-rwsr-xr-x"},
		{indexEntry{Type: typeFile, Mode: 0o4644}, "-rwSr--r--"},
		{indexEntry{Type: typeDir, Mode: 0o2775}, "drwxrwsr-x"},
		{indexEntry{Type: typeDir, Mode: 0o1777}, "drwxrwxrwt"},
		{indexEntry{Type: typeDir, Mode: 0o1770}, "drwxrwx--T"},
		{indexEntry{Type: typeChar, Mode: 0o600}, "crw-------"},
		{indexEntry{Type: typeFifo, Mode: 0}, "p---------"},
		{indexEntry{Type: typeOther, Mode: 0o644}, "?rw-r--r--"},
	}
	for _, c := range cases {
		if got := modeString(c.e); got != c.want {
			t.Errorf("modeString(%s %o) = %q, want %q", c.e.Type, c.e.Mode, got, c.want)
		}
	}
}

func TestIndexRoundTrip(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "20261009T030000Z"+indexSuffix)
	when := time.Date(2026, 10, 9, 3, 0, 0, 0, time.UTC)
	headers := []*tar.Header{
		{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: when},
		{Name: "./etc/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: when},
		{Name: "./etc/passwd", Typeflag: tar.TypeReg, Mode: 0o100644, Size: 30, ModTime: when, Uid: 0, Gid: 0},
		{Name: "./usr/bin/sudo", Typeflag: tar.TypeReg, Mode: 0o104755, Size: 277936, ModTime: when},
		{Name: "./bin", Typeflag: tar.TypeSymlink, Linkname: "usr/bin", Mode: 0o777, ModTime: when},
		{Name: "./home/me/hard2", Typeflag: tar.TypeLink, Linkname: "./home/me/hard1", Mode: 0o644, ModTime: when, Uid: 1000, Gid: 1000},
		{Name: "./home/me/中文 檔名.txt", Typeflag: tar.TypeReg, Mode: 0o600, Size: 13, ModTime: when, Uid: 1000, Gid: 1000},
		// 不是合法 UTF-8 的檔名（例如從舊系統搬來的 Big5 檔名）也要原樣保留。
		{Name: "./home/me/\xa4\xa4\xa4\xe5.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1, ModTime: when},
		{Name: "./dev/null-copy", Typeflag: tar.TypeChar, Mode: 0o666, ModTime: when},
	}
	w, err := newIndexWriter(file, "20261009T030000Z")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range headers {
		w.add(entryFromHeader(h))
	}
	// 還沒 close 之前，正式的檔名不該出現。
	if _, err := os.Stat(file); err == nil {
		t.Error("the index must not exist under its final name before it is complete")
	}
	if err := w.close(true); err != nil {
		t.Fatal(err)
	}

	var got []indexEntry
	if err := scanIndex(file, func(e indexEntry) bool { got = append(got, e); return true }); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(headers) {
		t.Fatalf("read %d entries, wrote %d", len(got), len(headers))
	}
	byName := map[string]indexEntry{}
	for _, e := range got {
		byName[e.name()] = e
	}
	if e := byName["."]; e.Type != typeDir {
		t.Errorf("the root should be stored as \".\": %+v", e)
	}
	if e := byName["./etc"]; e.Type != typeDir || e.Mode != 0o755 {
		t.Errorf("./etc: %+v (the trailing slash should be gone)", e)
	}
	if e := byName["./etc/passwd"]; e.Type != typeFile || e.Size != 30 || e.Mode != 0o644 || e.MTime != when.Unix() {
		t.Errorf("./etc/passwd: %+v", e)
	}
	if e := byName["./usr/bin/sudo"]; e.Mode != 0o4755 || modeString(e) != "-rwsr-xr-x" {
		t.Errorf("setuid bit lost: %+v", e)
	}
	if e := byName["./bin"]; e.Type != typeSymlink || e.Link != "usr/bin" {
		t.Errorf("./bin: %+v", e)
	}
	if e := byName["./home/me/hard2"]; e.Type != typeHardlink || e.Link != "./home/me/hard1" || e.UID != 1000 {
		t.Errorf("hard link: %+v", e)
	}
	if e, ok := byName["./home/me/\xa4\xa4\xa4\xe5.txt"]; !ok || e.Path != "" || e.PathB == "" {
		t.Errorf("a name that is not valid UTF-8 should round-trip through the base64 field: %+v, found=%v", e, ok)
	}
	if e := byName["./home/me/中文 檔名.txt"]; e.Path == "" || e.PathB != "" {
		t.Errorf("a valid UTF-8 name should be stored as text: %+v", e)
	}

	// 提早結束。
	seen := 0
	scanIndex(file, func(indexEntry) bool { seen++; return seen < 3 })
	if seen != 3 {
		t.Errorf("visit returned false after 3 entries, but %d were visited", seen)
	}

	// 失敗的備份不留索引。
	other := filepath.Join(dir, "20261009T040000Z"+indexSuffix)
	w2, _ := newIndexWriter(other, "20261009T040000Z")
	w2.add(entryFromHeader(headers[0]))
	w2.close(false)
	if left, _ := filepath.Glob(filepath.Join(dir, "20261009T040000Z*")); len(left) != 0 {
		t.Errorf("a failed backup left index files behind: %v", left)
	}

	// 不是索引的檔案、比這個版本新的索引。
	junk := filepath.Join(dir, "junk"+indexSuffix)
	os.WriteFile(junk, []byte("not gzip"), 0o644)
	if err := scanIndex(junk, func(indexEntry) bool { return true }); !errors.Is(err, errBadIndex) {
		t.Errorf("junk file: err = %v", err)
	}
	if err := scanIndex(filepath.Join(dir, "missing"+indexSuffix), func(indexEntry) bool { return true }); err == nil {
		t.Error("a missing index should be an error")
	}
}

// 備份時掃描 tar 的同時就產生索引：用真的 GNU tar 封存檢查兩邊一致。
func TestScanTarFeedsTheIndex(t *testing.T) {
	data, err := os.ReadFile("testdata/gnu.tar")
	if err != nil {
		t.Skip("testdata/gnu.tar is not available next to the test binary")
	}
	var entries []indexEntry
	idx, err := scanTar(bytes.NewReader(data), func(e indexEntry) { entries = append(entries, e) })
	if err != nil || int64(len(entries)) != idx.Entries {
		t.Fatalf("scanTar: %v; %d index entries for %d tar entries", err, len(entries), idx.Entries)
	}
	byName := map[string]indexEntry{}
	for _, e := range entries {
		byName[e.name()] = e
	}
	if e := byName["./fixture/sparse.bin"]; e.Type != typeFile || e.Size != 9<<30 {
		t.Errorf("sparse.bin should be listed with its logical size: %+v", e)
	}
	if e := byName["./fixture/hard2"]; e.Type != typeHardlink || e.Link != "./fixture/hard1" {
		t.Errorf("hard2: %+v", e)
	}
	if e := byName["./fixture/symlink"]; e.Type != typeSymlink || e.Link != "plain.txt" {
		t.Errorf("symlink: %+v", e)
	}
	if e := byName["./fixture/plain.txt"]; e.UID != 1234 || e.GID != 5678 || e.Size != 6 {
		t.Errorf("plain.txt: %+v", e)
	}
	if e := byName["./fixture/fifo"]; e.Type != typeFifo {
		t.Errorf("fifo: %+v", e)
	}
	if e := byName["./fixture/devnull"]; e.Type != typeChar {
		t.Errorf("devnull: %+v", e)
	}
	if e := byName["./etc"]; e.Type != typeDir {
		t.Errorf("./etc: %+v", e)
	}
}

func stdlibNames(t *testing.T, data []byte) []string {
	t.Helper()
	var names []string
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
}

// walkTar 只判讀標頭，所以要確認它看到的名稱和標準庫完全一樣，而且原樣通過的位元組一個不差。
func TestWalkTarAgainstStdlib(t *testing.T) {
	archives := map[string][]byte{}
	if data, err := os.ReadFile("testdata/gnu.tar"); err == nil {
		archives["real GNU tar, pax format"] = data
	}
	synthetic, _ := buildTar(t, sampleEntries())
	archives["written by Go, pax format"] = synthetic

	// GNU 格式的長檔名與長連結（L、K 標頭）。
	var gnu bytes.Buffer
	gw := tar.NewWriter(&gnu)
	long := "./" + strings.Repeat("d", 120) + "/" + strings.Repeat("f", 150) + ".txt"
	for _, h := range []*tar.Header{
		{Name: long, Typeflag: tar.TypeReg, Mode: 0o644, Size: 4, Format: tar.FormatGNU},
		{Name: "./link-to-long", Typeflag: tar.TypeSymlink, Linkname: long, Mode: 0o777, Format: tar.FormatGNU},
		{Name: "./short", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2, Format: tar.FormatGNU},
	} {
		if err := gw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		gw.Write(bytes.Repeat([]byte{'x'}, int(h.Size)))
	}
	gw.Close()
	archives["written by Go, GNU format with long names"] = gnu.Bytes()

	// 檔名前綴欄位（純 ustar）。
	var ustar bytes.Buffer
	uw := tar.NewWriter(&ustar)
	prefixed := "./" + strings.Repeat("p", 60) + "/" + strings.Repeat("q", 60) + "/name.txt"
	uw.WriteHeader(&tar.Header{Name: prefixed, Typeflag: tar.TypeReg, Mode: 0o644, Size: 1, Format: tar.FormatUSTAR})
	uw.Write([]byte{'x'})
	uw.Close()
	archives["written by Go, ustar with a name prefix"] = ustar.Bytes()

	for label, data := range archives {
		want := stdlibNames(t, data)
		var got []string
		var all bytes.Buffer
		n, err := walkTar(bytes.NewReader(data), &all, func(e rawEntry) bool { got = append(got, e.Name); return true })
		if err != nil {
			t.Errorf("%s: %v", label, err)
			continue
		}
		if !reflect.DeepEqual(got, want) || n != int64(len(want)) {
			t.Errorf("%s: walkTar saw %d names, the standard library %d\n  got  %q\n  want %q", label, len(got), len(want), got, want)
		}
		// 全部都要的時候，輸出就是原本的封存去掉結尾標記。
		if !bytes.HasPrefix(data, all.Bytes()) || !isZero(data[all.Len():]) {
			t.Errorf("%s: passing every entry through did not reproduce the archive (%d of %d bytes)", label, all.Len(), len(data))
		}
	}
}

func TestWalkTarSelects(t *testing.T) {
	data, err := os.ReadFile("testdata/gnu.tar")
	if err != nil {
		t.Skip("testdata/gnu.tar is not available next to the test binary")
	}
	pick := map[string]bool{
		"./fixture/plain.txt":           true, // 帶延伸屬性與特別的擁有者
		"./fixture/cap-binary":          true, // 帶 capability
		"./fixture/sparse.bin":          true, // 稀疏檔，邏輯大小超過 8 GiB
		"./fixture/中文檔名 with space.txt": true,
		"./fixture/hard1":               true,
		"./fixture/hard2":               true,
	}
	var out bytes.Buffer
	if _, err := walkTar(bytes.NewReader(data), &out, func(e rawEntry) bool { return pick[e.Name] }); err != nil {
		t.Fatal(err)
	}
	out.Write(make([]byte, 2*tarBlock))

	// 選出來的那一段本身要是合法的 tar，內容與屬性都和原本一樣。
	original := map[string]*tar.Header{}
	sums := map[string]string{}
	read := func(src []byte, into map[string]*tar.Header, hashes map[string]string) []string {
		var names []string
		tr := tar.NewReader(bytes.NewReader(src))
		for {
			h, err := tr.Next()
			if err == io.EOF {
				return names
			}
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, h.Name)
			into[h.Name] = h
			if h.Typeflag == tar.TypeReg && h.Size < 1<<20 {
				sum := sha256.New()
				io.Copy(sum, tr)
				hashes[h.Name] = hex.EncodeToString(sum.Sum(nil))
			}
		}
	}
	read(data, original, sums)
	selected, selectedSums := map[string]*tar.Header{}, map[string]string{}
	names := read(out.Bytes(), selected, selectedSums)
	if len(names) != len(pick) {
		t.Fatalf("selected %d entries, want %d: %q", len(names), len(pick), names)
	}
	for name := range pick {
		got, want := selected[name], original[name]
		if got == nil {
			t.Errorf("%s is missing from the selection", name)
			continue
		}
		if got.Size != want.Size || got.Mode != want.Mode || got.Uid != want.Uid || got.Gid != want.Gid ||
			got.Typeflag != want.Typeflag || got.Linkname != want.Linkname || !got.ModTime.Equal(want.ModTime) ||
			!reflect.DeepEqual(got.PAXRecords, want.PAXRecords) {
			t.Errorf("%s changed on the way through:\n  got  %+v\n  want %+v", name, got, want)
		}
		if selectedSums[name] != sums[name] {
			t.Errorf("%s: contents changed", name)
		}
	}
	if selected["./fixture/sparse.bin"].Size != 9<<30 {
		t.Error("the sparse file lost its logical size")
	}
}

func TestWalkTarDamaged(t *testing.T) {
	data, end := buildTar(t, sampleEntries())
	all := func(rawEntry) bool { return true }

	// 資料中途被截斷。
	if _, err := walkTar(bytes.NewReader(data[:end-700]), io.Discard, all); !errors.Is(err, errTarHeader) {
		t.Errorf("truncated data: err = %v", err)
	}
	// 標頭被改動（檢查碼對不上）。
	broken := bytes.Clone(data)
	broken[5] ^= 0x01
	if _, err := walkTar(bytes.NewReader(broken), io.Discard, all); !errors.Is(err, errTarHeader) {
		t.Errorf("damaged header: err = %v", err)
	}
	// 根本不是 tar。
	if _, err := walkTar(strings.NewReader(strings.Repeat("not a tar archive ", 100)), io.Discard, all); !errors.Is(err, errTarHeader) {
		t.Errorf("junk: err = %v", err)
	}
	// 空的輸入、只有結尾標記的輸入都是「沒有項目」。
	for _, input := range [][]byte{nil, make([]byte, 2*tarBlock)} {
		if n, err := walkTar(bytes.NewReader(input), io.Discard, all); err != nil || n != 0 {
			t.Errorf("empty archive (%d bytes): %d entries, %v", len(input), n, err)
		}
	}
	// 不要的項目不會出現在輸出裡，out 是 nil 時只走訪。
	var out bytes.Buffer
	n, err := walkTar(bytes.NewReader(data), &out, func(rawEntry) bool { return false })
	if err != nil || out.Len() != 0 || n != int64(len(sampleEntries())) {
		t.Errorf("selecting nothing: %d entries, %d bytes written, %v", n, out.Len(), err)
	}
	if _, err := walkTar(bytes.NewReader(data), nil, all); err != nil {
		t.Errorf("walking without an output: %v", err)
	}

	// 延伸標頭的格式。
	// 每一筆開頭的數字是整筆的長度，包含數字本身、空白與結尾的換行。
	good := "27 path=./a/very/long/name\n16 size=1234567\n"
	if got, err := parsePax([]byte(good)); err != nil || got["path"] != "./a/very/long/name" || got["size"] != "1234567" {
		t.Errorf("parsePax: %v, %v", got, err)
	}
	for _, bad := range []string{"path=x\n", "9 path=x", "5 path=x\n", "999 path=x\n", "8 noeq x\n", "x path=y\n"} {
		if _, err := parsePax([]byte(bad)); err == nil {
			t.Errorf("parsePax(%q) should fail", bad)
		}
	}

	// 數字欄位：八進位文字，或 GNU 的二進位寫法。
	if n, err := tarNumber([]byte("0000644\x00")); err != nil || n != 0o644 {
		t.Errorf("octal: %d, %v", n, err)
	}
	if n, err := tarNumber([]byte{0x80, 0, 0, 0, 0, 0, 0, 2, 0x40, 0, 0, 0}); err != nil || n != 9<<30 {
		t.Errorf("binary: %d, %v", n, err)
	}
	if n, err := tarNumber([]byte("       \x00")); err != nil || n != 0 {
		t.Errorf("blank: %d, %v", n, err)
	}
	if _, err := tarNumber([]byte("12x4567\x00")); err == nil {
		t.Error("a field that is not a number should fail")
	}
}
