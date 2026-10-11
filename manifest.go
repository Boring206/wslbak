package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// 每份備份是三個檔案：封存、<編號>.json 與 <編號>.idx.gz（檔案索引，見 index.go）。
// .json 是這裡的 manifest，在封存完整寫好並改名之後才寫；沒有 manifest 的封存不算一份備份。
//
// 封存有兩種。完整備份是 <編號>.tar.gz：整個 distro，可以不靠 wslbak 直接 wsl --import。
// 差異備份是 <編號>.diff.tar.gz：只有它的完整備份之後變過的東西，要和那份完整備份合起來才是一個
// distro（見 diff.go）。開著差異模式做的完整備份旁邊還有 <編號>.dirs.gz，記著當時每個目錄的 inode。

const (
	// manifestSchema 是這個版本看得懂的最新格式。完整備份仍然寫成 fullSchema：它的格式沒有變，
	// 舊版的 wslbak 照樣能列出、還原。差異備份是 2：舊版看不懂，也因此不會在有差異的資料夾裡刪東西。
	manifestSchema = 2
	fullSchema     = 1
	// idLayout 是備份編號：UTC 時間，排序就是時間順序，也不會遇到日光節約時間重複的那一小時。
	idLayout      = "20060102T150405Z"
	archiveSuffix = ".tar.gz"
	diffSuffix    = ".diff.tar.gz"
	partialSuffix = ".partial"
	dirsSuffix    = ".dirs.gz"
)

var backupIDRe = regexp.MustCompile(`^\d{8}T\d{6}Z$`)

const (
	verifyModeRestore = "restore"
	// verifyModeCheck 是差異備份的輕量檢查：重讀封存、核對雜湊與結構，不匯入。
	verifyModeCheck   = "check"
	verifyModeSkipped = "skipped"
)

type verifyResult struct {
	At      time.Time `json:"at"`
	OK      bool      `json:"ok"`
	Mode    string    `json:"mode"`
	Reason  string    `json:"reason,omitempty"` // 失敗或略過的原因代碼
	Detail  string    `json:"detail,omitempty"` // 補充說明，英文，給紀錄檔看
	Seconds float64   `json:"seconds"`
	Samples int       `json:"samples"`
}

type manifest struct {
	Schema   int       `json:"schema"`
	ID       string    `json:"id"`
	Distro   string    `json:"distro"`
	DistroID string    `json:"distroId"`
	Created  time.Time `json:"created"`
	Tool     string    `json:"tool"`
	WSL      string    `json:"wsl"`
	Archive  string    `json:"archive"`
	Size     int64     `json:"size"`
	SHA256   string    `json:"sha256"`

	// 還原時要設回去的 distro 設定。
	DefaultUID uint32 `json:"defaultUid"`
	Flags      uint32 `json:"flags"`

	Excludes      []string `json:"excludes"`
	Dropped       []string `json:"dropped,omitempty"`       // 這個 distro 的 tar 不支援而拿掉的選項
	SkippedMounts []string `json:"skippedMounts,omitempty"` // 掛在別的磁碟上、沒有被備份的目錄
	Warnings      []string `json:"warnings,omitempty"`      // tar 的警告，最多留前幾行
	WarningCount  int      `json:"warningCount"`
	Seconds       float64  `json:"seconds"`

	// Index 是備份時掃描 tar 得到的索引；掃描器看不懂這份封存時是 nil，這份備份就無法試還原。
	Index  *tarIndex     `json:"index,omitempty"`
	Verify *verifyResult `json:"verify,omitempty"`

	// 以下是差異備份用的欄位。完整備份只有在差異模式開著時才有前四個；沒有它們的完整備份不能當基準。
	//
	// Since 是基準時間（從 1970 年起算的秒數）：差異收的是在這之後變過的檔案。
	// 完整備份記的是「之後的差異要用的時間」，差異記的是它實際用的。
	Since      int64      `json:"since,omitempty"`
	Clock      *clockInfo `json:"clock,omitempty"`
	TarVersion string     `json:"tarVersion,omitempty"`
	// DirsSHA256 是 <編號>.dirs.gz 的雜湊。
	DirsSHA256 string `json:"dirsSha256,omitempty"`
	// Mounts 是完整備份當時，樹裡的掛載點（寫法同封存裡的名稱）。
	Mounts []string `json:"mounts,omitempty"`

	// Kind 是空的或 full 表示完整備份，diff 表示差異備份。
	Kind string `json:"kind,omitempty"`
	// Base 是這份差異所依據的完整備份；BaseSHA256 與 BaseEntries 用來確認旁邊那一份真的是它。
	Base        string `json:"base,omitempty"`
	BaseSHA256  string `json:"baseSha256,omitempty"`
	BaseEntries int64  `json:"baseEntries,omitempty"`
	// Drop 是完整備份裡「這個還原點不要了」的項目：每個項目一個位元（依封存裡的順序），
	// gzip 壓縮後以 base64 寫成文字。被刪掉的、被差異裡新的一筆取代的，都在裡面。
	// 它放在 manifest 裡而不是另一個檔案：少了它，被刪掉的檔案會在還原時全部回來。
	Drop      string `json:"drop,omitempty"`
	DropCount int64  `json:"dropCount,omitempty"`

	dir string
	// base 是 Base 指的那份完整備份，由 listBackups 接上；找不到或對不上時是 nil。
	base *manifest
}

