package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

// stuckAfter：握著鎖的那個 wslbak 跑了這麼久，就當它卡住了。
// 取和 dueAfter 一樣的值，隔天同一時間的排程才看得到（那時它剛好跑了將近 24 小時）。
const stuckAfter = dueAfter

// lockStampLen 是鎖檔裡那個時間的長度（RFC 3339、UTC、到秒）；長度固定，鎖檔的大小才不會變來變去。
const lockStampLen = len("2006-01-02T15:04:05Z")

func lockPath() (string, error) {
	state, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "wslbak.lock"), nil
}

// parseLockStamp 讀出鎖檔第 1 個位元組之後寫的開始時間；不是我們寫的格式就回傳 false。
func parseLockStamp(b []byte) (time.Time, bool) {
	if len(b) != lockStampLen {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, string(b))
	return t, err == nil
}

// lockHeldFor 回傳握著鎖的那個 wslbak 已經跑了多久。不知道時回傳 0：
// 鎖檔讀不到，或握著鎖的是不會寫開始時間的舊版。
func lockHeldFor() time.Duration {
	path, err := lockPath()
	if err != nil {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	// 第 0 個位元組被鎖住，讀不到；時間寫在它後面。
	buf := make([]byte, lockStampLen)
	if n, _ := f.ReadAt(buf, 1); n != lockStampLen {
		return 0
	}
	began, ok := parseLockStamp(buf)
	if !ok {
		return 0
	}
	return max(time.Since(began)+testLockAge(), 0)
}

// acquireLock 取得「同一時間只有一個 wslbak 在動備份」的鎖。
// held 為 true 表示已經有另一個在執行。鎖跟著檔案的 handle 走，行程不管怎麼結束都會放掉，
// 不會留下要人手動清掉的鎖檔。
func acquireLock() (release func(), held bool, err error) {
	file, err := lockPath()
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil, false, err
	}
	path, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return nil, false, err
	}
	h, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, false, err
	}
	var overlapped windows.Overlapped
	err = windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if err == windows.ERROR_LOCK_VIOLATION {
		windows.CloseHandle(h)
		return nil, true, nil
	}
	if err != nil {
		windows.CloseHandle(h)
		return nil, false, err
	}
	// 記下開始的時間，寫在鎖住的那個位元組後面：別的 wslbak 拿不到鎖時，靠它知道這一個跑了多久。
	// 寫不進去不影響鎖本身。
	var written uint32
	windows.WriteFile(h, []byte(time.Now().UTC().Format(time.RFC3339)), &written, &windows.Overlapped{Offset: 1})
	return func() {
		windows.UnlockFileEx(h, 0, 1, 0, &overlapped)
		windows.CloseHandle(h)
	}, false, nil
}

// takeLock 是各指令開頭共用的那一段：拿鎖，拿不到就說明原因。
// ok 為 false 時直接以 exit 結束。--dry-run 只看不動，不拿鎖，也就不會建立任何檔案。
func takeLock(opts options) (release func(), exit int, ok bool) {
	if opts.dryRun {
		return func() {}, 0, true
	}
	release, held, err := acquireLock()
	if err != nil {
		return nil, fail(err), false
	}
	if held {
		running := lockHeldFor()
		hours := int(running / time.Hour)
		if opts.scheduled {
			if running >= stuckAfter {
				// 上一次的備份到現在還沒結束：它不會自己發通知，之後每天的排程也都會在這裡退出，
				// 所以由這一次來說。
				logf("another wslbak has been running for %d hours and is probably stuck; no backup was made", hours)
				if cfg, err := loadConfig(); err == nil && cfg != nil {
					notify(cfg, fmt.Sprintf(T.NotifyStuckTitle, hours), T.NotifyStuckBody)
				}
				return nil, 2, false
			}
			// 手動的備份正在跑，排程這一次就不用做了。
			logf("another wslbak is running; the scheduled run exits")
			return nil, 0, false
		}
		fmt.Fprintln(os.Stderr, yellow(T.AlreadyRunning))
		if running >= stuckAfter {
			fmt.Fprintln(os.Stderr, yellow(fmt.Sprintf(T.AlreadyRunningLong, hours)))
		}
		return nil, 3, false
	}
	return release, 0, true
}
