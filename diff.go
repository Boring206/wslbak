package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/gzip"
)

// 差異備份：定期做一份完整備份，其餘的日子只存「從那份完整備份之後變過的東西」。
//
// 一份差異＝一趟 tar --after-date（內容或屬性變過的檔案，加上所有目錄），外加一份證明：
// 現在這棵樹裡的每個檔案，不是在這一趟裡，就是還在完整備份裡原來的位置、而且它所在的目錄
// 還是原來那個目錄。證明成立，就記下「完整備份裡哪些項目不要了」；有任何一處解釋不了，
// 這一次就改做完整備份。寧可多做一次完整，也不留下一份還原出來會不一樣的差異。
//
// 為什麼只比路徑不夠：把一個目錄改名，底下檔案的時間都不會變，--after-date 收不到它們；
// 把兩個目錄互換，連路徑都一樣，內容卻換了。所以要認目錄的 inode。

const (
	kindFull = "full"
	kindDiff = "diff"

	modeFull = "full"
	modeDiff = "diff"

	// defaultFullEvery：完整備份最多用幾天，之後的下一次備份重新做一份完整的。
	defaultFullEvery = 30
	maxFullEvery     = 365
	// sinceMargin：基準時間往前多留的一段。兩邊時鐘的小幅校正都落在這裡面。
	sinceMargin = 5 * time.Minute
	// raceSlack：比較「清單開始的時間」與檔案上的時間時容許的誤差（秒）。
	raceSlack = 2
	// mergedVerifyEvery：同一份完整備份底下，隔多久做一次真正的合併試還原；其餘的差異做輕量檢查。
	mergedVerifyEvery = 7 * 24 * time.Hour
)

// 這次為什麼做完整備份而不是差異。空字串表示沒有什麼好說的（本來就是完整模式）。
const (
	whyAsked          = "asked"      // 使用者給了 --full
	whyFirst          = "first"      // 還沒有可以當基準的完整備份
	whyBaseUnverified = "unverified" // 基準沒有通過試還原
	whyBaseDamaged    = "damaged"    // 基準的檔案不見了或變了
	whySettings       = "settings"   // 排除清單、tar 的版本或選項和基準不同
	whyAge            = "age"        // 基準太舊
	whySize           = "size"       // 上一份差異已經超過基準的一半
	whyFailed         = "failed"     // 上一份差異沒有通過檢查
	whyClock          = "clock"      // 時鐘不對勁
	whyFilesystem     = "filesystem" // 根檔案系統不是 ext4
	whyAlias          = "alias"      // 同一個目錄從兩條路徑都走得到
	whyListing        = "listing"    // 清單拿不到，或不能用
	whyMoved          = "moved"      // 有檔案換了位置卻沒有變過（目錄被改名或搬動）
	whyStructure      = "structure"  // 寫出來的差異沒有通過結構檢查
)

// clockInfo 是備份開始時兩邊的時鐘（從 1970 年起算的秒數）。
type clockInfo struct {
	Distro  int64 `json:"distro"`
	Windows int64 `json:"windows"`
}

// sinceFor 算出基準時間：兩邊時鐘較早的那個，再往前留一段。
// 檔案上的時間是 distro 的時鐘寫的；它在電腦睡眠後常常落後，那沒有關係（落後的時鐘照樣只往前走）。
// 要防的是它「超前」之後又被校正回來：那之後改過的檔案，時間會比超前時記下的基準還早。
func sinceFor(c clockInfo) int64 {
	return min(c.Distro, c.Windows) - currentSinceMargin()
}

// mountRow 是 /proc/self/mountinfo 的一行裡我們要的欄位。
type mountRow struct {
	ID, Parent    string
	Dev           string
	FSRoot, Mount string
	FS            string
}

