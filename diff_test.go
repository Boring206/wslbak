package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/gzip"
)

func TestSinceFor(t *testing.T) {
	if got := sinceFor(clockInfo{Distro: 10000, Windows: 10900}); got != 9700 {
		t.Errorf("distro behind: since = %d, want 9700", got)
	}
	// distro 的時鐘超前時用 Windows 的：超前的時鐘之後會被校正回來。
	if got := sinceFor(clockInfo{Distro: 20000, Windows: 10900}); got != 10600 {
		t.Errorf("distro ahead: since = %d, want 10600", got)
	}
}

// mountRows 是一個普通的 WSL distro 的掛載表（節錄自真的 /proc/self/mountinfo）。
func mountRows() []mountRow {
	return []mountRow{
		{"343", "347", "0:34", "/", "/mnt/wsl", "tmpfs"},
		{"347", "332", "8:64", "/", "/", "ext4"},
		{"348", "347", "0:84", "/", "/mnt/wslg", "tmpfs"},
		// 根磁碟在別的檔案系統底下又掛了一次：tar 走不到那裡。
		{"349", "348", "8:64", "/", "/mnt/wslg/distro", "ext4"},
		{"417", "347", "0:2", "/init", "/init", "rootfs"},
		{"422", "347", "0:109", "/", "/run", "tmpfs"},
		{"423", "422", "0:110", "/", "/run/lock", "tmpfs"},
		{"439", "347", "0:115", "/", "/mnt/c", "9p"},
		{"438", "347", "0:84", "/.X11-unix", "/tmp/.X11-unix", "tmpfs"},
	}
}

func TestReadMounts(t *testing.T) {
	f := readMounts(mountRows())
	if f.RootFS != "ext4" || len(f.Aliases) != 0 {
		t.Errorf("root %q, aliases %v", f.RootFS, f.Aliases)
	}
	for _, want := range []string{"./init", "./run", "./mnt/c", "./mnt/wslg", "./tmp/.X11-unix"} {
		if !f.Points[want] {
			t.Errorf("%s is not among the mount points", want)
		}
	}
	for _, not := range []string{"./run/lock", "./mnt/wslg/distro", "."} {
		if f.Points[not] {
			t.Errorf("%s is a mount point tar cannot reach, or the root itself", not)
		}
	}

	// 把根磁碟上的一個資料夾掛到另一個位置：同一批檔案出現兩次。
	rows := append(mountRows(), mountRow{"500", "347", "8:64", "/home/me/data", `/srv/my\040data`, "ext4"})
	if f := readMounts(rows); len(f.Aliases) != 1 || f.Aliases[0] != "/srv/my data" {
		t.Errorf("aliases = %q, want /srv/my data", f.Aliases)
	}
	// Docker 把它的資料夾掛在自己上面，那不會多出一條路徑。
	rows = append(mountRows(), mountRow{"501", "347", "8:64", "/var/lib/docker", "/var/lib/docker", "ext4"},
		// 掛在那個掛載點底下的也算走得到。
		mountRow{"502", "501", "8:64", "/data", "/var/lib/docker/extra", "ext4"})
	if f := readMounts(rows); len(f.Aliases) != 1 || f.Aliases[0] != "/var/lib/docker/extra" {
		t.Errorf("aliases = %q, want only /var/lib/docker/extra", f.Aliases)
	}
	if f := readMounts(nil); f.RootFS != "" {
		t.Errorf("no rows: root %q", f.RootFS)
	}
}

func TestFactsFromProto(t *testing.T) {
	f := factsFromProto([][]string{
		{"tar-version", "tar (GNU tar) 1.35"}, {"clock", "1791690000"},
		{"mount", "347", "332", "8:64", "/", "/", "ext4"}, {"mount", "417", "347", "0:2", "/init", "/init", "rootfs"},
		{"mount", "too", "short"}, {"skipped-mount", "/data", "ext4"}, {"done"},
	})
	if f.Clock != 1791690000 || f.TarVersion != "tar (GNU tar) 1.35" || f.Mounts.RootFS != "ext4" || !f.Mounts.Points["./init"] {
		t.Errorf("facts = %+v", f)
	}
}

// 一份可以當基準的完整備份，和依據它的差異。
func baseBackup(id string) *manifest {
	created, _ := time.Parse(idLayout, id)
	return &manifest{ID: id, Created: created, Size: 1000, SHA256: "aa", Excludes: []string{"./init"},
		Since: created.Unix() - 300, Clock: &clockInfo{Distro: created.Unix(), Windows: created.Unix()},
		TarVersion: "tar (GNU tar) 1.35", DirsSHA256: "dd", Index: &tarIndex{Entries: 10},
		Verify: &verifyResult{OK: true, Mode: verifyModeRestore}}
}

func diffBackup(id string, base *manifest, size int64) *manifest {
	created, _ := time.Parse(idLayout, id)
	return &manifest{ID: id, Kind: kindDiff, Created: created, Size: size, Base: base.ID, base: base,
		Verify: &verifyResult{OK: true, Mode: verifyModeCheck, At: created}}
}

