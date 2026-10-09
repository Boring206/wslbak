package main

import (
	"os"
	"sort"
)

// maxUnverified 是沒有通過試還原的備份最多留幾份。它們不能算數，但留著最新的可以查原因。
const maxUnverified = 2

// planPrune 決定哪些備份可以刪。
//
// 有做試還原時：通過的留最新的 keep 份，沒通過的留最新的 maxUnverified 份。
// 兩種分開算，所以連續幾次驗證失敗不會把好的備份擠掉，最新一份通過的也永遠不會被刪。
// 關掉試還原時（verify 不是 restore）：不分通過與否，留最新的 keep 份。
func planPrune(backups []*manifest, keep int, verify string) []*manifest {
	if keep < 1 {
		keep = 1
	}
	sorted := append([]*manifest{}, backups...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID > sorted[j].ID })

	var doomed []*manifest
	good, bad := 0, 0
	for _, m := range sorted {
		switch {
		case verify != verifyRestore || m.verified():
			if good++; good > keep {
				doomed = append(doomed, m)
			}
		default:
			if bad++; bad > maxUnverified {
				doomed = append(doomed, m)
			}
		}
	}
	return doomed
}

// applyPrune 刪除指定的備份：只刪 manifest 與它記載的那個封存，一次一個檔案。
// 這些 manifest 都是 readManifest 讀出來的，檔名已經確認是我們的格式。
func applyPrune(doomed []*manifest) (removed int) {
	for _, m := range doomed {
		// 先刪封存：刪不掉（例如檔案正被開著）就整份留到下次再試；
		// 刪掉之後 manifest 才刪失敗的話，剩下的 manifest 沒有封存，不會被當成一份備份。
		if err := os.Remove(m.archivePath()); err != nil {
			logf("prune %s: %v", m.ID, err)
			continue
		}
		if err := os.Remove(m.path()); err != nil {
			logf("prune %s: %v", m.ID, err)
		}
		removed++
		logf("pruned %s", m.ID)
	}
	return removed
}
