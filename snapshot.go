package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 清單：差異備份要知道「現在這棵樹裡有什麼」以及「哪個目錄還是原來那個目錄」。
//
// 來源是 GNU tar 自己的快照檔：tar --create --file=/dev/null --listed-incremental=<檔案>。
// 寫到 /dev/null 時 tar 不開啟任何檔案，只把整棵樹走一遍；快照檔（格式 2）以 NUL 分隔，
// 列出每個目錄的裝置、inode，以及它底下每個名稱和一個標記：
//
//	Y  會被打包的項目（目錄以外的東西）
//	N  不會被打包：被排除樣式擋掉，或是從別的裝置掛進來的單一檔案
//	D  子目錄（它自己另有一筆紀錄；被排除的目錄是 N，沒有紀錄）
//
// 用它而不用 tar -v 的輸出，是因為它不必猜跳脫格式，而且帶著目錄的 inode。
// 真正的打包與還原都不使用 --listed-incremental。
//
// 這份清單和實際打包只有兩處不同（scripts/e2e-tarfacts.sh 會確認）：
// socket 在清單裡是 Y，但 tar 不打包它；從別的裝置掛進來的單一檔案在清單裡是 N，但 tar 會打包它。

const (
	markYes = 'Y'
	markNo  = 'N'
	markDir = 'D'
)

// 每個名稱的狀態，由比對的過程填上。
const (
	seenInDiff  = 1 << iota // 這一趟差異的 tar 收了它
	keptInBase              // 它留在完整備份裡，原樣沿用
	isSocketBit             // tar 說它是 socket
	goneBit                 // 這一趟差異的 tar 走到它時，它已經不見了
)

var errBadSnapshot = errors.New("not a usable tar snapshot")

// listedDir 是清單裡的一個目錄。
type listedDir struct {
	dev, ino uint64
	// names 是底下所有名稱接在一起：每一筆是「標記＋名稱」。starts 是每一筆的起點，依名稱排序。
	names  []byte
	starts []uint32
	state  []uint8
	// inDiff：這一趟差異的 tar 也看到了這個目錄。
	inDiff bool
}

func (d *listedDir) count() int { return len(d.starts) }

func (d *listedDir) entry(i int) []byte {
	end := uint32(len(d.names))
	if i+1 < len(d.starts) {
		end = d.starts[i+1]
	}
	return d.names[d.starts[i]:end]
}

func (d *listedDir) mark(i int) byte   { return d.entry(i)[0] }
func (d *listedDir) name(i int) string { return string(d.entry(i)[1:]) }

// find 回傳名稱在這個目錄裡的位置；沒有就是 -1。
func (d *listedDir) find(name string) int {
	i := sort.Search(len(d.starts), func(i int) bool { return string(d.entry(i)[1:]) >= name })
	if i < len(d.starts) && string(d.entry(i)[1:]) == name {
		return i
	}
	return -1
}

// listing 是整份清單。目錄的名稱和 tar 封存裡的寫法一樣：「.」、「./etc」、「./etc/apt」。
type listing struct {
	header string
	dirs   map[string]*listedDir
	// bytes 是快照檔的長度，拿來和腳本回報的數字核對。
	bytes int64
}

var snapshotHeaderRe = regexp.MustCompile(`^GNU tar-[^\n]*-2$`)

// maxSnapshotField 是快照檔裡單一欄位的長度上限：路徑最長 4096，留一點餘裕。
const maxSnapshotField = 64 << 10

type snapshotReader struct {
	r *bufio.Reader
	n int64
}