func TestChooseKind(t *testing.T) {
	now := time.Date(2026, 10, 20, 3, 0, 0, 0, time.UTC)
	base := func() *manifest { return baseBackup("20261015T030000Z") }
	query := func(change func(q *kindQuery)) kindQuery {
		q := kindQuery{Mode: modeDiff, FullEvery: 30, Verify: verifyRestore, Excludes: []string{"./init"}, Now: now,
			Backups: []*manifest{base()}, ArchiveSize: func(m *manifest) int64 { return m.Size }}
		if change != nil {
			change(&q)
		}
		return q
	}
	old := baseBackup("20261001T030000Z")
	cases := []struct {
		label string
		q     kindQuery
		want  string // 空的表示做差異
	}{
		{"a differential on the full backup", query(nil), ""},
		{"full mode", query(func(q *kindQuery) { q.Mode = "" }), "full"},
		{"--full", query(func(q *kindQuery) { q.ForceFull = true }), whyAsked},
		{"no backups yet", query(func(q *kindQuery) { q.Backups = nil }), whyFirst},
		{"the full backup was made in full mode", query(func(q *kindQuery) { q.Backups[0].DirsSHA256 = "" }), whyFirst},
		{"the full backup has no index", query(func(q *kindQuery) { q.Backups[0].Index = nil }), whyFirst},
		{"the full backup failed its test restore", query(func(q *kindQuery) { q.Backups[0].Verify.OK = false }), whyBaseUnverified},
		{"…which does not matter with verification off", query(func(q *kindQuery) { q.Backups[0].Verify = nil; q.Verify = verifyNone }), ""},
		{"the archive is gone", query(func(q *kindQuery) { q.ArchiveSize = func(*manifest) int64 { return -1 } }), whyBaseDamaged},
		{"the archive has another size", query(func(q *kindQuery) { q.ArchiveSize = func(*manifest) int64 { return 999 } }), whyBaseDamaged},
		{"another exclude list", query(func(q *kindQuery) { q.Excludes = []string{"./init", "./var/cache/*"} }), whySettings},
		{"the clock is before the full backup", query(func(q *kindQuery) { q.Now = time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC) }), whyClock},
		{"the full backup is too old", query(func(q *kindQuery) { q.FullEvery = 5 }), whyAge},
		{"one day short of too old", query(func(q *kindQuery) { q.FullEvery = 6 }), ""},
		{"no limit given means thirty days", query(func(q *kindQuery) { q.FullEvery = 0; q.Now = now.AddDate(0, 0, 24) }), ""},
		{"thirty days are up", query(func(q *kindQuery) { q.FullEvery = 0; q.Now = now.AddDate(0, 0, 25) }), whyAge},
		{"the last differential grew past half", query(func(q *kindQuery) {
			q.Backups = append(q.Backups, diffBackup("20261016T030000Z", q.Backups[0], 100), diffBackup("20261017T030000Z", q.Backups[0], 501))
		}), whySize},
		{"an earlier large differential does not count", query(func(q *kindQuery) {
			q.Backups = append(q.Backups, diffBackup("20261016T030000Z", q.Backups[0], 900), diffBackup("20261017T030000Z", q.Backups[0], 500))
		}), ""},
		{"the last differential failed its check", query(func(q *kindQuery) {
			d := diffBackup("20261016T030000Z", q.Backups[0], 10)
			d.Verify = &verifyResult{OK: false, Mode: verifyModeCheck, Reason: reasonChanged}
			q.Backups = append(q.Backups, d)
		}), whyFailed},
		{"a check that was skipped is not a failure", query(func(q *kindQuery) {
			d := diffBackup("20261016T030000Z", q.Backups[0], 10)
			d.Verify = &verifyResult{OK: false, Mode: verifyModeSkipped, Reason: reasonNoSpace}
			q.Backups = append(q.Backups, d)
		}), ""},
		{"differentials of an older full backup do not count", query(func(q *kindQuery) {
			q.Backups = []*manifest{old, diffBackup("20261002T030000Z", old, 990), q.Backups[0]}
		}), ""},
	}
	for _, c := range cases {
		got, why := chooseKind(c.q)
		switch {
		case c.want == "" && (got == nil || got.ID != "20261015T030000Z"):
			t.Errorf("%s: no differential (%q), want one on the newest full backup", c.label, why)
		case c.want == "full" && (got != nil || why != ""):
			t.Errorf("%s: base %v, why %q; want a plain full backup", c.label, got, why)
		case c.want != "" && c.want != "full" && (got != nil || why != c.want):
			t.Errorf("%s: base %v, why %q; want %q", c.label, got, why, c.want)
		}
	}
}

func TestEnvironmentProblem(t *testing.T) {
	base := baseBackup("20261015T030000Z")
	good := func() distroFacts {
		return distroFacts{Clock: base.Clock.Distro + 86400, TarVersion: base.TarVersion, Mounts: readMounts(mountRows())}
	}
	cases := []struct {
		label  string
		change func(f *distroFacts)
		want   string
	}{
		{"nothing changed", func(*distroFacts) {}, ""},
		{"another tar", func(f *distroFacts) { f.TarVersion = "tar (GNU tar) 1.36" }, whySettings},
		{"tar did not say its version", func(f *distroFacts) { f.TarVersion = "" }, whySettings},
		{"the clock went back", func(f *distroFacts) { f.Clock = base.Clock.Distro - 10 }, whyClock},
		{"the clock stands still", func(f *distroFacts) { f.Clock = base.Clock.Distro }, whyClock},
		{"btrfs", func(f *distroFacts) { f.Mounts.RootFS = "btrfs" }, whyFilesystem},
		{"a folder mounted at a second place", func(f *distroFacts) { f.Mounts.Aliases = []string{"/srv/data"} }, whyAlias},
	}
	for _, c := range cases {
		f := good()
		c.change(&f)
		if got := environmentProblem(base, f); got != c.want {
			t.Errorf("%s: %q, want %q", c.label, got, c.want)
		}
	}
}

func TestChooseVerify(t *testing.T) {
	base := baseBackup("20261015T030000Z")
	at := func(day int) time.Time { return time.Date(2026, 10, day, 3, 0, 0, 0, time.UTC) }
	restored := func(id string, day int) *manifest {
		d := diffBackup(id, base, 10)
		d.Verify = &verifyResult{OK: true, Mode: verifyModeRestore, At: at(day)}
		return d
	}
	other := baseBackup("20261001T030000Z")
	cases := []struct {
		label  string
		m      *manifest
		others []*manifest
		now    time.Time
		want   string
	}{
		{"a full backup is always test-restored", base, nil, at(15), verifyModeRestore},
		{"the first differential of a full backup", diffBackup("20261016T030000Z", base, 10), []*manifest{base}, at(16), verifyModeRestore},
		{"the next day", diffBackup("20261017T030000Z", base, 10), []*manifest{base, restored("20261016T030000Z", 16)}, at(17), verifyModeCheck},
		{"six days later", diffBackup("20261022T030000Z", base, 10), []*manifest{base, restored("20261016T030000Z", 16)}, at(22), verifyModeCheck},
		{"seven days later", diffBackup("20261023T030000Z", base, 10), []*manifest{base, restored("20261016T030000Z", 16)}, at(23), verifyModeRestore},
		{"checks in between do not reset the week", diffBackup("20261023T030000Z", base, 10),
			[]*manifest{base, restored("20261016T030000Z", 16), diffBackup("20261020T030000Z", base, 10)}, at(23), verifyModeRestore},
		{"a test restore under another full backup does not count", diffBackup("20261016T030000Z", base, 10),
			[]*manifest{other, func() *manifest { d := restored("20261002T030000Z", 15); d.Base, d.base = other.ID, other; return d }(), base}, at(16), verifyModeRestore},
		{"its own earlier result does not count", restored("20261016T030000Z", 16), []*manifest{base, restored("20261016T030000Z", 16)}, at(17), verifyModeRestore},
	}
	for _, c := range cases {
		if got := chooseVerify(c.m, c.others, c.now); got != c.want {
			t.Errorf("%s: %s, want %s", c.label, got, c.want)
		}
	}
}

