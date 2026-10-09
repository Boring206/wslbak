package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// 排程執行時沒有畫面可看，所以每次執行都把經過寫進 %LOCALAPPDATA%\wslbak\wslbak.log。
// 紀錄是給回報問題用的，固定用英文。

const maxLogSize = 1 << 20

var runLog *os.File

// openRunLog 開啟紀錄檔；太大時先把舊的改名成 .1（只留一份舊的）。開不了就不記，不影響備份。
func openRunLog() {
	state, err := stateDir()
	if err != nil {
		return
	}
	if err := os.MkdirAll(state, 0o755); err != nil {
		return
	}
	path := filepath.Join(state, "wslbak.log")
	if info, err := os.Stat(path); err == nil && info.Size() > maxLogSize {
		os.Remove(path + ".1")
		os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	runLog = f
}

// logf 寫一行到紀錄檔；有 --debug 時也印到畫面上。
func logf(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	if runLog != nil {
		fmt.Fprintf(runLog, "%s [%d] %s\n", time.Now().Format(time.RFC3339), os.Getpid(), text)
	}
	debugf("%s", text)
}
