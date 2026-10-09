package main

import (
	"errors"
	"regexp"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// distro 的身分直接從登錄檔讀，不解析 wsl.exe 印出來的文字：
// 那些文字會隨語言改變，而且拿不到安裝位置。

const lxssKey = `Software\Microsoft\Windows\CurrentVersion\Lxss`

// lxssInstalled 是 State 欄位的值：安裝完成、不在匯入／匯出／移除的途中。
const lxssInstalled = 1

type regDistro struct {
	GUID       string
	Name       string
	BasePath   string // 虛擬磁碟所在的資料夾
	Version    int    // 1 或 2
	State      int
	DefaultUID uint32
	Flags      uint32
}

// readLxss 回傳目前使用者註冊的所有 distro；沒有安裝 WSL 時回傳 nil, nil。
func readLxss() ([]regDistro, error) {
	root, err := registry.OpenKey(registry.CURRENT_USER, lxssKey, registry.ENUMERATE_SUB_KEYS)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	guids, err := root.ReadSubKeyNames(-1)
	if err != nil {
		return nil, err
	}
	var list []regDistro
	for _, guid := range guids {
		k, err := registry.OpenKey(root, guid, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		d := regDistro{GUID: guid}
		d.Name, _, _ = k.GetStringValue("DistributionName")
		d.BasePath, _, _ = k.GetStringValue("BasePath")
		version, _, _ := k.GetIntegerValue("Version")
		state, _, _ := k.GetIntegerValue("State")
		uid, _, _ := k.GetIntegerValue("DefaultUid")
		flags, _, _ := k.GetIntegerValue("Flags")
		k.Close()
		d.Version, d.State, d.DefaultUID, d.Flags = int(version), int(state), uint32(uid), uint32(flags)
		if d.Name != "" {
			list = append(list, d)
		}
	}
	return list, nil
}

// distroNameRe 是 WSL 自己允許的 distro 名稱。名稱會出現在 wsl.exe 的命令列與資料夾名稱裡，
// 不在這個範圍內的一律不碰。
var distroNameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,64}$`)

// managedPrefixes 是由其他程式建立與管理的 distro：它們的內容隨時會被重建，備份沒有意義。
var managedPrefixes = []string{"docker-desktop", "rancher-desktop", "podman-machine-"}

// eligible 回報這個 distro 能不能備份；不能的話 reason 是給使用者看的原因。
func eligible(d regDistro) (reason string, ok bool) {
	lower := strings.ToLower(d.Name)
	switch {
	case strings.HasPrefix(lower, "wslbak-verify-"):
		return T.SkipOwn, false
	case !distroNameRe.MatchString(d.Name):
		return T.SkipName, false
	case d.State != lxssInstalled:
		return T.SkipBusy, false
	case d.Version != 2:
		return T.SkipWSL1, false
	}
	for _, prefix := range managedPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return T.SkipManaged, false
		}
	}
	return "", true
}

func findDistro(distros []regDistro, name string) *regDistro {
	for i := range distros {
		if strings.EqualFold(distros[i].Name, name) {
			return &distros[i]
		}
	}
	return nil
}