func TestPackBits(t *testing.T) {
	bits := make([]byte, 250000)
	for _, n := range []int64{0, 7, 8, 1999999} {
		bits[n/8] |= 1 << uint(n%8)
	}
	text := packBits(bits)
	if len(text) > 2000 {
		t.Errorf("two million entries with four set take %d characters", len(text))
	}
	back, err := unpackBits(text)
	if err != nil || !bytes.Equal(back, bits) {
		t.Fatalf("round trip failed: %v", err)
	}
	if countBits(back) != 4 || !bitSet(back, 7) || !bitSet(back, 1999999) || bitSet(back, 1) || bitSet(back, 2000000) || bitSet(back, -1) {
		t.Error("bits are not where they were put")
	}
	for _, bad := range []string{"not base64!", "aGVsbG8="} {
		if _, err := unpackBits(bad); err == nil {
			t.Errorf("unpackBits(%q) succeeded", bad)
		}
	}
}

// 打包前後的兩份清單：./a 沒變，./b 在途中被換成另一個目錄，./gone 消失了，./new 是途中才出現的，
// 還有一個名稱不是 UTF-8 的目錄。
func twoListings(t *testing.T) (before, after *listing, dumped map[string]int64) {
	before = mustParse(t, buildSnapshot(snapDir{".", 2, []string{"Da", "Db", "Dgone", "Dbad\xffname"}},
		snapDir{"./a", 3, nil}, snapDir{"./b", 4, nil}, snapDir{"./gone", 5, nil}, snapDir{"./bad\xffname", 12, nil}))
	after = mustParse(t, buildSnapshot(snapDir{".", 2, []string{"Da", "Db", "Dnew", "Dbad\xffname"}},
		snapDir{"./a", 3, nil}, snapDir{"./b", 44, nil}, snapDir{"./new", 6, nil}, snapDir{"./bad\xffname", 12, nil}))
	return before, after, map[string]int64{".": 111, "./a": 222, "./b": 3, "./gone": 4, "./new": 5, "./only-dumped": 6, "./bad\xffname": 333}
}

func TestStableDirs(t *testing.T) {
	before, after, dumped := twoListings(t)
	got, others := stableDirs(before, after, dumped)
	if len(got) != 3 || got["."] != (dirIdentity{2, 111}) || got["./a"] != (dirIdentity{3, 222}) || got["./bad\xffname"] != (dirIdentity{12, 333}) {
		t.Errorf("stable directories = %v; want ., ./a and the oddly named one (./b was replaced, the others came or went)", got)
	}
	// 不能拿來認路徑的目錄，inode 仍然要留著：之後看到它們時，知道它們不是新建的。
	if fmt.Sprint(others) != "[4 5 6 44]" {
		t.Errorf("inodes of the other directories = %v, want [4 5 6 44]", others)
	}
}

func TestDirsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "20261015T030000Z.dirs.gz")
	before, after, dumped := twoListings(t)
	sum, err := writeDirs(path, "20261015T030000Z", before, after, dumped)
	if err != nil {
		t.Fatal(err)
	}
	got, others, err := readDirs(path, sum)
	if err != nil || len(got) != 3 || got["./a"] != (dirIdentity{3, 222}) || got["./bad\xffname"] != (dirIdentity{12, 333}) || fmt.Sprint(others) != "[4 5 6 44]" {
		t.Errorf("read back %v, %v, %v", got, others, err)
	}
	if _, _, err := readDirs(path, strings.Repeat("0", 64)); !errors.Is(err, errBadDirs) {
		t.Errorf("another checksum: err = %v", err)
	}
	if _, _, err := readDirs(path, ""); !errors.Is(err, errBadDirs) {
		t.Errorf("no checksum recorded: err = %v", err)
	}
	if _, _, err := readDirs(filepath.Join(dir, "missing"), sum); !errors.Is(err, errBadDirs) {
		t.Errorf("missing file: err = %v", err)
	}
}

// world 是 explainer 的一個測試情境：完整備份當時的樣子，和現在的樣子。
type world struct {
	// base 是完整備份的項目（依封存裡的順序）；baseDirs 是當時每個目錄的 inode。
	base     []indexEntry
	baseDirs map[string]dirIdentity
	// now 是現在的清單；diff 是這一趟差異收到的項目。
	now        []snapDir
	diff       []indexEntry
	mounts     map[string]bool
	baseMounts []string
	// baseOthers 是完整備份當時存在、卻沒有被記成某個路徑的目錄的 inode。
	baseOthers []uint64
	socket     string
	// gone 是 tar 在打包途中說「已經不見了」的名稱（tar 的寫法）。
	gone []string
}

const (
	testSince     = 5000 // 基準時間
	testListStart = 9000 // 清單開始的時間
)

func entry(kind, name string) indexEntry { return indexEntry{Path: name, Type: kind} }
func link(name, target string) indexEntry {
	return indexEntry{Path: name, Type: typeHardlink, Link: target}
}

// dirAt：一個目錄項目，變更時間是 ctime（秒）。
func dirAt(name string, ctime int64) indexEntry {
	return indexEntry{Path: name, Type: typeDir, ctime: ctime * int64(time.Second)}
}

// explain 跑完整個比對，回傳留下來的完整備份項目（名稱）與原因。
func (w world) explain(t *testing.T) (kept []string, why string) {
	t.Helper()
	list := mustParse(t, buildSnapshot(w.now...))
	if err := list.validate(); err != nil {
		t.Fatalf("the test's listing is not a tree: %v", err)
	}
	if w.socket != "" && !list.noteSocket(w.socket) {
		t.Fatalf("socket %s is not in the listing", w.socket)
	}
	x := newExplainer(list, w.baseDirs, w.mounts, testSince, testListStart)
	for _, point := range w.baseMounts {
		x.baseMounts[point] = true
	}
	for _, ino := range w.baseOthers {
		x.baseInos[ino] = true
	}
	for _, e := range w.diff {
		x.sawDiffEntry(e)
	}
	for _, quoted := range w.gone {
		list.noteGone(quoted)
	}
	for _, e := range w.base {
		x.sawBaseEntry(e)
	}
	drop, why, _ := x.finish(int64(len(w.base)))
	if why != "" {
		return nil, why
	}
	if countBits(drop) != x.dropped {
		t.Errorf("%d bits are set, %d entries were counted as dropped", countBits(drop), x.dropped)
	}
	for i, e := range w.base {
		if !bitSet(drop, int64(i)) {
			kept = append(kept, e.name())
		}
	}
	return kept, ""
}