func (m *manifest) archivePath() string { return filepath.Join(m.dir, m.Archive) }
func (m *manifest) path() string        { return filepath.Join(m.dir, m.ID+".json") }
func (m *manifest) dirsPath() string    { return filepath.Join(m.dir, m.ID+dirsSuffix) }
func (m *manifest) save() error         { return writeJSON(m.path(), m) }
func (m *manifest) isDiff() bool        { return m.Kind == kindDiff }

// orphan：差異所依據的完整備份不在了（或不是當初那一份）。這種差異還原不出一個 distro。
func (m *manifest) orphan() bool { return m.isDiff() && m.base == nil }

// verified 回報這份備份有沒有通過檢查（試還原，或差異的輕量檢查）。
func (m *manifest) verified() bool { return m.Verify != nil && m.Verify.OK && !m.orphan() }

// canBeBase 回報這份完整備份能不能當差異的基準：要有基準時間、當時的目錄清單與檔案索引。
func (m *manifest) canBeBase() bool {
	return !m.isDiff() && m.Since > 0 && m.Clock != nil && m.DirsSHA256 != "" && m.TarVersion != "" && m.Index != nil
}

// suffixOf 回傳某一種備份的封存檔副檔名。
func suffixOf(kind string) string {
	if kind == kindDiff {
		return diffSuffix
	}
	return archiveSuffix
}

// tarSize 是還原這個還原點要送進 wsl --import 的 tar 大約有多大。
func (m *manifest) tarSize() int64 {
	var n int64
	if m.Index != nil {
		n = m.Index.Size
	}
	if m.base != nil && m.base.Index != nil {
		n += m.base.Index.Size
	}
	return n
}

// readManifest 讀一份 manifest，並確認它描述的就是旁邊那個封存：
// 編號與檔名一致、封存檔名是我們的格式（不含路徑）、封存還在。
func readManifest(dir, id string) *manifest {
	if !backupIDRe.MatchString(id) {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		return nil
	}
	m := &manifest{}
	if json.Unmarshal(data, m) != nil || m.Schema < 1 || m.Schema > manifestSchema {
		return nil
	}
	switch m.Kind {
	case "", kindFull:
		m.Kind = ""
	case kindDiff:
		if m.Schema < 2 || !backupIDRe.MatchString(m.Base) {
			return nil
		}
	default:
		return nil
	}
	if m.ID != id || m.Archive != id+suffixOf(m.Kind) {
		return nil
	}
	m.dir = dir
	if info, err := os.Stat(m.archivePath()); err != nil || info.IsDir() {
		return nil
	}
	return m
}

// listBackups 回傳 dir 裡所有的備份，由舊到新，並把每份差異接上它的完整備份。
func listBackups(dir string) []*manifest {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var list []*manifest
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() {
			continue
		}
		if m := readManifest(dir, id); m != nil {
			list = append(list, m)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	linkBases(list)
	return list
}

// linkBases 把每份差異接上它的完整備份。旁邊那一份的雜湊與項目數都要和差異當初記下的一樣：
// 「哪些項目不要了」是照著那一份的順序記的，換了一份就完全不對。
func linkBases(list []*manifest) {
	byID := make(map[string]*manifest, len(list))
	for _, m := range list {
		byID[m.ID] = m
	}
	for _, m := range list {
		if !m.isDiff() {
			continue
		}
		if b := byID[m.Base]; b != nil && !b.isDiff() && b.SHA256 == m.BaseSHA256 && b.Index != nil && b.Index.Entries == m.BaseEntries {
			m.base = b
		}
	}
}

// findBackup 讀出 dir 裡編號為 id 的那一份（差異會接上它的完整備份）；沒有就是 nil。
func findBackup(dir, id string) *manifest {
	if !backupIDRe.MatchString(id) {
		return nil
	}
	for _, m := range listBackups(dir) {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// newest 回傳最新的一份；onlyVerified 為 true 時只看通過試還原的。
func newest(list []*manifest, onlyVerified bool) *manifest {
	for i := len(list) - 1; i >= 0; i-- {
		if !onlyVerified || list[i].verified() {
			return list[i]
		}
	}
	return nil
}

// newerManifests 回報 dir 裡有沒有「比這個版本新的 wslbak」寫的 manifest。
// 那些備份我們讀不懂，也不知道它們之間的依賴關係，所以只要看到，就不在這個資料夾刪任何東西。
func newerManifests(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || !backupIDRe.MatchString(id) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var head struct {
			Schema int `json:"schema"`
		}
		if json.Unmarshal(data, &head) == nil && head.Schema > manifestSchema {
			return true
		}
	}
	return false
}