// unescapeMount 還原 mountinfo 裡的路徑：空白、定位、換行與反斜線寫成 \040 這種八進位。
func unescapeMount(s string) string {
	if strings.IndexByte(s, '\\') < 0 {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// mountFacts 是從掛載表看出來、和差異備份有關的事。
type mountFacts struct {
	// RootFS 是根目錄的檔案系統類型。
	RootFS string
	// Aliases 是「把根磁碟上的某個資料夾再掛到另一個地方」的掛載點：同一個目錄會被走到兩次。
	Aliases []string
	// Points 是 tar 走得到的掛載點，寫法和封存裡的名稱一樣（./init）。
	Points map[string]bool
}

// readMounts 分析掛載表。tar 帶著 --one-file-system 從 / 走下去，只會經過和 / 同一個裝置的掛載；
// 掛在別的裝置底下的東西它碰不到，所以不管。
func readMounts(rows []mountRow) mountFacts {
	facts := mountFacts{Points: map[string]bool{}}
	var root *mountRow
	for i := range rows {
		if rows[i].Mount == "/" {
			root = &rows[i]
		}
	}
	if root == nil {
		return facts
	}
	facts.RootFS = root.FS
	reached := map[string]bool{root.ID: true}
	// 掛載表不保證上層排在前面，所以重複到不再增加為止。
	for grew := true; grew; {
		grew = false
		for i := range rows {
			r := &rows[i]
			if reached[r.ID] || !reached[r.Parent] {
				continue
			}
			facts.Points["."+unescapeMount(r.Mount)] = true
			if r.Dev != root.Dev {
				continue
			}
			reached[r.ID] = true
			grew = true
			// 掛的是根磁碟上的另一個資料夾：那個資料夾現在有兩條路徑。
			// 把資料夾掛在它自己上面（Docker 會這樣做）不算。
			if r.FSRoot != r.Mount {
				facts.Aliases = append(facts.Aliases, unescapeMount(r.Mount))
			}
		}
	}
	return facts
}

// distroFacts 是清單那一趟順便帶回來的資訊。
type distroFacts struct {
	Clock      int64
	TarVersion string
	Mounts     mountFacts
}

// factsFromProto 從腳本的回報裡取出時鐘、tar 的版本與掛載表。
func factsFromProto(rows [][]string) distroFacts {
	var f distroFacts
	var mounts []mountRow
	for _, row := range rows {
		switch {
		case row[0] == "clock" && len(row) > 1:
			f.Clock, _ = strconv.ParseInt(row[1], 10, 64)
		case row[0] == "tar-version" && len(row) > 1:
			f.TarVersion = row[1]
		case row[0] == "mount" && len(row) == 7:
			mounts = append(mounts, mountRow{ID: row[1], Parent: row[2], Dev: row[3], FSRoot: row[4], Mount: row[5], FS: row[6]})
		}
	}
	f.Mounts = readMounts(mounts)
	return f
}

// kindQuery 是決定「這次做完整還是差異」需要知道的事。
type kindQuery struct {
	Mode      string
	ForceFull bool
	FullEvery int
	Verify    string // 設定裡的 verify
	Excludes  []string
	Backups   []*manifest // 由舊到新
	Now       time.Time
	// ArchiveSize 回傳封存檔現在的大小；檔案不在時是 -1。
	ArchiveSize func(*manifest) int64
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// newestFull 回傳最新的一份完整備份。
func newestFull(list []*manifest) *manifest {
	for i := len(list) - 1; i >= 0; i-- {
		if !list[i].isDiff() {
			return list[i]
		}
	}
	return nil
}

// chooseKind 在動手之前判斷這次能不能做差異。可以的話回傳當基準的那份完整備份；
// 不行的話 base 是 nil，why 是原因（完整模式本來就不做差異，why 是空的）。
// 這裡只看 Windows 這邊已經知道的事；distro 裡的情況由 environmentProblem 與 explainer 接著判斷。
func chooseKind(q kindQuery) (base *manifest, why string) {
	if q.Mode != modeDiff {
		return nil, ""
	}
	if q.ForceFull {
		return nil, whyAsked
	}
	base = newestFull(q.Backups)
	if base == nil || !base.canBeBase() {
		return nil, whyFirst
	}
	if q.Verify == verifyRestore && !base.verified() {
		return nil, whyBaseUnverified
	}
	if q.ArchiveSize != nil && q.ArchiveSize(base) != base.Size {
		return nil, whyBaseDamaged
	}
	if !sameStrings(base.Excludes, q.Excludes) {
		return nil, whySettings
	}
	if q.Now.Before(base.Created) {
		return nil, whyClock
	}
	every := q.FullEvery
	if every < 1 {
		every = defaultFullEvery
	}
	if q.Now.Sub(backupTime(base)) >= time.Duration(every)*24*time.Hour {
		return nil, whyAge
	}
	// 這份基準底下最新的一份差異。
	for i := len(q.Backups) - 1; i >= 0; i-- {
		m := q.Backups[i]
		if !m.isDiff() || m.Base != base.ID {
			continue
		}
		if m.Verify != nil && !m.Verify.OK && m.Verify.Mode != verifyModeSkipped {
			return nil, whyFailed
		}
		if m.Size > base.Size/2 {
			return nil, whySize
		}
		break
	}
	return base, ""
}

// environmentProblem 看 distro 裡現在的情況和做基準時是否一樣，一樣才能做差異。
func environmentProblem(base *manifest, f distroFacts) string {
	switch {
	case f.Mounts.RootFS != "ext4":
		// 差異靠的是「改名、建立連結、被覆蓋都會更新 inode 的變更時間」，這只在 ext4 上確認過。
		return whyFilesystem
	case len(f.Mounts.Aliases) > 0:
		return whyAlias
	case f.TarVersion == "" || f.TarVersion != base.TarVersion:
		return whySettings
	case base.Clock == nil || f.Clock <= base.Clock.Distro:
		return whyClock
	}
	return ""
}

// chooseVerify 決定一份剛寫好的備份要怎麼驗證：真正的試還原，或輕量檢查。
// 完整備份每次都試還原。差異要把整份完整備份再匯入一次才能試還原，天天做就失去差異的意義，
// 所以只在這三種時候做：這份基準的第一份差異、距離上次合併試還原滿七天、以及上一份沒有通過
// （那種情況其實會先被 chooseKind 改成完整備份）。
func chooseVerify(m *manifest, backups []*manifest, now time.Time) string {
	if !m.isDiff() {
		return verifyModeRestore
	}
	var last time.Time
	for _, other := range backups {
		if other.ID == m.ID || !other.isDiff() || other.Base != m.Base {
			continue
		}
		if other.Verify != nil && other.Verify.OK && other.Verify.Mode == verifyModeRestore && other.Verify.At.After(last) {
			last = other.Verify.At
		}
	}
	if last.IsZero() || now.Sub(last) >= mergedVerifyEvery {
		return verifyModeRestore
	}
	return verifyModeCheck
}

// dirIdentity 是完整備份當時某個目錄的身分。
type dirIdentity struct {
	Ino   uint64
	CTime int64 // 奈秒
}

// <編號>.dirs.gz：開著差異模式做完整備份時，旁邊多存的一個檔案，記著當時每個目錄的 inode 與變更時間。
// 之後的差異靠它認出「這個路徑上的目錄還是不是原來那個」。格式和檔案索引一樣是 gzip 壓縮的 JSON，
// 一行一筆，第一行是檔頭。它的雜湊記在 manifest 裡；對不上就不拿來用。

const dirsVersion = 1

type dirsHeader struct {
	Version int    `json:"wslbakDirs"`
	ID      string `json:"id"`
}

// dirRecord 是 dirs.gz 裡的一筆。沒有名稱的紀錄只說「完整備份當時有一個目錄是這個 inode」：
// 那個目錄在備份途中被換過或搬過，不能拿來認路徑，但之後看到這個 inode 時要知道它不是新建的。
type dirRecord struct {
	Path  string `json:"p,omitempty"`
	PathB string `json:"pb,omitempty"` // 名稱不是合法的 UTF-8 時：原始位元組的 base64
	Ino   uint64 `json:"i"`
	CTime int64  `json:"c,omitempty"`
}

// stableDirs 挑出可以當作身分的目錄：打包前、後兩次清單裡 inode 相同的那些，配上打包時看到的變更時間。
// 只在一次清單裡出現、或兩次 inode 不同的目錄，是在備份途中被建立、刪除或換掉的；封存裡存的
// 是哪一個說不準，所以不拿它們的路徑當身分。之後的差異遇到沒有紀錄的路徑，會把那個目錄當成「不是原來那個」。
// 那些目錄的 inode 另外回傳（others）：它們確實在完整備份當時就存在。
func stableDirs(before, after *listing, dumped map[string]int64) (stable map[string]dirIdentity, others []uint64) {
	stable = make(map[string]dirIdentity, len(dumped))
	inStable := map[uint64]bool{}
	for name, ctime := range dumped {
		a, b := before.dirs[name], after.dirs[name]
		if a != nil && b != nil && a.dev == b.dev && a.ino == b.ino && a.ino != 0 {
			stable[name] = dirIdentity{Ino: a.ino, CTime: ctime}
			inStable[a.ino] = true
		}
	}
	seen := map[uint64]bool{}
	for _, list := range []*listing{before, after} {
		for _, dir := range list.dirs {
			if !inStable[dir.ino] && !seen[dir.ino] {
				seen[dir.ino] = true
				others = append(others, dir.ino)
			}
		}
	}
	sort.Slice(others, func(i, j int) bool { return others[i] < others[j] })
	return stable, others
}

// writeDirs 由打包前後的兩份清單與打包時看到的目錄，寫出 <編號>.dirs.gz，回傳檔案的 SHA-256。
func writeDirs(path, id string, before, after *listing, dumped map[string]int64) (string, error) {
	dirs, others := stableDirs(before, after, dumped)
	names := make([]string, 0, len(dirs))
	for name := range dirs {
		names = append(names, name)
	}
	sort.Strings(names)
	var raw bytes.Buffer
	zw := gzip.NewWriter(&raw)
	enc := json.NewEncoder(zw)
	err := enc.Encode(dirsHeader{Version: dirsVersion, ID: id})
	for _, name := range names {
		if err != nil {
			break
		}
		rec := dirRecord{Ino: dirs[name].Ino, CTime: dirs[name].CTime}
		if utf8.ValidString(name) {
			rec.Path = name
		} else {
			rec.PathB = base64.StdEncoding.EncodeToString([]byte(name))
		}
		err = enc.Encode(rec)
	}
	for _, ino := range others {
		if err != nil {
			break
		}
		err = enc.Encode(dirRecord{Ino: ino})
	}
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(path, raw.Bytes()); err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

var errBadDirs = errors.New("the record of directories is missing or has changed")

// readDirs 讀回 <編號>.dirs.gz：每個路徑上的目錄身分，以及當時存在的其他目錄的 inode。
// 雜湊和 manifest 記的不一樣就不用。
func readDirs(path, wantSHA string) (dirs map[string]dirIdentity, others []uint64, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errBadDirs, err)
	}
	if sum := sha256.Sum256(raw); wantSHA == "" || hex.EncodeToString(sum[:]) != wantSHA {
		return nil, nil, errBadDirs
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errBadDirs, err)
	}
	dec := json.NewDecoder(zr)
	var head dirsHeader
	if err := dec.Decode(&head); err != nil || head.Version != dirsVersion {
		return nil, nil, errBadDirs
	}
	dirs = map[string]dirIdentity{}
	for {
		var rec dirRecord
		if err := dec.Decode(&rec); err == io.EOF {
			return dirs, others, nil
		} else if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", errBadDirs, err)
		}
		name := rec.Path
		if rec.PathB != "" {
			b, err := base64.StdEncoding.DecodeString(rec.PathB)
			if err != nil {
				return nil, nil, errBadDirs
			}
			name = string(b)
		}
		if name == "" {
			others = append(others, rec.Ino)
			continue
		}
		dirs[name] = dirIdentity{Ino: rec.Ino, CTime: rec.CTime}
	}
}

