package main

import (
	"fmt"
	"os"
	"sort"
	"time"
)

// maxUnverified 是沒有通過試還原的備份最多留幾份。它們不能算數，但留著最新的可以查原因。
const maxUnverified = 2

// retention 是保留規則：最新的 Last 份一定留；在那之外，每週、每月各再留最新的一份，
// 分別留 Weekly 週與 Monthly 個月。每週與每月的規則不會讓備份變小，它們的用處是
// 用差不多的份數涵蓋更長的時間。（讓備份變小的是差異備份，見 diff.go。）
//
// 每一份備份，不管是完整還是差異，都是一個還原點，都照這套規則算。
type retention struct {
	Last, Weekly, Monthly int
}

func (d *distroConfig) retention() retention {
	return retention{Last: d.Keep, Weekly: d.KeepWeekly, Monthly: d.KeepMonthly}
}

// backupTime 是備份的時間，從編號解析（編號就是 UTC 時間）。
func backupTime(m *manifest) time.Time {
	t, err := time.Parse(idLayout, m.ID)
	if err != nil {
		return m.Created
	}
	return t
}

// planRetention 決定哪些備份可以刪。
//
// 有做試還原時，只有通過的才算「好的」：好的依規則保留，沒通過的另外留最新的 maxUnverified 份。
// 兩種分開算，所以連續幾次驗證失敗不會把好的備份擠掉，最新一份好的也永遠不會被刪。
// 關掉試還原時（verify 不是 restore）不分通過與否，全部當成好的。
//
// 每週、每月的算法：由新到舊看過去，每遇到一個還沒留過的週（或月）就留下那一週最新的一份，
// 留滿指定的週數（或月數）為止。沒有備份的週不佔名額。
//
// 差異備份要和它的完整備份合起來才還原得出東西，所以只要還有一份被留下的「好的」差異依據著某份
// 完整備份，那份完整備份就一定留下，不管它自己排在第幾。沒通過檢查而留著查原因的差異不算：
// 不值得為了它多留一整份完整備份。反過來，完整備份不見了的差異不算好的備份。
func planRetention(backups []*manifest, pol retention, verify string) []*manifest {
	if pol.Last < 1 {
		pol.Last = 1
	}
	sorted := append([]*manifest{}, backups...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID > sorted[j].ID })

	keep := map[*manifest]bool{}
	var goodOnes []*manifest
	weeks, months := map[string]bool{}, map[string]bool{}
	good, bad := 0, 0
	for _, m := range sorted {
		if m.orphan() || (verify == verifyRestore && !m.verified()) {
			if bad++; bad <= maxUnverified {
				keep[m] = true
			}
			continue
		}
		goodOnes = append(goodOnes, m)
		if good++; good <= pol.Last {
			keep[m] = true
		}
		t := backupTime(m)
		year, week := t.ISOWeek()
		if w := fmt.Sprintf("%d-W%02d", year, week); !weeks[w] && len(weeks) < pol.Weekly {
			weeks[w] = true
			keep[m] = true
		}
		if mo := t.Format("2006-01"); !months[mo] && len(months) < pol.Monthly {
			months[mo] = true
			keep[m] = true
		}
	}
	for _, m := range goodOnes {
		if keep[m] && m.isDiff() {
			keep[m.base] = true
		}
	}
	var doomed []*manifest
	for _, m := range sorted {
		if !keep[m] {
			doomed = append(doomed, m)
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
		// 檔案索引與目錄清單是附屬的檔案，有就一起刪。
		os.Remove(m.indexPath())
		os.Remove(m.dirsPath())
		removed++
		logf("pruned %s", m.ID)
	}
	return removed
}
