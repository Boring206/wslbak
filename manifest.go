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

// 每份備份是兩個檔案：<編號>.tar.gz 與 <編號>.json。
// .json 是這裡的 manifest，在封存完整寫好並改名之後才寫；沒有 manifest 的封存不算一份備份。

const (
	manifestSchema = 1
	// idLayout 是備份編號：UTC 時間，排序就是時間順序，也不會遇到日光節約時間重複的那一小時。
	idLayout      = "20060102T150405Z"
	archiveSuffix = ".tar.gz"
	partialSuffix = ".tar.gz.partial"
)

var backupIDRe = regexp.MustCompile(`^\d{8}T\d{6}Z$`)

const (
	verifyModeRestore = "restore"
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

	dir string
}

func (m *manifest) archivePath() string { return filepath.Join(m.dir, m.Archive) }
func (m *manifest) path() string        { return filepath.Join(m.dir, m.ID+".json") }
func (m *manifest) verified() bool      { return m.Verify != nil && m.Verify.OK }
func (m *manifest) save() error         { return writeJSON(m.path(), m) }

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
	if m.ID != id || m.Archive != id+archiveSuffix {
		return nil
	}
	m.dir = dir
	if info, err := os.Stat(m.archivePath()); err != nil || info.IsDir() {
		return nil
	}
	return m
}

// listBackups 回傳 dir 裡所有的備份，由舊到新。
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
	return list
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