// explainer 拿三樣東西對照：現在的清單、完整備份的索引、這一趟差異收到的項目。
// 用法：差異的每個項目呼叫 sawDiffEntry，接著完整備份的每個項目依序呼叫 sawBaseEntry，最後 finish。
type explainer struct {
	list *listing
	// baseDirs：完整備份當時，每個目錄的身分。只有在備份前後兩次清單裡 inode 相同的目錄才在裡面。
	// baseInos：完整備份當時存在的每個目錄的 inode，包含不在 baseDirs 裡的那些。
	// 一個目錄的 inode 不在這裡面，它才是完整備份之後新建的。
	baseDirs map[string]dirIdentity
	baseInos map[uint64]bool
	// mounts 是現在樹裡的掛載點，baseMounts 是完整備份當時的。
	mounts     map[string]bool
	baseMounts map[string]bool
	since      int64 // 秒
	// listStart 是清單開始時 distro 的時鐘（秒）。
	listStart int64

	// extra 是清單之後才出現、卻被這一趟收進去的名稱。
	extra map[string]bool
	// diffCTime 是這一趟看到的每個目錄的變更時間（秒）。
	diffCTime map[*listedDir]int64

	drop     []byte
	seen     int64
	dropped  int64
	why      string
	detail   string
	examples []string
}

