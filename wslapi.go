package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 還原出來的 distro 要把預設使用者設回備份當時的那一個；匯入的 distro 預設是 root。
// 用 wslapi.dll 直接指定 UID：每一版 WSL 都有這個介面，而且不需要啟動那個 distro
// （wsl --manage --set-default-user 要進到 distro 裡把名稱查成 UID，而且舊版沒有這個選項）。

// wslFlagsMask 是 WslConfigureDistribution 接受的旗標：互通、附加 Windows 的 PATH、掛載磁碟。
// 登錄檔裡的 Flags 另外帶著「這是 WSL2」的位元，傳進去會被當成無效參數。
const wslFlagsMask = 0x7

var procWslConfigure = windows.NewLazySystemDLL("wslapi.dll").NewProc("WslConfigureDistribution")

// wslConfigure 設定 distro 的預設 UID 與旗標。
func wslConfigure(name string, uid uint32, flags uint32) error {
	if err := procWslConfigure.Find(); err != nil {
		return err
	}
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	hr, _, _ := procWslConfigure.Call(uintptr(unsafe.Pointer(ptr)), uintptr(uid), uintptr(flags&wslFlagsMask))
	if int32(hr) < 0 {
		return fmt.Errorf("WslConfigureDistribution: HRESULT %#08x", uint32(hr))
	}
	return nil
}
