package main

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"testing"
)

// snapDir 是測試用的清單裡的一個目錄：名稱、inode，以及底下的名稱（開頭是標記）。
type snapDir struct {
	name     string
	ino      uint64
	children []string
}

// buildSnapshot 做出一份和 GNU tar 寫的格式相同的快照檔。
func buildSnapshot(dirs ...snapDir) []byte {
	var b bytes.Buffer
	b.WriteString("GNU tar-1.35-2\n1791690000\x00123456789\x00")
	for _, d := range dirs {
		fmt.Fprintf(&b, "0\x001791680000\x000\x002112\x00%d\x00%s\x00", d.ino, d.name)
		for _, c := range d.children {
			b.WriteString(c + "\x00")
		}
		b.WriteString("\x00\x00")
	}
	return b.Bytes()
}

func mustParse(t *testing.T, data []byte) *listing {
	t.Helper()
	l, err := parseSnapshot(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestParseSnapshot(t *testing.T) {
	data := buildSnapshot(
		snapDir{".", 2, []string{"Detc", "Yvmlinuz", "Ninit", "Dhome"}},
		snapDir{"./etc", 10, []string{"Ypasswd", "Yhosts", "Ya\nb"}},
		snapDir{"./home", 11, nil},
	)
	l := mustParse(t, data)
	if l.bytes != int64(len(data)) {
		t.Errorf("bytes = %d, want %d", l.bytes, len(data))
	}
	if l.header != "GNU tar-1.35-2" || len(l.dirs) != 3 || l.entries() != 7 {
		t.Errorf("header %q, %d directories, %d names", l.header, len(l.dirs), l.entries())
	}
	if err := l.validate(); err != nil {
		t.Errorf("validate: %v", err)
	}
	cases := []struct {
		name string
		mark byte
	}{
		{"./etc/passwd", markYes}, {"./etc/a\nb", markYes}, {"./init", markNo}, {"./etc", markDir}, {"./vmlinuz", markYes},
		{"./etc/shadow", 0}, {"./nope/x", 0}, {".", 0},
	}
	for _, c := range cases {
		dir, i := l.lookup(c.name)
		var got byte
		if dir != nil && i >= 0 {
			got = dir.mark(i)
		}
		if got != c.mark {
			t.Errorf("lookup(%q) = %q, want %q", c.name, got, c.mark)
		}
	}
	if l.dirs["./etc"].ino != 10 || l.dirs["./home"].count() != 0 {
		t.Errorf("etc inode %d, home has %d names", l.dirs["./etc"].ino, l.dirs["./home"].count())
	}
	// 名稱不照順序給也找得到。
	if dir := l.dirs["."]; dir.name(0) != "etc" || dir.name(3) != "vmlinuz" {
		t.Errorf("names are not sorted: %q … %q", dir.name(0), dir.name(3))
	}
}

func TestParseSnapshotRejects(t *testing.T) {
	good := buildSnapshot(snapDir{".", 2, []string{"Yfile"}})
	cases := map[string][]byte{
		"empty":                      nil,
		"not a snapshot":             []byte("hello\nworld"),
		"another format":             bytes.Replace(good, []byte("-2\n"), []byte("-1\n"), 1),
		"cut in the middle":          good[:len(good)-4],
		"a list that is not closed":  good[:len(good)-1],
		"an unknown mark":            bytes.Replace(good, []byte("Yfile"), []byte("Xfile"), 1),
		"a name with a slash":        bytes.Replace(good, []byte("Yfile"), []byte("Ya/b"), 1),
		"a directory listed twice":   buildSnapshot(snapDir{".", 2, nil}, snapDir{".", 3, nil}),
		"an inode that is no number": bytes.Replace(good, []byte("\x002112\x002\x00"), []byte("\x002112\x00x\x00"), 1),
	}
	for label, data := range cases {
		if _, err := parseSnapshot(bytes.NewReader(data)); !errors.Is(err, errBadSnapshot) {
			t.Errorf("%s: err = %v, want errBadSnapshot", label, err)
		}
	}
	// 解析失敗也要把來源讀完，寫的那一端才不會卡住。
	r := bytes.NewReader(append([]byte("junk\n"), make([]byte, 1<<20)...))
	if l, _ := parseSnapshot(r); r.Len() != 0 || l.bytes != int64(r.Size()) {
		t.Errorf("after a failed parse %d bytes are unread, counted %d of %d", r.Len(), l.bytes, r.Size())
	}
}

func TestValidateListing(t *testing.T) {
	cases := []struct {
		label string
		dirs  []snapDir
		bad   string
	}{
		{"a tree", []snapDir{{".", 2, []string{"Da", "Yf"}}, {"./a", 3, []string{"Db"}}, {"./a/b", 4, nil}}, ""},
		{"no top", []snapDir{{"./a", 3, nil}}, "top directory"},
		// 同一個目錄從兩條路徑都走得到：GNU tar 1.26 到 1.35 寫出來的就是這個樣子。
		{"one directory at two paths", []snapDir{{".", 2, []string{"Da", "Dalias"}}, {"./a", 3, []string{"Yf"}}, {"./alias", 3, []string{"Yf"}}}, "same directory"},
		{"a directory without a record", []snapDir{{".", 2, []string{"Da"}}}, "no record"},
		{"a record nobody lists", []snapDir{{".", 2, nil}, {"./a", 3, nil}}, "not listed in its parent"},
		{"a record listed as a file", []snapDir{{".", 2, []string{"Ya"}}, {"./a", 3, nil}}, "not listed in its parent"},
		{"a name twice", []snapDir{{".", 2, []string{"Yf", "Yf"}}}, "appears twice"},
	}
	for _, c := range cases {
		err := mustParse(t, buildSnapshot(c.dirs...)).validate()
		switch {
		case c.bad == "" && err != nil:
			t.Errorf("%s: %v", c.label, err)
		case c.bad != "" && (err == nil || !strings.Contains(err.Error(), c.bad)):
			t.Errorf("%s: err = %v, want something about %q", c.label, err, c.bad)
		}
	}
}

func TestUnquoteTarName(t *testing.T) {
	cases := map[string]string{
		`./plain`:                    "./plain",
		`./with space`:               "./with space",
		`./new\nline`:                "./new\nline",
		`./back\\slash`:              `./back\slash`,
		`./colon\:name`:              "./colon:name",
		`./tab\there`:                "./tab\there",
		`./\344\270\255\346\226\207`: "./中文",
		`./bad\377utf`:               "./bad\xffutf",
	}
	for in, want := range cases {
		if got, ok := unquoteTarName(in); !ok || got != want {
			t.Errorf("unquoteTarName(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{`./ends\`, `./odd\q`, `./short\34`} {
		if got, ok := unquoteTarName(in); ok {
			t.Errorf("unquoteTarName(%q) = %q, want it refused", in, got)
		}
	}
}

func TestNoteSocket(t *testing.T) {
	l := mustParse(t, buildSnapshot(snapDir{".", 2, []string{"Drun"}}, snapDir{"./run", 3, []string{"Ymy sock", "Yother"}}))
	if !l.noteSocket("./run/my sock") {
		t.Error("the socket was not found in the listing")
	}
	if l.noteSocket("./run/missing") || l.noteSocket(`./run/odd\q`) {
		t.Error("a name that is not in the listing was accepted")
	}
	dir, i := l.lookup("./run/my sock")
	if dir.state[i]&isSocketBit == 0 {
		t.Error("the socket is not marked")
	}
	if _, j := l.lookup("./run/other"); dir.state[j] != 0 {
		t.Error("another name was marked")
	}
}

// tarMembers 讀出一個 tar 裡每個項目的名稱（去掉結尾的斜線）與類型。
func tarMembers(t *testing.T, data []byte) map[string]byte {
	t.Helper()
	out := map[string]byte{}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := out[cleanMember(h.Name)]; dup {
			t.Errorf("%s appears twice", h.Name)
		}
		out[cleanMember(h.Name)] = h.Typeflag
	}
}

// testdata/gnu.snar 是真的 GNU tar 對著 testdata/gnu.tar 那棵樹寫出來的清單（見 scripts/mkfixtures.sh）。
// 兩者要講同一批東西：清單裡的目錄和標成 Y 的名稱，就是封存裡的項目。
func TestRealSnapshot(t *testing.T) {
	snap, err := os.ReadFile("testdata/gnu.snar")
	if err != nil {
		t.Skip("testdata/gnu.snar is not available next to the test binary")
	}
	archive, err := os.ReadFile("testdata/gnu.tar")
	if err != nil {
		t.Fatal(err)
	}
	l := mustParse(t, snap)
	if l.bytes != int64(len(snap)) {
		t.Errorf("bytes = %d, want %d", l.bytes, len(snap))
	}
	if err := l.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	var listed []string
	excluded := 0
	for name, dir := range l.dirs {
		listed = append(listed, name)
		for i := 0; i < dir.count(); i++ {
			switch dir.mark(i) {
			case markYes:
				listed = append(listed, name+"/"+dir.name(i))
			case markNo:
				excluded++
			}
		}
	}
	var dumped []string
	for name, kind := range tarMembers(t, archive) {
		dumped = append(dumped, name)
		if _, isDir := l.dirs[name]; isDir != (kind == tar.TypeDir) {
			t.Errorf("%s: a directory in one, not in the other", name)
		}
	}
	sort.Strings(listed)
	sort.Strings(dumped)
	if strings.Join(listed, "\n") != strings.Join(dumped, "\n") {
		t.Errorf("the listing and the archive disagree:\nlisting: %q\narchive: %q", listed, dumped)
	}
	// ./tmp/* 被排除了，清單裡那個檔案要標成 N。
	if excluded != 1 {
		t.Errorf("%d names are marked as not dumped, want 1 (the file under ./tmp)", excluded)
	}
}