func newExplainer(list *listing, baseDirs map[string]dirIdentity, mounts map[string]bool, since, listStart int64) *explainer {
	x := &explainer{list: list, baseDirs: baseDirs, mounts: mounts, since: since, listStart: listStart,
		baseInos: make(map[uint64]bool, len(baseDirs)), baseMounts: map[string]bool{}, extra: map[string]bool{}, diffCTime: map[*listedDir]int64{}}
	for _, id := range baseDirs {
		x.baseInos[id.Ino] = true
	}
	return x
}

func (x *explainer) fail(why, detail string) {
	if x.why == "" {
		x.why, x.detail = why, detail
	}
}

// unexplained 記下一個解釋不了的檔案。名稱只進紀錄檔，不進通知。
func (x *explainer) unexplained(name string) {
	if len(x.examples) < 3 {
		x.examples = append(x.examples, displayPath(name))
	}
	x.fail(whyMoved, "")
}

// sameDir 回報清單裡的這個目錄是不是完整備份當時、同一個路徑上的那個目錄。
func (x *explainer) sameDir(name string, dir *listedDir) bool {
	base, ok := x.baseDirs[name]
	return ok && base.Ino != 0 && base.Ino == dir.ino
}

// changedDuringRun 回報這個目錄在清單開始之後還有沒有變過（多了或少了名稱）。
func (x *explainer) changedDuringRun(dir *listedDir) bool {
	return x.diffCTime[dir] >= x.listStart-raceSlack
}