// field 讀到下一個 NUL 為止（不含 NUL）。檔案剛好在欄位開頭結束時回傳 io.EOF。
func (s *snapshotReader) field() ([]byte, error) {
	var out []byte
	for {
		chunk, err := s.r.ReadSlice(0)
		s.n += int64(len(chunk))
		if err == nil {
			return append(out, chunk[:len(chunk)-1]...), nil
		}
		out = append(out, chunk...)
		if err == io.EOF {
			if len(out) == 0 {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("%w: it ends in the middle of a field", errBadSnapshot)
		}
		if err != bufio.ErrBufferFull {
			return nil, err
		}
		if len(out) > maxSnapshotField {
			return nil, fmt.Errorf("%w: a field of more than %d bytes", errBadSnapshot, maxSnapshotField)
		}
	}
}

func (s *snapshotReader) number() (uint64, error) {
	f, err := s.field()
	if err == io.EOF {
		return 0, fmt.Errorf("%w: it ends in the middle of a record", errBadSnapshot)
	}
	if err != nil {
		return 0, err
	}
	// 裝置與時間在理論上可以是負數；這裡只拿來比對是否相同，照位元樣式存。
	if n, perr := strconv.ParseInt(string(f), 10, 64); perr == nil {
		return uint64(n), nil
	}
	n, perr := strconv.ParseUint(string(f), 10, 64)
	if perr != nil {
		return 0, fmt.Errorf("%w: %q is not a number", errBadSnapshot, f)
	}
	return n, nil
}

// parseSnapshot 把快照檔讀成清單。只檢查格式；這份清單能不能用，由 validate 判斷。
func parseSnapshot(r io.Reader) (*listing, error) {
	s := &snapshotReader{r: bufio.NewReaderSize(r, 256<<10)}
	l := &listing{dirs: map[string]*listedDir{}}
	finish := func(err error) (*listing, error) {
		// 不管成不成功都把剩下的讀完：寫的那一端才不會卡住，長度也才算得出來。
		rest, _ := io.Copy(io.Discard, s.r)
		l.bytes = s.n + rest
		if err != nil {
			return l, err
		}
		return l, nil
	}

	line, err := s.r.ReadString('\n')
	s.n += int64(len(line))
	if err != nil {
		return finish(fmt.Errorf("%w: no first line", errBadSnapshot))
	}
	l.header = strings.TrimSuffix(line, "\n")
	if !snapshotHeaderRe.MatchString(l.header) {
		return finish(fmt.Errorf("%w: first line %q", errBadSnapshot, plain(l.header)))
	}
	// 這次執行的時間：秒、奈秒。
	for i := 0; i < 2; i++ {
		if _, err := s.number(); err != nil {
			return finish(err)
		}
	}
	for {
		// 每個目錄：nfs、修改時間（秒、奈秒）、裝置、inode、名稱，然後是底下的名稱，最後兩個 NUL。
		if _, err := s.field(); err == io.EOF {
			return finish(nil)
		} else if err != nil {
			return finish(err)
		}
		var numbers [4]uint64
		for i := range numbers {
			if numbers[i], err = s.number(); err != nil {
				return finish(err)
			}
		}
		name, err := s.field()
		if err != nil {
			return finish(fmt.Errorf("%w: it ends in the middle of a record", errBadSnapshot))
		}
		dir := &listedDir{dev: numbers[2], ino: numbers[3]}
		for {
			child, err := s.field()
			if err != nil {
				return finish(fmt.Errorf("%w: it ends in the middle of a record", errBadSnapshot))
			}
			if len(child) == 0 {
				break
			}
			if len(child) < 2 || (child[0] != markYes && child[0] != markNo && child[0] != markDir) || bytes.IndexByte(child[1:], '/') >= 0 {
				return finish(fmt.Errorf("%w: entry %q in %q", errBadSnapshot, plain(string(child)), plain(string(name))))
			}
			dir.starts = append(dir.starts, uint32(len(dir.names)))
			dir.names = append(dir.names, child...)
		}
		// 名稱清單以兩個 NUL 結束：剛才讀到的空欄位是第一個。
		if closing, err := s.field(); err != nil || len(closing) != 0 {
			return finish(fmt.Errorf("%w: the list of %q is not closed", errBadSnapshot, plain(string(name))))
		}
		dir.sortNames()
		dir.state = make([]uint8, len(dir.starts))
		if _, dup := l.dirs[string(name)]; dup {
			return finish(fmt.Errorf("%w: %q is listed twice", errBadSnapshot, plain(string(name))))
		}
		l.dirs[string(name)] = dir
	}
}

// sortNames 把名稱排好。tar 寫出來時通常已經排好了，但這裡不依賴它。
func (d *listedDir) sortNames() {
	type span struct{ from, to uint32 }
	spans := make([]span, len(d.starts))
	for i := range d.starts {
		end := uint32(len(d.names))
		if i+1 < len(d.starts) {
			end = d.starts[i+1]
		}
		spans[i] = span{d.starts[i], end}
	}
	key := func(s span) []byte { return d.names[s.from+1 : s.to] }
	if sort.SliceIsSorted(spans, func(i, j int) bool { return bytes.Compare(key(spans[i]), key(spans[j])) < 0 }) {
		return
	}
	sort.Slice(spans, func(i, j int) bool { return bytes.Compare(key(spans[i]), key(spans[j])) < 0 })
	names := make([]byte, 0, len(d.names))
	for i, s := range spans {
		d.starts[i] = uint32(len(names))
		names = append(names, d.names[s.from:s.to]...)
	}
	d.names = names
}

// splitMember 把名稱拆成上一層與自己的名稱：./etc/apt → ./etc、apt；./etc → 「.」、etc。
func splitMember(name string) (parent, base string) {
	i := strings.LastIndexByte(name, '/')
	if i < 0 {
		return "", name
	}
	return name[:i], name[i+1:]
}

// validate 確認清單是一棵完整的樹：有根、沒有哪個目錄出現兩次、每個標成 D 的名稱都有自己的紀錄、
// 每筆紀錄都掛在某個上層底下。
//
// 這不是多餘的小心。同一個目錄如果從兩條路徑都走得到（同一顆磁碟上的綁定掛載），tar 會把第二次
// 看到的當成「改名」，清單就不再描述真正的樹；拿它去判斷哪些檔案被刪了，會把還在的檔案當成不見了。
func (l *listing) validate() error {
	if _, ok := l.dirs["."]; !ok {
		return fmt.Errorf("%w: the top directory is not in it", errBadSnapshot)
	}
	type identity struct{ dev, ino uint64 }
	seen := make(map[identity]string, len(l.dirs))
	children := 0
	for name, dir := range l.dirs {
		id := identity{dir.dev, dir.ino}
		if other, dup := seen[id]; dup {
			return fmt.Errorf("%w: %q and %q are the same directory", errBadSnapshot, plain(name), plain(other))
		}
		seen[id] = name
		for i := 0; i < dir.count(); i++ {
			if i > 0 && dir.name(i) == dir.name(i-1) {
				return fmt.Errorf("%w: %q appears twice in %q", errBadSnapshot, plain(dir.name(i)), plain(name))
			}
			if dir.mark(i) != markDir {
				continue
			}
			children++
			if _, ok := l.dirs[name+"/"+dir.name(i)]; !ok {
				return fmt.Errorf("%w: the directory %q has no record", errBadSnapshot, plain(name+"/"+dir.name(i)))
			}
		}
	}
	// 每個標成 D 的名稱都有紀錄，而且名稱不重複；紀錄數如果正好是「那些名稱＋根」，
	// 就沒有哪一筆紀錄是沒人認領的。
	if children+1 != len(l.dirs) {
		for name := range l.dirs {
			if name == "." {
				continue
			}
			parent, base := splitMember(name)
			if up, ok := l.dirs[parent]; !ok || up.find(base) < 0 || up.mark(up.find(base)) != markDir {
				return fmt.Errorf("%w: %q is not listed in its parent", errBadSnapshot, plain(name))
			}
		}
		return fmt.Errorf("%w: %d records for %d directories", errBadSnapshot, len(l.dirs), children+1)
	}
	return nil
}

// lookup 找出一個名稱（不是目錄紀錄，而是它在上層裡的那一筆）。
func (l *listing) lookup(name string) (dir *listedDir, index int) {
	parent, base := splitMember(name)
	dir = l.dirs[parent]
	if dir == nil {
		return nil, -1
	}
	return dir, dir.find(base)
}

// entries 回傳清單裡的名稱總數（不含根）。
func (l *listing) entries() int64 {
	var n int64
	for _, dir := range l.dirs {
		n += int64(dir.count())
	}
	return n
}

// socketLineRe 是 tar 對每個 socket 說的話（LC_ALL=C 下的原文）。
var socketLineRe = regexp.MustCompile(`^tar: (.+): socket ignored$`)

// unquoteTarName 把 tar 訊息裡的檔名還原：tar 用反斜線寫出控制字元、反斜線本身、冒號，
// 以及（在 C 語系下）所有非 ASCII 的位元組。看不懂的寫法回傳 false。
func unquoteTarName(s string) (string, bool) {
	if strings.IndexByte(s, '\\') < 0 {
		return s, true
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			return "", false
		}
		switch c := s[i]; c {
		case 'a':
			b.WriteByte(7)
		case 'b':
			b.WriteByte(8)
		case 't':
			b.WriteByte(9)
		case 'n':
			b.WriteByte(10)
		case 'v':
			b.WriteByte(11)
		case 'f':
			b.WriteByte(12)
		case 'r':
			b.WriteByte(13)
		case '\\', ':', '"', '\'', ' ', '?':
			b.WriteByte(c)
		default:
			if c < '0' || c > '7' || i+2 >= len(s) {
				return "", false
			}
			n, err := strconv.ParseUint(s[i:i+3], 8, 8)
			if err != nil {
				return "", false
			}
			b.WriteByte(byte(n))
			i += 2
		}
	}
	return b.String(), true
}

// noteGone 記下 tar 在打包途中發現已經不見的一個名稱（清單之後才被刪掉或改名的）。
// 不見的是目錄的話，它底下的東西也跟著沒了。
func (l *listing) noteGone(quoted string) {
	name, ok := unquoteTarName(quoted)
	if !ok {
		return
	}
	name = cleanMember(name)
	if dir := l.dirs[name]; dir != nil {
		dir.inDiff = false
	}
	if dir, i := l.lookup(name); dir != nil && i >= 0 {
		dir.state[i] |= goneBit
	}
}

// noteSocket 把 tar 回報的一個 socket 記到清單上；清單裡找不到那個名稱時回傳 false。
func (l *listing) noteSocket(quoted string) bool {
	name, ok := unquoteTarName(quoted)
	if !ok {
		return false
	}
	dir, i := l.lookup(cleanMember(name))
	if dir == nil || i < 0 {
		return false
	}
	dir.state[i] |= isSocketBit
	return true
}
