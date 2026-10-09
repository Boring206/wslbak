package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
)

type testEntry struct {
	name string
	kind byte
	body string
	link string
}

// buildTar 做出一個結構和 GNU tar 輸出相同的封存：項目、兩個全零區塊的結尾標記，
// 再補零到 10240 的倍數。回傳整個封存與結尾標記的起點。
func buildTar(t *testing.T, entries []testEntry) ([]byte, int64) {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.kind, Mode: 0o644, Size: int64(len(e.body)), Linkname: e.link, Format: tar.FormatPAX}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	end := int64(b.Len())
	b.Write(make([]byte, 2*tarBlock))
	if pad := b.Len() % 10240; pad != 0 {
		b.Write(make([]byte, 10240-pad))
	}
	return b.Bytes(), end
}

func sampleEntries() []testEntry {
	return []testEntry{
		{name: "./", kind: tar.TypeDir},
		{name: "./etc/", kind: tar.TypeDir},
		{name: "./etc/passwd", kind: tar.TypeReg, body: "root:x:0:0:root:/root:/bin/sh\n"},
		{name: "./etc/wsl.conf", kind: tar.TypeReg, body: "[boot]\nsystemd=true\n"},
		{name: "./etc/empty", kind: tar.TypeReg},
		{name: "./bin", kind: tar.TypeSymlink, link: "usr/bin"},
		{name: "./home/user/中文 檔名.txt", kind: tar.TypeReg, body: "內容"},
		{name: "./home/user/back\\slash", kind: tar.TypeReg, body: "x"},
		{name: "./big", kind: tar.TypeReg, body: string(bytes.Repeat([]byte{'a'}, sampleMaxSize+1))},
		// 最後一個檔案以全零的資料區塊結尾：結尾標記的位置不能靠「往回找零」來猜。
		{name: "./zeros", kind: tar.TypeReg, body: string(make([]byte, 3*tarBlock))},
	}
}

func TestScanTar(t *testing.T) {
	data, end := buildTar(t, sampleEntries())
	idx, err := scanTar(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if idx.Entries != int64(len(sampleEntries())) {
		t.Errorf("Entries = %d, want %d", idx.Entries, len(sampleEntries()))
	}
	if idx.EndOffset != end {
		t.Errorf("EndOffset = %d, want %d", idx.EndOffset, end)
	}
	if idx.Size != int64(len(data)) {
		t.Errorf("Size = %d, want %d", idx.Size, len(data))
	}
	if !idx.EtcIsDir {
		t.Error("EtcIsDir should be true")
	}
	got := map[string]fileSample{}
	for _, s := range idx.Samples {
		got[s.Path] = s
	}
	sum := sha256.Sum256([]byte("root:x:0:0:root:/root:/bin/sh\n"))
	if s := got["./etc/passwd"]; s.SHA256 != hex.EncodeToString(sum[:]) || s.Size != 30 {
		t.Errorf("./etc/passwd sample = %+v", s)
	}
	if _, ok := got["./home/user/中文 檔名.txt"]; !ok {
		t.Error("a file with a Chinese name and a space should be sampled")
	}
	if _, ok := got["./zeros"]; !ok {
		t.Error("./zeros should be sampled")
	}
	// 不取樣的：會被覆寫的設定檔、空檔、太大的檔、檔名要跳脫的檔、不是一般檔案的項目。
	for _, name := range []string{"./etc/wsl.conf", "./etc/empty", "./big", "./home/user/back\\slash", "./bin", "./etc/"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s should not be sampled", name)
		}
	}
}