// sawDiffEntry：這一趟差異的 tar 寫出了這個項目。
func (x *explainer) sawDiffEntry(e indexEntry) {
	name := e.name()
	if e.Type != typeDir {
		if dir, i := x.list.lookup(name); dir != nil && i >= 0 {
			dir.state[i] |= seenInDiff
		} else {
			x.extra[name] = true
		}
		return
	}
	dir := x.list.dirs[name]
	ctime := e.ctime / int64(time.Second)
	if dir == nil {
		// 清單裡沒有這個目錄。清單之後才建立或搬進來的沒有問題；更早就在的話，是清單漏了東西
		// （同一個目錄走到兩次時就會這樣），整份清單都不能信。
		if ctime < x.listStart-raceSlack {
			x.fail(whyListing, "a directory is missing from the listing: "+plain(displayPath(name)))
		}
		x.extra[name] = true
		return
	}
	dir.inDiff = true
	x.diffCTime[dir] = ctime
	// 同一個目錄，變更時間和完整備份時不同，卻比基準時間還早：它是在時鐘被往回調的期間變的。
	// 那段期間改過的檔案也會有同樣的問題，而 --after-date 看不到它們。
	if base, ok := x.baseDirs[name]; ok && base.Ino == dir.ino && e.ctime != 0 && e.ctime != base.CTime && ctime < x.since {
		x.fail(whyClock, "a directory changed at a time earlier than the full backup: "+plain(displayPath(name)))
	}
	if up, i := x.list.lookup(name); up != nil && i >= 0 {
		up.state[i] |= seenInDiff
	}
}

// keepBase 決定完整備份裡的一個項目要不要留在還原點裡。
func (x *explainer) keepBase(e indexEntry) bool {
	name := e.name()
	// 現在還在的目錄，這一趟一定寫了新的一筆；不在的就是被刪了。完整備份裡的目錄項目一律不用。
	if e.Type == typeDir || x.extra[name] {
		return false
	}
	dir, i := x.list.lookup(name)
	// 上層目錄沒了（或在清單之後消失了），或名稱不在裡面：已經刪除。
	if dir == nil || !dir.inDiff || i < 0 {
		return false
	}
	// 這一趟有新的內容，或現在是個目錄。
	if dir.state[i]&seenInDiff != 0 || dir.mark(i) == markDir {
		return false
	}
	// 還在，而且沒有被這一趟收進去：只有在「它所在的目錄還是原來那個目錄」時，才知道它沒變。
	parent, _ := splitMember(name)
	if !x.sameDir(parent, dir) {
		x.unexplained(name)
		return false
	}
	if e.Type == typeHardlink {
		// 硬連結只記「和哪個項目是同一個檔案」；那個項目也要留著才接得上。它一定排在前面。
		// 留不住的情況只有一種來源：那個項目所在的目錄被改名或搬走了（直接刪掉或換掉它，
		// 這個連結的變更時間會跟著變，就不會走到這裡）。所以原因和其他被搬動的檔案一樣。
		target, j := x.list.lookup(e.Link)
		if target == nil || j < 0 || target.state[j]&keptInBase == 0 {
			x.unexplained(name)
			return false
		}
	}
	dir.state[i] |= keptInBase
	return true
}