// quiet 是一個什麼都沒變的情境，其他情境從它改。
func quiet() world {
	return world{
		base: []indexEntry{
			entry(typeDir, "."), entry(typeDir, "./a"), entry(typeFile, "./a/f1"), entry(typeFile, "./a/f2"),
			link("./a/h", "./a/f1"), entry(typeDir, "./b"), entry(typeFile, "./b/f1"), entry(typeSymlink, "./b/s"),
		},
		baseDirs: map[string]dirIdentity{".": {2, 1000e9}, "./a": {3, 1000e9}, "./b": {4, 1000e9}},
		now: []snapDir{
			{".", 2, []string{"Da", "Db", "Ninit"}},
			{"./a", 3, []string{"Yf1", "Yf2", "Yh"}},
			{"./b", 4, []string{"Yf1", "Ys"}},
		},
		// 差異一定帶著現在的每個目錄；變更時間和完整備份時一樣。
		diff: []indexEntry{dirAt(".", 1000), dirAt("./a", 1000), dirAt("./b", 1000)},
	}
}

func TestExplain(t *testing.T) {
	everything := "./a/f1 ./a/f2 ./a/h ./b/f1 ./b/s"
	cases := []struct {
		label  string
		change func(w *world)
		kept   string
		why    string
	}{
		{"nothing changed: every file stays, every directory comes from the differential", func(*world) {}, everything, ""},
		{"a file was edited", func(w *world) { w.diff = append(w.diff, entry(typeFile, "./a/f2")) }, "./a/f1 ./a/h ./b/f1 ./b/s", ""},
		{"a file was deleted", func(w *world) {
			w.now[2].children = []string{"Ys"}
			w.diff[2] = dirAt("./b", 8000)
		}, "./a/f1 ./a/f2 ./a/h ./b/s", ""},
		{"a file was added", func(w *world) {
			w.now[2].children = []string{"Yf1", "Ys", "Ynew"}
			w.diff = append(w.diff[:2], dirAt("./b", 8000), entry(typeFile, "./b/new"))
		}, everything, ""},
		{"a directory was deleted", func(w *world) {
			w.now = []snapDir{{".", 2, []string{"Da"}}, w.now[1]}
			w.diff = []indexEntry{dirAt(".", 8000), dirAt("./a", 1000)}
		}, "./a/f1 ./a/f2 ./a/h", ""},
		{"a new directory with new files", func(w *world) {
			w.now[0].children = []string{"Da", "Db", "Dc"}
			w.now = append(w.now, snapDir{"./c", 50, []string{"Yx"}})
			w.diff = append(w.diff, dirAt("./c", 8000), entry(typeFile, "./c/x"))
		}, everything, ""},
		{"a file became a directory", func(w *world) {
			w.now[2].children = []string{"Df1", "Ys"}
			w.now = append(w.now, snapDir{"./b/f1", 60, []string{"Yinside"}})
			w.diff = append(w.diff, dirAt("./b/f1", 8000), entry(typeFile, "./b/f1/inside"))
		}, "./a/f1 ./a/f2 ./a/h ./b/s", ""},
		{"a directory became a file", func(w *world) {
			w.now = []snapDir{{".", 2, []string{"Da", "Yb"}}, w.now[1]}
			w.diff = []indexEntry{dirAt(".", 8000), dirAt("./a", 1000), entry(typeFile, "./b")}
		}, "./a/f1 ./a/f2 ./a/h", ""},
		{"all names of a hard-linked file changed together", func(w *world) {
			w.diff = append(w.diff, entry(typeFile, "./a/f1"), link("./a/h", "./a/f1"))
		}, "./a/f2 ./b/f1 ./b/s", ""},

		// 目錄被改名：底下的檔案時間都沒變，這一趟收不到，完整備份裡也沒有這個路徑。
		{"a directory was renamed", func(w *world) {
			w.now = []snapDir{{".", 2, []string{"Da", "Dc"}}, w.now[1], {"./c", 4, []string{"Yf1", "Ys"}}}
			w.diff = []indexEntry{dirAt(".", 8000), dirAt("./a", 1000), dirAt("./c", 8000)}
		}, "", whyMoved},
		// 兩個目錄互換：路徑都還在，只有 inode 看得出來。
		{"two directories were swapped", func(w *world) {
			w.now[1].ino, w.now[2].ino = 4, 3
			w.now[1].children, w.now[2].children = []string{"Yf1", "Ys"}, []string{"Yf1", "Yf2", "Yh"}
			w.diff = []indexEntry{dirAt(".", 8000), dirAt("./a", 8000), dirAt("./b", 8000)}
		}, "", whyMoved},
		{"a directory was replaced by another with the same names", func(w *world) { w.now[2].ino = 77 }, "", whyMoved},
		// 完整備份當時，這個目錄在備份途中被換過，所以沒有記下它的 inode。
		{"the full backup has no record of a directory", func(w *world) { delete(w.baseDirs, "./b") }, "", whyMoved},
		// 一個新的目錄（完整備份沒見過這個 inode），底下卻有這一趟沒收到的檔案，而它也不是剛剛才被刪掉的。
		{"old files under a directory that came from elsewhere", func(w *world) {
			w.now[0].children = []string{"Da", "Db", "Dc"}
			w.now = append(w.now, snapDir{"./c", 50, []string{"Yold"}})
			w.diff = append(w.diff, dirAt("./c", 7000))
		}, "", whyMoved},

		{"a file that the listing saw was gone a moment later", func(w *world) {
			w.now[2].children = []string{"Yf1", "Ys", "Ytemp"}
			w.diff[2] = dirAt("./b", testListStart+5)
		}, everything, ""},
		{"…but not if its directory has not changed since", func(w *world) {
			w.now[2].children = []string{"Yf1", "Ys", "Ytemp"}
			w.diff[2] = dirAt("./b", 8000)
		}, "", whyMoved},
		{"…and not in a directory that was moved here", func(w *world) {
			// ./c 的 inode 是完整備份時 ./b 的：它是搬過來的舊目錄，底下沒收到的就是舊檔案。
			w.now = []snapDir{{".", 2, []string{"Da", "Dc"}}, w.now[1], {"./c", 4, []string{"Yf1"}}}
			w.diff = []indexEntry{dirAt(".", testListStart+5), dirAt("./a", 1000), dirAt("./c", testListStart+5)}
		}, "", whyMoved},
		// tar 走到那個名稱時它已經不見了：tar 會說，名稱照 tar 的寫法跳脫。
		{"a new file that tar found gone when it got to it", func(w *world) {
			w.now[2].children = []string{"Yf1", "Ys", "Ytemp file"}
			w.gone = []string{"./b/temp file"}
		}, everything, ""},
		{"an old file that tar found gone stays as it was in the full backup", func(w *world) { w.gone = []string{"./b/f1"} }, everything, ""},
		{"a directory that tar found gone after writing its entry", func(w *world) {
			w.now[0].children = []string{"Da", "Db", "Dnew"}
			w.now = append(w.now, snapDir{"./new", 50, []string{"Yx", "Yy"}})
			w.diff = append(w.diff, dirAt("./new", 8000))
			w.gone = []string{"./new"}
		}, everything, ""},
		{"a name tar reports that is not in the listing changes nothing", func(w *world) { w.gone = []string{"./b/never-seen", `./odd\q`} }, everything, ""},
		// 一個完整備份沒見過的目錄在清單之後還在變，清單裡有個名稱這一趟沒收到。它多半是新目錄裡剛被刪掉的檔案，
		// 但也可能是從完整備份沒走過的地方（被排除的路徑）搬進來的舊目錄裡的舊檔案：
		// 兩者分不出來，所以不留這份差異。
		{"a file in a directory the full backup never saw is not excused, even if the directory just changed", func(w *world) {
			w.now[0].children = []string{"Da", "Db", "Dc"}
			w.now = append(w.now, snapDir{"./c", 50, []string{"Ytemp"}})
			w.diff = append(w.diff, dirAt("./c", testListStart+5))
		}, "", whyChanging},
		// 同一個目錄裡，tar 自己說走到時已經不見的名稱，照樣不算問題。
		{"…unless tar itself found it gone", func(w *world) {
			w.now[0].children = []string{"Da", "Db", "Dc"}
			w.now = append(w.now, snapDir{"./c", 50, []string{"Ytemp"}})
			w.diff = append(w.diff, dirAt("./c", testListStart+5))
			w.gone = []string{"./c/temp"}
		}, everything, ""},
		// 同樣的情況，但那個目錄在完整備份當時就存在了（只是當時在備份途中被搬動，沒有記成路徑）：
		// 它是舊目錄，底下沒收到的是舊檔案，不能當成剛被刪掉。
		{"…but not in a directory that already existed at the time of the full backup", func(w *world) {
			w.now[0].children = []string{"Da", "Db", "Dc"}
			w.now = append(w.now, snapDir{"./c", 50, []string{"Yold"}})
			w.diff = append(w.diff, dirAt("./c", testListStart+5))
			w.baseOthers = []uint64{50}
		}, "", whyMoved},
		{"a directory that vanished after the listing takes its files with it", func(w *world) {
			w.diff = []indexEntry{dirAt(".", testListStart+5), dirAt("./a", 1000)}
		}, "./a/f1 ./a/f2 ./a/h", ""},
		{"a directory created after the listing", func(w *world) {
			w.diff = append(w.diff, dirAt("./late", testListStart+5), entry(typeFile, "./late/x"))
		}, everything, ""},
		// 兩件事在同一趟裡發生：舊目錄不見了，又出現一個清單沒有的目錄。這可能是改名，
		// 而改名的目錄底下沒變過的檔案兩邊都不會有。
		{"an old directory vanished and another appeared during the run: it may be a rename", func(w *world) {
			w.diff = []indexEntry{dirAt(".", testListStart+5), dirAt("./a", 1000), dirAt("./c", testListStart+5)}
		}, "", whyChanging},
		// 不見的若是完整備份之後才建的目錄，就沒有舊檔案可以遺失。
		{"a directory newer than the full backup vanished while another appeared", func(w *world) {
			w.now[0].children = []string{"Da", "Db", "Dn"}
			w.now = append(w.now, snapDir{"./n", 50, []string{"Yx"}})
			w.diff = append(w.diff, dirAt("./late", testListStart+5))
			w.diff[0] = dirAt(".", testListStart+5)
		}, everything, ""},
		{"a file that appeared after the listing replaces the old one", func(w *world) {
			w.now[2].children = []string{"Ys"}
			w.diff = append(w.diff[:2], dirAt("./b", testListStart+5), entry(typeFile, "./b/f1"))
		}, "./a/f1 ./a/f2 ./a/h ./b/s", ""},
		// 清單漏了一個早就在的目錄：同一個目錄走到兩次時，tar 的清單就是這樣壞的。
		{"a directory the listing should have had", func(w *world) { w.diff = append(w.diff, dirAt("./ghost", 3000)) }, "", whyListing},

		{"a socket is in the listing and nowhere else", func(w *world) {
			w.now[2].children = []string{"Yf1", "Ys", "Ysock"}
			w.socket = "./b/sock"
		}, everything, ""},
		{"the same name without tar calling it a socket", func(w *world) { w.now[2].children = []string{"Yf1", "Ys", "Ysock"} }, "", whyMoved},
		{"an excluded name is in the listing and nowhere else", func(w *world) { w.now[2].children = []string{"Yf1", "Ys", "Ncache"} }, everything, ""},
		// 從別的裝置掛進來的單一檔案：清單標成 N，tar 卻會打包它。
		{"a file mounted from another device stays", func(w *world) {
			w.base = append(w.base, entry(typeFile, "./b/bind"))
			w.now[2].children = []string{"Yf1", "Ys", "Nbind"}
			w.mounts = map[string]bool{"./b/bind": true}
		}, everything + " ./b/bind", ""},
		{"a new mount of that kind needs a source", func(w *world) {
			w.now[2].children = []string{"Yf1", "Ys", "Nbind"}
			w.mounts = map[string]bool{"./b/bind": true}
		}, "", whyMoved},
		// WSL 的 /init 就是這樣：掛進來的單一檔案，而且被排除。完整備份當時它就在，也沒被收進去。
		{"a mount that was there at the time of the full backup and was excluded", func(w *world) {
			w.mounts = map[string]bool{"./init": true}
			w.baseMounts = []string{"./init"}
		}, everything, ""},
		{"the same mount, new since the full backup", func(w *world) { w.mounts = map[string]bool{"./init": true} }, "", whyMoved},

		// 目錄的變更時間和完整備份時不同，卻比基準時間還早：時鐘被往回調過。
		{"a change stamped before the full backup", func(w *world) { w.diff[2] = dirAt("./b", 4000) }, "", whyClock},
		{"the same stamp, in a directory that is not the old one", func(w *world) {
			w.now = []snapDir{{".", 2, []string{"Da", "Db"}}, w.now[1], {"./b", 90, nil}}
			w.diff[2] = dirAt("./b", 4000)
		}, "./a/f1 ./a/f2 ./a/h", ""},
		// 硬連結所指的那個項目不留了，連結自己卻沒有被這一趟收進去：它接不上任何東西。
		{"a hard link whose target is replaced while the link is not", func(w *world) { w.diff = append(w.diff, entry(typeFile, "./a/f1")) }, "", whyMoved},
		{"a hard link whose target was in a directory that is gone", func(w *world) {
			// 完整備份裡 ./b/h2 是指向 ./a/f1 的硬連結；之後 ./a 被改名成 ./c。
			w.base = append(w.base, link("./b/h2", "./a/f1"))
			w.now = []snapDir{{".", 2, []string{"Db", "Dc"}}, {"./b", 4, []string{"Yf1", "Ys", "Yh2"}}, {"./c", 3, []string{"Yf1", "Yf2", "Yh"}}}
			w.diff = []indexEntry{dirAt(".", 8000), dirAt("./b", 1000), dirAt("./c", 8000)}
		}, "", whyMoved},
	}
	for _, c := range cases {
		w := quiet()
		c.change(&w)
		kept, why := w.explain(t)
		if why != c.why || strings.Join(kept, " ") != c.kept {
			t.Errorf("%s:\n  kept %q, why %q\n  want %q, why %q", c.label, strings.Join(kept, " "), why, c.kept, c.why)
		}
	}

	// 索引的項目數和 manifest 記的不一樣：講的不是同一份封存。
	w := quiet()
	list := mustParse(t, buildSnapshot(w.now...))
	x := newExplainer(list, w.baseDirs, nil, testSince, testListStart)
	for _, e := range w.diff {
		x.sawDiffEntry(e)
	}
	for _, e := range w.base {
		x.sawBaseEntry(e)
	}
	if _, why, _ := x.finish(int64(len(w.base)) + 1); why != whyStructure {
		t.Errorf("an index with fewer entries than the manifest says: why = %q, want %q", why, whyStructure)
	}
}

