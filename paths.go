package main

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// 資料夾配置，都在目前這個 Windows 使用者底下：
//
//	%LOCALAPPDATA%\Programs\wslbak\   排程執行的程式複本
//	%LOCALAPPDATA%\wslbak\            設定、紀錄、鎖
//	%LOCALAPPDATA%\wslbak\verify\     試還原用的暫時 distro
//
// --home 會把後兩個換到指定的資料夾，測試時用來和真正的設定隔開。

var homeOverride string

// localAppData 向系統查資料夾位置，不讀環境變數：排程啟動時環境變數不一定和登入時相同。
func localAppData() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
}

func stateDir() (string, error) {
	if homeOverride != "" {
		return homeOverride, nil
	}
	base, err := localAppData()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "wslbak"), nil
}

func verifyRoot() (string, error) {
	state, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "verify"), nil
}

// normPath 把路徑整理成可以直接比對的樣子：去掉 \\?\ 前綴與多餘的分隔符號，並轉成小寫
// （Windows 的路徑不分大小寫）。只用來比對，不拿來開檔。
func normPath(p string) string {
	p = strings.TrimPrefix(p, `\\?\`)
	p = filepath.Clean(strings.ReplaceAll(p, "/", `\`))
	return strings.ToLower(p)
}