// sawBaseEntry：完整備份裡的下一個項目（依封存裡的順序）。
func (x *explainer) sawBaseEntry(e indexEntry) {
	ordinal := x.seen
	x.seen++
	if int64(len(x.drop))*8 <= ordinal {
		x.drop = append(x.drop, 0)
	}
	if x.why != "" || x.keepBase(e) {
		return
	}
	x.drop[ordinal/8] |= 1 << uint(ordinal%8)
	x.dropped++
}

// finish 檢查清單裡有沒有「哪裡都找不到來源」的檔案，並回傳結果。
// why 是空的表示證明成立：drop 是完整備份裡不要的項目（每個項目一個位元）。
func (x *explainer) finish(baseEntries int64) (drop []byte, why, detail string) {
	if x.why == "" && x.seen != baseEntries {
		x.fail(whyStructure, fmt.Sprintf("the index of the full backup has %d entries, its manifest says %d", x.seen, baseEntries))
	}
	if x.why == "" {
	search:
		for name, dir := range x.list.dirs {
			if !dir.inDiff {
				// 清單之後消失的目錄：底下的東西跟著沒了。
				continue
			}
			for i := 0; i < dir.count(); i++ {
				if dir.state[i]&(seenInDiff|keptInBase|isSocketBit|goneBit) != 0 {
					continue
				}
				switch dir.mark(i) {
				case markDir:
					// 子目錄在清單之後被刪掉了。
					continue
				case markNo:
					// 被排除的名稱。從別的裝置掛進來的單一檔案也是 N，而那種 tar 會打包（除非它也被排除，
					// 像 WSL 的 /init）。完整備份當時就掛在那裡的不用管：當時收了的話它在完整備份裡，
					// 沒收就是被排除的。完整備份之後才掛上來的，就得有來源。
					if path := name + "/" + dir.name(i); !x.mounts[path] || x.baseMounts[path] {
						continue
					}
				}
				// 清單裡有，這一趟沒收，完整備份裡也沒有。唯一正常的解釋是它在清單之後被刪掉或改名了
				// （否則它是新的，這一趟會收到）。tar 走到它時才發現不見的，tar 會說，前面已經記下了；
				// 剩下的是在那之前就不見的：那樣的話，tar 看到它所在的目錄時，目錄才剛變過。
				// 一個從別處搬來的舊目錄不適用這個解釋：它底下的舊檔案本來就不會被這一趟收到。
				if x.changedDuringRun(dir) && (x.sameDir(name, dir) || !x.baseInos[dir.ino]) {
					continue
				}
				x.unexplained(name + "/" + dir.name(i))
				break search
			}
		}
	}
	if x.why != "" {
		detail := x.detail
		if len(x.examples) > 0 {
			detail = "for example " + plain(strings.Join(x.examples, ", "))
		}
		return nil, x.why, detail
	}
	return x.drop, "", ""
}

// packBits 把位元圖壓縮後寫成文字，放進 manifest。
func packBits(bits []byte) string {
	var b bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	zw.Write(bits)
	zw.Close()
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

// maxDropBytes 是位元圖解開後的長度上限（十億個項目）。manifest 是從磁碟讀來的，不照單全收。
const maxDropBytes = 128 << 20

func unpackBits(text string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	bits, err := io.ReadAll(io.LimitReader(zr, maxDropBytes+1))
	if err != nil {
		return nil, err
	}
	if len(bits) > maxDropBytes {
		return nil, fmt.Errorf("the list of dropped entries is too large")
	}
	return bits, nil
}

func bitSet(bits []byte, n int64) bool {
	return n >= 0 && n/8 < int64(len(bits)) && bits[n/8]&(1<<uint(n%8)) != 0
}

func countBits(bits []byte) int64 {
	var n int64
	for _, b := range bits {
		for ; b != 0; b &= b - 1 {
			n++
		}
	}
	return n
}