// member 是 tar 裡一個項目的樣子，拿來比對兩個封存是不是同一棵樹。
type member struct {
	Kind   byte
	Mode   int64
	Owner  string
	Size   int64
	MTime  int64
	Target string // 符號連結的目標
	// Content 是內容的雜湊；硬連結記的是它指向的那個檔案的。
	Content string
	Xattrs  string
}

// readTree 把一個 tar 讀成「路徑 → 樣子」，並回傳哪些路徑是同一個檔案（硬連結）。
// 每個路徑只能出現一次。
func readTree(t *testing.T, r io.Reader) (map[string]member, map[string]string) {
	t.Helper()
	tree := map[string]member{}
	group := map[string]string{} // 路徑 → 這組硬連結裡排序最前面的那個路徑
	members := map[string][]string{}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		name := cleanMember(h.Name)
		if _, dup := tree[name]; dup {
			t.Errorf("%s appears twice in the stream", name)
		}
		m := member{Kind: h.Typeflag, Mode: h.Mode, Owner: fmt.Sprintf("%d:%d", h.Uid, h.Gid), Size: h.Size, MTime: h.ModTime.UnixNano(), Target: h.Linkname}
		var keys []string
		for k, v := range h.PAXRecords {
			if strings.HasPrefix(k, "SCHILY.xattr.") || strings.HasPrefix(k, "SCHILY.acl.") {
				keys = append(keys, k+"="+v)
			}
		}
		sort.Strings(keys)
		m.Xattrs = strings.Join(keys, ";")
		switch h.Typeflag {
		case tar.TypeLink:
			target := cleanMember(h.Linkname)
			src, ok := tree[target]
			if !ok {
				t.Errorf("%s is a hard link to %s, which is not earlier in the stream", name, target)
			}
			src.Target = ""
			m = src
			members[target] = append(members[target], name)
		case tar.TypeReg:
			sum := sha256.New()
			// 稀疏檔的洞不必真的讀出來比：大小與有資料的部分一樣就夠了。
			if _, err := io.Copy(sum, io.LimitReader(tr, 4<<20)); err != nil {
				t.Fatal(err)
			}
			m.Content = hex.EncodeToString(sum.Sum(nil))
			members[name] = append(members[name], name)
		}
		tree[name] = m
	}
	for _, names := range members {
		sort.Strings(names)
		for _, n := range names {
			group[n] = names[0]
		}
	}
	return tree, group
}