func TestScanTarEtcNotADirectory(t *testing.T) {
	data, _ := buildTar(t, []testEntry{
		{name: "./", kind: tar.TypeDir},
		{name: "./etc", kind: tar.TypeSymlink, link: "private/etc"},
	})
	idx, err := scanTar(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if idx.EtcIsDir {
		t.Error("EtcIsDir should be false when ./etc is a symlink")
	}
}

func TestScanTarBadInput(t *testing.T) {
	data, end := buildTar(t, sampleEntries())

	// 結尾標記之後有不是零的資料。
	dirty := bytes.Clone(data)
	dirty[len(dirty)-1] = 1
	idx, err := scanTar(bytes.NewReader(dirty))
	if !errors.Is(err, errTrailingData) {
		t.Errorf("trailing data: err = %v", err)
	}
	if idx.Size != int64(len(dirty)) || idx.EndOffset != 0 || len(idx.Samples) != 0 {
		t.Errorf("trailing data: only Size should be reported, got %+v", idx)
	}

	// 從中間被截斷：要回報錯誤，而且仍然把資料讀完（Size 等於實際長度）。
	cut := data[:end-700]
	idx, err = scanTar(bytes.NewReader(cut))
	if err == nil {
		t.Error("truncated archive: expected an error")
	}
	if idx.Size != int64(len(cut)) {
		t.Errorf("truncated archive: Size = %d, want %d", idx.Size, len(cut))
	}

	// 根本不是 tar。
	junk := bytes.Repeat([]byte("not a tar archive "), 100)
	idx, err = scanTar(bytes.NewReader(junk))
	if err == nil {
		t.Error("junk: expected an error")
	}
	if idx.Size != int64(len(junk)) {
		t.Errorf("junk: Size = %d, want %d", idx.Size, len(junk))
	}
}

func tarNames(t *testing.T, data []byte) []string {
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

func TestOverrideReader(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []testEntry
	}{
		{"wsl.conf is a regular file", sampleEntries()},
		{"wsl.conf is absent", []testEntry{{name: "./", kind: tar.TypeDir}, {name: "./etc/", kind: tar.TypeDir}}},
		{"wsl.conf is a symlink", []testEntry{{name: "./etc/", kind: tar.TypeDir}, {name: "./etc/wsl.conf", kind: tar.TypeSymlink, link: "/etc/real.conf"}}},
	} {
		data, end := buildTar(t, tc.entries)
		out, err := io.ReadAll(newOverrideReader(bytes.NewReader(data), end, buildOverrideTail()))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		// 原本的資料一個位元組都沒有動。
		if !bytes.Equal(out[:end], data[:end]) {
			t.Errorf("%s: the original entries were altered", tc.name)
		}
		names := tarNames(t, out)
		want := len(tc.entries) + 2
		if len(names) != want || names[want-2] != "./etc/wsl.conf" || names[want-1] != "./etc/wsl-distribution.conf" {
			t.Errorf("%s: entries after override = %q", tc.name, names)
		}
		content, found, err := lastWSLConf(bytes.NewReader(out))
		if err != nil || !found || content != inertWSLConf {
			t.Errorf("%s: last wsl.conf found=%v err=%v content=%q", tc.name, found, err, content)
		}
		if len(out)%tarBlock != 0 {
			t.Errorf("%s: output length %d is not a multiple of the block size", tc.name, len(out))
		}
	}
}

func TestOverrideReaderWrongOffset(t *testing.T) {
	data, end := buildTar(t, []testEntry{
		{name: "./etc/", kind: tar.TypeDir},
		{name: "./etc/passwd", kind: tar.TypeReg, body: "root:x:0:0::/root:/bin/sh\n"},
	})
	// 位置記得太前面：後面還有真正的資料，不能當成結尾丟掉。
	if _, err := io.ReadAll(newOverrideReader(bytes.NewReader(data), end-tarBlock, buildOverrideTail())); !errors.Is(err, errTrailingData) {
		t.Errorf("offset too small: err = %v", err)
	}
	// 封存比記錄的位置還短（檔案被截斷）。
	if _, err := io.ReadAll(newOverrideReader(bytes.NewReader(data[:end-10]), end, buildOverrideTail())); !errors.Is(err, errShortArchive) {
		t.Errorf("short archive: err = %v", err)
	}
	// 沒有覆寫的原始封存，最後一個 wsl.conf 不是我們的。
	original, _ := buildTar(t, sampleEntries())
	if content, found, err := lastWSLConf(bytes.NewReader(original)); err != nil || !found || content == inertWSLConf {
		t.Errorf("original archive: found=%v err=%v content=%q", found, err, content)
	}
}

// testdata/gnu.tar 是 backup.sh 用正式的選項、真的 GNU tar 產生的（見 scripts/mkfixtures.sh）。
// 這裡確認掃描器看得懂 GNU tar 實際寫出來的東西，而不只是 Go 自己寫的封存。
func TestScanRealGNUTar(t *testing.T) {
	data, err := os.ReadFile("testdata/gnu.tar")
	if err != nil {
		t.Skip("testdata/gnu.tar is not available next to the test binary")
	}
	count, _ := os.ReadFile("testdata/gnu.entries")
	entries, _ := strconv.ParseInt(strings.TrimSpace(string(count)), 10, 64)

	idx, err := scanTar(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if idx.Entries != entries || entries == 0 {
		t.Errorf("Entries = %d, GNU tar itself lists %d", idx.Entries, entries)
	}
	if idx.Size != int64(len(data)) || !idx.EtcIsDir {
		t.Errorf("Size = %d (file is %d bytes), EtcIsDir = %v", idx.Size, len(data), idx.EtcIsDir)
	}
	if idx.EndOffset <= 0 || idx.EndOffset%tarBlock != 0 || !isZero(data[idx.EndOffset:]) {
		t.Errorf("EndOffset = %d does not point at the end-of-archive marker", idx.EndOffset)
	}
	if int64(len(data))-idx.EndOffset < 2*tarBlock {
		t.Errorf("fewer than two blocks after EndOffset = %d", idx.EndOffset)
	}
	if idx.ACLs != 1 {
		t.Errorf("ACLs = %d, the fixture has exactly one file with an ACL", idx.ACLs)
	}
	got := map[string]fileSample{}
	for _, s := range idx.Samples {
		got[s.Path] = s
	}
	sum := sha256.Sum256([]byte("中文內容\n"))
	if s := got["./fixture/中文檔名 with space.txt"]; s.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("sample of the file with a Chinese name = %+v", s)
	}
	for _, name := range []string{"./etc/passwd", "./fixture/plain.txt", "./fixture/cap-binary", "./fixture/hard1"} {
		if _, ok := got[name]; !ok {
			t.Errorf("%s should be sampled", name)
		}
	}
	// 9 GiB 的稀疏檔太大不取樣；硬連結的第二個名字、符號連結、fifo、裝置都不是一般檔案。
	for _, name := range []string{"./fixture/sparse.bin", "./fixture/hard2", "./fixture/symlink", "./fixture/fifo", "./fixture/devnull", "./etc/wsl.conf"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s should not be sampled", name)
		}
	}

	// Go 的解析器要看得出稀疏檔的邏輯大小、延伸屬性、capability 與超過 255 個字元的路徑。
	seen := map[string]*tar.Header{}
	longest := 0
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		seen[h.Name] = h
		longest = max(longest, len(h.Name))
	}
	if h := seen["./fixture/sparse.bin"]; h == nil || h.Size != 9<<30 {
		t.Errorf("sparse.bin: %+v", h)
	}
	if h := seen["./fixture/plain.txt"]; h == nil || h.PAXRecords["SCHILY.xattr.user.wslbak"] != "hello" || h.Uid != 1234 || h.Gid != 5678 {
		t.Errorf("plain.txt lost its attribute or owner: %+v", h)
	}
	if h := seen["./fixture/cap-binary"]; h == nil || h.PAXRecords["SCHILY.xattr.security.capability"] == "" {
		t.Errorf("cap-binary lost its capability: %+v", h)
	}
	if h := seen["./fixture/acl-file"]; h == nil || !strings.Contains(h.PAXRecords["SCHILY.acl.access"], "user:1234:rw-") {
		t.Errorf("acl-file lost its ACL: %+v", h)
	}
	if h := seen["./fixture/hard2"]; h == nil || h.Typeflag != tar.TypeLink || h.Linkname != "./fixture/hard1" {
		t.Errorf("hard2 is not a hard link to hard1: %+v", h)
	}
	if longest < 300 {
		t.Errorf("the longest path is %d characters; the fixture should contain one well over 255", longest)
	}
	if _, ok := seen["./tmp/scratch"]; ok {
		t.Error("./tmp/scratch should have been excluded")
	}
	if _, ok := seen["./tmp/"]; !ok {
		t.Error("the excluded folder itself (./tmp/) should still be in the archive")
	}

	// 接上覆寫項目之後：原本的項目都在，最後留下的 wsl.conf 是我們的。
	out, err := io.ReadAll(newOverrideReader(bytes.NewReader(data), idx.EndOffset, buildOverrideTail()))
	if err != nil {
		t.Fatal(err)
	}
	if names := tarNames(t, out); int64(len(names)) != entries+2 {
		t.Errorf("%d entries after override, want %d", len(names), entries+2)
	}
	if content, found, err := lastWSLConf(bytes.NewReader(out)); err != nil || !found || content != inertWSLConf {
		t.Errorf("after override: found=%v err=%v content=%q", found, err, content)
	}
	if content, _, _ := lastWSLConf(bytes.NewReader(data)); !strings.Contains(content, "systemd=true") {
		t.Errorf("the fixture's own wsl.conf should enable systemd, got %q", content)
	}
}
