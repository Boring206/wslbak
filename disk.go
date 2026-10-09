package main

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

type volumeInfo struct {
	Root  string // 例如 D:\ 或 \\server\share\
	Free  uint64 // 目前使用者還能用的位元組數（考慮配額）
	FS    string // NTFS、exFAT、FAT32…
	Fixed bool   // 內接的固定磁碟
}

// existingAncestor 回傳 path 往上找到的第一個存在的資料夾：查可用空間要給一個存在的路徑。
func existingAncestor(path string) string {
	for {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		path = parent
	}
}

func volumeRoot(path string) string {
	return filepath.VolumeName(path) + `\`
}

// volumeOf 查 path 所在磁碟的資訊；path 本身可以還不存在。
func volumeOf(path string) (volumeInfo, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return volumeInfo{}, err
	}
	v := volumeInfo{Root: volumeRoot(abs)}
	dir, err := windows.UTF16PtrFromString(existingAncestor(abs))
	if err != nil {
		return v, err
	}
	var total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(dir, &v.Free, &total, &totalFree); err != nil {
		return v, err
	}
	root, err := windows.UTF16PtrFromString(v.Root)
	if err != nil {
		return v, err
	}
	var fs [windows.MAX_PATH + 1]uint16
	if windows.GetVolumeInformation(root, nil, 0, nil, nil, nil, &fs[0], uint32(len(fs))) == nil {
		v.FS = windows.UTF16ToString(fs[:])
	}
	v.Fixed = windows.GetDriveType(root) == windows.DRIVE_FIXED
	return v, nil
}

// fixedVolumes 列出所有內接固定磁碟上的磁碟區。
func fixedVolumes() []volumeInfo {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	var list []volumeInfo
	for i := 0; i < 26; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		v, err := volumeOf(string(rune('A'+i)) + `:\`)
		if err == nil && v.Fixed {
			list = append(list, v)
		}
	}
	return list
}

func sameVolume(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && strings.EqualFold(volumeRoot(absA), volumeRoot(absB))
}

// isWSLPath 回報路徑是不是在某個 distro 的檔案系統裡：備份不能放回被備份的地方。
func isWSLPath(path string) bool {
	p := normPath(path)
	return strings.HasPrefix(p, `\\wsl.localhost\`) || strings.HasPrefix(p, `\\wsl$\`)
}
