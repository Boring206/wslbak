package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// 設定檔是 %LOCALAPPDATA%\wslbak\config.json，每個 Windows 使用者一份。
// 排程執行時所有會變的東西（備份哪個 distro、放到哪裡）都從這裡讀。

// configSchema 是這個版本看得懂的設定格式。讀到更新的格式時拒絕寫入，
// 免得舊版程式把新版加的欄位弄丟。
const configSchema = 1

const (
	verifyRestore = "restore" // 每次備份後匯入成暫時 distro 檢查
	verifyNone    = "none"

	notifyOnFailure = "failure"
	notifyAlways    = "always" // 成功也通知：收不到通知就代表排程沒有在跑

	defaultKeep = 7
	defaultAt   = "03:00"
)

type notifyConfig struct {
	Toast   bool   `json:"toast"`
	Webhook string `json:"webhook"`
	On      string `json:"on"`
}

type distroConfig struct {
	// ID 是登錄檔裡的 GUID。同名的 distro 被移除重裝之後 GUID 會不同，
	// 這時不該默默把新的 distro 接著備份到舊的那一串後面。
	ID   string `json:"id"`
	Dest string `json:"dest"`
	// Keep 是保留最新的幾份；KeepWeekly、KeepMonthly 是在那之外，每週、每月各再留一份，留幾週、幾個月。
	Keep            int      `json:"keep"`
	KeepWeekly      int      `json:"keepWeekly,omitempty"`
	KeepMonthly     int      `json:"keepMonthly,omitempty"`
	Exclude         []string `json:"exclude"`
	DefaultExcludes bool     `json:"defaultExcludes"`
	Enabled         bool     `json:"enabled"`
}

type config struct {
	Schema  int                      `json:"schema"`
	At      string                   `json:"at"`
	Verify  string                   `json:"verify"`
	Notify  notifyConfig             `json:"notify"`
	Distros map[string]*distroConfig `json:"distros"`
}

func newConfig() *config {
	return &config{
		Schema:  configSchema,
		At:      defaultAt,
		Verify:  verifyRestore,
		Notify:  notifyConfig{Toast: true, On: notifyOnFailure},
		Distros: map[string]*distroConfig{},
	}
}

// defaultExcludes 是預設不備份的東西：暫存檔、快取，以及 WSL 掛進每個 distro 的 /init
// （它在另一個裝置上，是綁定掛載的單一檔案，--one-file-system 擋不住）。
var defaultExcludes = []string{"./init", "./tmp/*", "./var/tmp/*", "./home/*/.cache/*", "./root/.cache/*"}

// alwaysExclude 即使關掉預設排除也照樣排除。
var alwaysExclude = []string{"./init"}

func (d *distroConfig) excludes() []string {
	list := alwaysExclude
	if d.DefaultExcludes {
		list = defaultExcludes
	}
	return append(append([]string{}, list...), d.Exclude...)
}

var errNewerConfig = errors.New("the configuration was written by a newer version of wslbak")

func configPath() (string, error) {
	state, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "config.json"), nil
}

// loadConfig 讀設定檔；還沒有設定過時回傳 nil, nil。
func loadConfig() (*config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c := newConfig()
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Schema > configSchema {
		return nil, errNewerConfig
	}
	if _, ok := normalizeClock(c.At); !ok {
		c.At = defaultAt
	}
	if c.Verify != verifyNone {
		c.Verify = verifyRestore
	}
	if c.Notify.On != notifyAlways {
		c.Notify.On = notifyOnFailure
	}
	if c.Distros == nil {
		c.Distros = map[string]*distroConfig{}
	}
	for _, d := range c.Distros {
		if d.Keep < 1 {
			d.Keep = defaultKeep
		}
	}
	return c, nil
}

func saveConfig(c *config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	// 先確認現有的檔案不是更新的格式。
	if _, err := loadConfig(); errors.Is(err, errNewerConfig) {
		return err
	}
	c.Schema = configSchema
	return writeJSON(path, c)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// writeFileAtomic 先寫到旁邊的暫存檔、落盤、再改名：讀的人只會看到舊的或新的完整內容。
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".new"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// 執行紀錄：每個 distro 上次嘗試與上次成功的時間。排程用它判斷今天是不是已經備份過，
// status 用它判斷備份是不是過期了。備份的目的地可能是沒接上的外接碟，所以不能只靠那邊的檔案。

const (
	resultOK         = "ok"
	resultUnverified = "unverified" // 備份寫好了，但沒有做（或做不了）試還原
	resultFailed     = "failed"
)

type distroState struct {
	LastAttempt time.Time `json:"lastAttempt"`
	LastSuccess time.Time `json:"lastSuccess"`
	LastID      string    `json:"lastId"`
	LastResult  string    `json:"lastResult"`
	LastMessage string    `json:"lastMessage"`
}

type runState struct {
	Distros map[string]*distroState `json:"distros"`
}

func statePath() (string, error) {
	state, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "state.json"), nil
}

func loadState() *runState {
	s := &runState{Distros: map[string]*distroState{}}
	path, err := statePath()
	if err != nil {
		return s
	}
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, s)
	}
	if s.Distros == nil {
		s.Distros = map[string]*distroState{}
	}
	return s
}

func (s *runState) of(distro string) *distroState {
	if s.Distros[distro] == nil {
		s.Distros[distro] = &distroState{}
	}
	return s.Distros[distro]
}

func saveState(s *runState) error {
	path, err := statePath()
	if err != nil {
		return err
	}
	return writeJSON(path, s)
}