// 這是整個差異備份最重要的一個測試。testdata 裡的檔案都是真的 GNU tar 寫的（scripts/mkfixtures.sh）：
// 一份完整備份、之後那棵樹被改了一輪（改檔、刪檔、新增、改名、改權限、多一個硬連結、fifo 變成檔案、
// 裝置檔變成目錄、刪掉一整棵子目錄），再來是當時的清單、差異，以及改完之後重新做的一份完整備份。
// 把第一份完整備份和差異合併起來，必須和那份重新做的完整備份是同一棵樹。
func TestRealDifferential(t *testing.T) {
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Skipf("testdata/%s is not available next to the test binary", name)
		}
		return data
	}
	baseTar, diffTar, wantTar := read("gnu.tar"), read("gnu2.diff.tar"), read("gnu2.tar")
	var since, listStart int64
	if _, err := fmt.Sscan(string(read("gnu2.times")), &since, &listStart); err != nil {
		t.Fatal(err)
	}

	// 完整備份：它的索引、當時每個目錄的 inode 與變更時間。
	before := mustParse(t, read("gnu.snar"))
	var baseEntries []indexEntry
	dumped := map[string]int64{}
	if _, err := scanTar(bytes.NewReader(baseTar), func(e indexEntry) {
		baseEntries = append(baseEntries, e)
		if e.Type == typeDir {
			dumped[e.name()] = e.ctime
		}
	}); err != nil {
		t.Fatal(err)
	}
	baseDirs, others := stableDirs(before, before, dumped)
	if len(baseDirs) != len(dumped) || len(others) != 0 {
		t.Fatalf("%d of %d directories have an identity", len(baseDirs), len(dumped))
	}
	for name, id := range baseDirs {
		if id.CTime == 0 {
			t.Errorf("the archive has no change time for %s", name)
		}
	}

	// 差異：清單、這一趟收到的項目，然後逐一看完整備份的項目。
	now := mustParse(t, read("gnu2.snar"))
	if err := now.validate(); err != nil {
		t.Fatal(err)
	}
	x := newExplainer(now, baseDirs, nil, since, listStart)
	diffEntries := 0
	if _, err := scanTar(bytes.NewReader(diffTar), func(e indexEntry) {
		diffEntries++
		x.sawDiffEntry(e)
	}); err != nil {
		t.Fatal(err)
	}
	for _, e := range baseEntries {
		x.sawBaseEntry(e)
	}
	drop, why, detail := x.finish(int64(len(baseEntries)))
	if why != "" {
		t.Fatalf("no differential: %s (%s)", why, detail)
	}
	if x.dropped == 0 || x.dropped == int64(len(baseEntries)) {
		t.Errorf("%d of %d entries of the full backup are dropped", x.dropped, len(baseEntries))
	}

	// 合併，然後和重新做的那份完整備份比。
	var merged bytes.Buffer
	diffs := 0
	err := mergeTar(mergeInput{
		Base: bytes.NewReader(baseTar),
		Diff: func() (io.Reader, error) { diffs++; return bytes.NewReader(diffTar), nil },
		Drop: drop, BaseEntries: int64(len(baseEntries)),
	}, &merged)
	if err != nil {
		t.Fatal(err)
	}
	merged.Write(make([]byte, 2*tarBlock))
	if diffs != 2 {
		t.Errorf("the differential was opened %d times, want 2", diffs)
	}
	got, gotLinks := readTree(t, &merged)
	want, wantLinks := readTree(t, bytes.NewReader(wantTar))
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("%s is missing from the merged tree", name)
			continue
		}
		// 硬連結在兩個封存裡可能由不同的名稱當「本尊」；比的是內容與哪些名稱是同一個檔案。
		if w.Kind == tar.TypeLink || g.Kind == tar.TypeLink {
			w.Kind, g.Kind = tar.TypeReg, tar.TypeReg
		}
		if g != w {
			t.Errorf("%s differs:\n  merged %+v\n  wanted %+v", name, g, w)
		}
		if gotLinks[name] != wantLinks[name] {
			t.Errorf("%s is the same file as %q in the merged tree, as %q in the fresh backup", name, gotLinks[name], wantLinks[name])
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("%s is in the merged tree but no longer exists", name)
		}
	}
	// 用過的東西確實都用上了：被改過的檔案來自差異，沒動過的大檔案來自完整備份。
	if got["./fixture/plain.txt"].Size != 11 || got["./fixture/sparse.bin"].Size != 9<<30 {
		t.Errorf("plain.txt is %d bytes, sparse.bin %d", got["./fixture/plain.txt"].Size, got["./fixture/sparse.bin"].Size)
	}
	if diffEntries == 0 {
		t.Error("the differential is empty")
	}
}

