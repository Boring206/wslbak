package main

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// acquireLock 取得「同一時間只有一個 wslbak 在動備份」的鎖。
// held 為 true 表示已經有另一個在執行。鎖跟著檔案的 handle 走，行程不管怎麼結束都會放掉，
// 不會留下要人手動清掉的鎖檔。
func acquireLock() (release func(), held bool, err error) {
	state, err := stateDir()
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(state, 0o755); err != nil {
		return nil, false, err
	}
	path, err := windows.UTF16PtrFromString(filepath.Join(state, "wslbak.lock"))
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
		if opts.scheduled {
			// 手動的備份正在跑，排程這一次就不用做了。
			logf("another wslbak is running; the scheduled run exits")
			return nil, 0, false
		}
		fmt.Fprintln(os.Stderr, yellow(T.AlreadyRunning))
		return nil, 3, false
	}
	return release, 0, true
}
