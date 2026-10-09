package main

import (
	"io"
	"os"
	"strconv"

	"golang.org/x/sys/windows"
)

// 這個檔案裡的東西只在測試用的沙箱（--home）裡生效，讓端對端測試製造平常很難遇到的狀況。
// 沒有 --home 時這些環境變數一律被忽略，正常使用不會經過這裡。

const (
	// 備份檔寫到這麼多位元組之後，假裝磁碟滿了。
	envTestDiskFullAfter = "WSLBAK_TEST_DISK_FULL_AFTER"
	// 設成 1 時，沙箱裡也真的跳出 Windows 通知。
	envTestToast = "WSLBAK_TEST_TOAST"
	// 設成 1 時，沒有指定 distro 的 init 只挑測試用的 distro（平常正好相反），
	// 測試才能真的執行 init --all，又碰不到別的 distro。
	envTestOnlyTestDistros = "WSLBAK_TEST_ONLY_TEST_DISTROS"
)

// fullAfterWriter 讓前 left 個位元組照常寫入，之後回報磁碟已滿。
type fullAfterWriter struct {
	w    io.Writer
	left int64
}

func (f *fullAfterWriter) Write(p []byte) (int, error) {
	if int64(len(p)) <= f.left {
		f.left -= int64(len(p))
		return f.w.Write(p)
	}
	n, err := f.w.Write(p[:f.left])
	f.left = 0
	if err != nil {
		return n, err
	}
	return n, windows.ERROR_DISK_FULL
}

// withTestFaults 在沙箱裡依環境變數替寫入端加上故障；其他時候原樣傳回。
func withTestFaults(w io.Writer) io.Writer {
	if homeOverride == "" {
		return w
	}
	n, err := strconv.ParseInt(os.Getenv(envTestDiskFullAfter), 10, 64)
	if err != nil || n < 0 {
		return w
	}
	return &fullAfterWriter{w: w, left: n}
}

// toastAllowed：平常一律跳通知；沙箱裡預設不跳，免得測試一直打擾人。
func toastAllowed() bool {
	return homeOverride == "" || os.Getenv(envTestToast) == "1"
}

// onlyTestDistros：自動挑選 distro 時是否只看測試用的那些。
func onlyTestDistros() bool {
	return homeOverride != "" && os.Getenv(envTestOnlyTestDistros) == "1"
}