func gzipped(t *testing.T, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	zw.Write(data)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// writeBackup 在 dir 裡寫出一份備份的封存、索引與 manifest，內容是 entries。
func writeBackup(t *testing.T, dir string, m *manifest, entries []testEntry) *manifest {
	t.Helper()
	data, _ := buildTar(t, entries)
	m.dir, m.Archive = dir, m.ID+suffixOf(m.Kind)
	archive := gzipped(t, data)
	if err := os.WriteFile(m.archivePath(), archive, 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := newIndexWriter(m.indexPath(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := scanTar(bytes.NewReader(data), w.add)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.close(true); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archive)
	m.Index, m.Size, m.SHA256 = &idx, int64(len(archive)), hex.EncodeToString(sum[:])
	if m.Schema == 0 {
		m.Schema = fullSchema
		if m.isDiff() {
			m.Schema = manifestSchema
		}
	}
	if err := m.save(); err != nil {
		t.Fatal(err)
	}
	return m
}

// 一份完整備份與一份差異，寫在磁碟上：./etc/passwd 被改了，./etc/gone 被刪了，./home/new 是新的。
func backupPair(t *testing.T, dir string) (base, diff *manifest) {
	t.Helper()
	base = writeBackup(t, dir, &manifest{ID: "20261015T030000Z", Distro: "Test"}, []testEntry{
		{name: "./", kind: tar.TypeDir},
		{name: "./etc/", kind: tar.TypeDir},
		{name: "./etc/passwd", kind: tar.TypeReg, body: "old\n"},
		{name: "./etc/hosts", kind: tar.TypeReg, body: "127.0.0.1 localhost\n"},
		{name: "./etc/gone", kind: tar.TypeReg, body: "bye\n"},
		{name: "./etc/hosts.link", kind: tar.TypeLink, link: "./etc/hosts"},
	})
	// 完整備份的六個項目裡，留下來的是 hosts 與它的硬連結（第 3、5 個）。
	drop := []byte{0b010111}
	diff = writeBackup(t, dir, &manifest{ID: "20261016T030000Z", Kind: kindDiff, Distro: "Test",
		Base: base.ID, BaseSHA256: base.SHA256, BaseEntries: base.Index.Entries, Drop: packBits(drop), DropCount: 4}, []testEntry{
		{name: "./", kind: tar.TypeDir},
		{name: "./etc/", kind: tar.TypeDir},
		{name: "./etc/passwd", kind: tar.TypeReg, body: "new\n"},
		{name: "./home/", kind: tar.TypeDir},
		{name: "./home/new", kind: tar.TypeReg, body: "hello\n"},
	})
	return base, diff
}

func TestMergedRestorePoint(t *testing.T) {
	dir := t.TempDir()
	base, _ := backupPair(t, dir)
	list := listBackups(dir)
	if len(list) != 2 || list[0].isDiff() || !list[1].isDiff() || list[1].base != list[0] || list[1].orphan() {
		t.Fatalf("listBackups did not link the differential to its full backup: %+v", list)
	}
	diff := findBackup(dir, "20261016T030000Z")
	if diff == nil || diff.base == nil || findBackup(dir, "20261017T030000Z") != nil || findBackup(dir, "../x") != nil {
		t.Fatal("findBackup")
	}

	// 整個還原點：每個路徑一次，目錄在前。
	var out bytes.Buffer
	var seenBase []string
	if err := writeMerged(diff, &out, mergeOptions{OnBase: func(name string, dropped bool) {
		if !dropped {
			seenBase = append(seenBase, name)
		}
	}}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(seenBase, " "); got != "./etc/hosts ./etc/hosts.link" {
		t.Errorf("kept from the full backup: %q", got)
	}
	var order []string
	contents := map[string]string{}
	tr := tar.NewReader(io.MultiReader(&out, bytes.NewReader(make([]byte, 2*tarBlock))))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		order = append(order, h.Name)
		contents[h.Name] = string(body)
	}
	if got := strings.Join(order, " "); got != "./ ./etc/ ./home/ ./etc/hosts ./etc/hosts.link ./etc/passwd ./home/new" {
		t.Errorf("order of the merged stream: %q", got)
	}
	if contents["./etc/passwd"] != "new\n" {
		t.Errorf("passwd is %q, want the one from the differential", contents["./etc/passwd"])
	}

	// files 與取回單檔看到的是同一個合併後的樣子。
	var names []string
	if err := scanEntries(diff, func(e indexEntry) bool { names = append(names, e.name()); return true }); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names, " "); got != "./etc/hosts ./etc/hosts.link . ./etc ./etc/passwd ./home ./home/new" {
		t.Errorf("scanEntries: %q", got)
	}
	names = nil
	scanEntries(diff, func(e indexEntry) bool { names = append(names, e.name()); return len(names) < 1 })
	if len(names) != 1 {
		t.Errorf("scanEntries went on after being told to stop: %q", names)
	}
	sel, stats, err := resolveSelection(func(visit func(indexEntry) bool) error { return scanEntries(diff, visit) }, []string{"/etc/hosts.link", "/etc/gone", "/home"})
	if err != nil || len(stats.Missing) != 1 || stats.Missing[0] != "/etc/gone" || !sel.extras["./etc/hosts"] {
		t.Errorf("selection: missing %q, extras %v, err %v", stats.Missing, sel.extras, err)
	}
	out.Reset()
	if err := writeMerged(diff, &out, mergeOptions{Want: func(e rawEntry) bool { return sel.wants(e.Name) }}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sortedKeys(tarMembers(t, append(out.Bytes(), make([]byte, 2*tarBlock)...))), " "); got != "./etc/hosts ./etc/hosts.link ./home ./home/new" {
		t.Errorf("a selection from the merged restore point: %q", got)
	}

	// 輕量檢查與封存的檢查。
	if res := runCheck(diff); !res.OK || res.Mode != verifyModeCheck {
		t.Errorf("runCheck: %+v", res)
	}
	if reason, detail := checkArchive(base); reason != "" {
		t.Errorf("checkArchive(base): %s %s", reason, detail)
	}

	// 完整備份被換成別的檔案：合併到最後會發現，輕量檢查也看得出大小不對。
	original, _ := os.ReadFile(base.archivePath())
	damaged := append([]byte{}, original...)
	damaged[len(damaged)-12] ^= 0xff
	os.WriteFile(base.archivePath(), damaged, 0o644)
	if err := writeMerged(diff, io.Discard, mergeOptions{}); err == nil {
		t.Error("a full backup with one byte flipped was merged without complaint")
	}
	// 換成另一個內容相同、壓縮結果不同的檔案：tar 走得完，只有雜湊看得出來。
	plain, _ := gzip.NewReader(bytes.NewReader(original))
	raw, _ := io.ReadAll(plain)
	var again bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&again, gzip.BestSpeed)
	zw.Write(raw)
	zw.Close()
	os.WriteFile(base.archivePath(), again.Bytes(), 0o644)
	var changed *errArchiveChanged
	if err := writeMerged(diff, io.Discard, mergeOptions{}); !errors.As(err, &changed) || changed.Name != base.Archive {
		t.Errorf("a full backup that was compressed again: err = %v, want it reported as changed", err)
	}
	os.WriteFile(base.archivePath(), original[:len(original)-1], 0o644)
	if res := runCheck(findBackup(dir, diff.ID)); res.OK || res.Reason != reasonBase {
		t.Errorf("runCheck with a shortened full backup: %+v", res)
	}
	os.WriteFile(base.archivePath(), original, 0o644)

	// 差異自己壞了。
	diffBytes, _ := os.ReadFile(diff.archivePath())
	os.WriteFile(diff.archivePath(), append(append([]byte{}, diffBytes...), 'x'), 0o644)
	if res := runCheck(findBackup(dir, diff.ID)); res.OK || (res.Reason != reasonChanged && res.Reason != reasonUnread) {
		t.Errorf("runCheck with a differential that has grown a byte: %+v", res)
	}
	os.WriteFile(diff.archivePath(), diffBytes, 0o644)

	// 「哪些項目不要了」壞了，或和完整備份的項目數對不上。
	broken := findBackup(dir, diff.ID)
	broken.DropCount = 3
	if err := writeMerged(broken, io.Discard, mergeOptions{}); !errors.Is(err, errMergeMismatch) {
		t.Errorf("a wrong count of dropped entries: err = %v", err)
	}
	if res := runCheck(broken); res.OK {
		t.Error("runCheck passed a damaged list of dropped entries")
	}

	// 完整備份不見了：差異成了孤兒，什麼都還原不出來。
	os.Remove(base.path())
	orphan := findBackup(dir, diff.ID)
	if orphan == nil || !orphan.orphan() || orphan.verified() {
		t.Fatalf("without its full backup the differential should be an orphan: %+v", orphan)
	}
	if err := writeMerged(orphan, io.Discard, mergeOptions{}); !errors.Is(err, errMergeMismatch) {
		t.Errorf("merging an orphan: err = %v", err)
	}
	if err := scanEntries(orphan, func(indexEntry) bool { return true }); err == nil {
		t.Error("scanEntries on an orphan succeeded")
	}
	if res := runCheck(orphan); res.OK || res.Reason != reasonBase {
		t.Errorf("runCheck on an orphan: %+v", res)
	}
}

func sortedKeys(m map[string]byte) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestManifestKinds(t *testing.T) {
	dir := t.TempDir()
	write := func(id, archive, body string) {
		os.WriteFile(filepath.Join(dir, archive), []byte("x"), 0o644)
		os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0o644)
	}
	write("20261001T030000Z", "20261001T030000Z.tar.gz", `{"schema":1,"id":"20261001T030000Z","archive":"20261001T030000Z.tar.gz"}`)
	write("20261002T030000Z", "20261002T030000Z.diff.tar.gz", `{"schema":2,"kind":"diff","base":"20261001T030000Z","id":"20261002T030000Z","archive":"20261002T030000Z.diff.tar.gz"}`)
	// 種類和檔名對不上、差異卻寫成舊格式、沒有基準、不認得的種類：都不算一份備份。
	write("20261003T030000Z", "20261003T030000Z.tar.gz", `{"schema":2,"kind":"diff","base":"20261001T030000Z","id":"20261003T030000Z","archive":"20261003T030000Z.tar.gz"}`)
	write("20261004T030000Z", "20261004T030000Z.diff.tar.gz", `{"schema":1,"kind":"diff","base":"20261001T030000Z","id":"20261004T030000Z","archive":"20261004T030000Z.diff.tar.gz"}`)
	write("20261005T030000Z", "20261005T030000Z.diff.tar.gz", `{"schema":2,"kind":"diff","id":"20261005T030000Z","archive":"20261005T030000Z.diff.tar.gz"}`)
	write("20261006T030000Z", "20261006T030000Z.tar.gz", `{"schema":2,"kind":"delta","id":"20261006T030000Z","archive":"20261006T030000Z.tar.gz"}`)
	write("20261007T030000Z", "20261007T030000Z.tar.gz", `{"schema":2,"kind":"full","id":"20261007T030000Z","archive":"20261007T030000Z.tar.gz"}`)
	list := listBackups(dir)
	if got := ids(list); got != "20261001T030000Z 20261002T030000Z 20261007T030000Z" {
		t.Errorf("listBackups = %q", got)
	}
	// 完整備份的雜湊和差異記的不一樣：不是當初那一份，接不上。
	if !list[1].orphan() {
		t.Error("a differential whose full backup has no matching checksum must be an orphan")
	}
	if list[2].isDiff() || list[2].Kind != "" {
		t.Errorf("kind full is the same as no kind: %q", list[2].Kind)
	}
}
