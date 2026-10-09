package main

import (
	"io"
	"os"
	"strconv"
	"time"

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
	// 取回檔案時，在「解開完」與「搬到目的地」之間停下來：先建立「<值>.reached」這個檔案，
	// 再等「<值>」這個檔案出現（最多一分鐘）。測試利用這段時間把目的地換成別的東西。
	envTestHoldBeforeMove = "WSLBAK_TEST_HOLD_BEFORE_MOVE"
	// 「多久沒有資料就算停住」改成這麼多秒，測試才不必等上十分鐘。
	envTestStallSeconds = "WSLBAK_TEST_STALL_SECONDS"
	// 從 distro 讀資料的速度上限（MiB／秒），讓一次備份久到來得及在途中做別的事。
	envTestReadRate = "WSLBAK_TEST_READ_MIB_PER_SECOND"
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

// testHoldBeforeMove 見 envTestHoldBeforeMove。
func testHoldBeforeMove() {
	path := os.Getenv(envTestHoldBeforeMove)
	if homeOverride == "" || path == "" {
		return
	}
	os.WriteFile(path+".reached", nil, 0o644)
	for i := 0; i < 600 && !fileExists(path); i++ {
		time.Sleep(100 * time.Millisecond)
	}
}

// currentStallLimit 是「多久沒有資料就算停住」；沙箱裡可以用環境變數縮短。
func currentStallLimit() time.Duration {
	if homeOverride != "" {
		if n, err := strconv.Atoi(os.Getenv(envTestStallSeconds)); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return stallLimit
}

// slowReader 把讀取的速度壓在一個上限以下。
type slowReader struct {
	r       io.Reader
	perByte time.Duration
}

func (s *slowReader) Read(p []byte) (int, error) {
	if len(p) > 64<<10 {
		p = p[:64<<10]
	}
	n, err := s.r.Read(p)
	time.Sleep(time.Duration(n) * s.perByte)
	return n, err
}

// withTestReadRate 在沙箱裡依環境變數放慢讀取；其他時候原樣傳回。
func withTestReadRate(r io.Reader) io.Reader {
	if homeOverride == "" {
		return r
	}
	rate, err := strconv.Atoi(os.Getenv(envTestReadRate))
	if err != nil || rate <= 0 {
		return r
	}
	return &slowReader{r: r, perByte: time.Second / time.Duration(rate<<20)}
}
