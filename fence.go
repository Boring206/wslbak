package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// 這個檔案是整個程式裡唯一會移除 distro、唯一會遞迴刪除資料夾的地方。
// 對象只有一種：試還原時我們自己建立的暫時 distro。
// fence_test.go 會檢查原始碼，確認 --unregister 與 os.RemoveAll 沒有出現在別處。

// verifyNameRe 是暫時 distro 的名稱：建立時間加上 64 位元的亂數。
var verifyNameRe = regexp.MustCompile(`^wslbak-verify-\d{8}T\d{6}Z-[0-9a-f]{16}$`)

// claimSuffix 是認領檔的副檔名。認領檔放在暫時 distro 的資料夾旁邊，
// 內容是 distro 的名稱，在匯入之前寫下，代表「這個名稱是 wslbak 建立的」。
const claimSuffix = ".claim"

func newVerifyName(now time.Time) (string, error) {
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return "wslbak-verify-" + now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(token[:]), nil
}

var (
	errFenceName     = errors.New("the name is not one wslbak generates")
	errFenceMissing  = errors.New("no distro with this name is registered")
	errFenceAmbigous = errors.New("more than one distro matches this name")
	errFencePath     = errors.New("the distro is not stored in wslbak's verify folder")
	errFenceClaim    = errors.New("there is no claim file for this distro")
)

// fenceAllows 判斷能不能移除名為 name 的 distro。四個條件全部成立才回傳 nil：
//  1. 名稱是我們產生的格式
//  2. 登錄檔裡恰好有一個 distro 叫這個名稱
//  3. 它的安裝位置「就是」root\name（整個路徑相等，不是只比開頭）
//  4. root 底下有它的認領檔
func fenceAllows(name string, distros []regDistro, root string, claimed bool) error {
	if !verifyNameRe.MatchString(name) {
		return errFenceName
	}
	var match *regDistro
	for i := range distros {
		if strings.EqualFold(distros[i].Name, name) {
			if match != nil {
				return errFenceAmbigous
			}
			match = &distros[i]
		}
	}
	if match == nil {
		return errFenceMissing
	}
	if match.Name != name || normPath(match.BasePath) != normPath(filepath.Join(root, name)) {
		return errFencePath
	}
	if !claimed {
		return errFenceClaim
	}
	return nil
}

func claimPath(root, name string) string {
	return filepath.Join(root, name+claimSuffix)
}

// writeClaim 在匯入之前留下認領檔。
func writeClaim(root, name string) error {
	if !verifyNameRe.MatchString(name) {
		return errFenceName
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	return os.WriteFile(claimPath(root, name), []byte(name+"\n"), 0o644)
}

func hasClaim(root, name string) bool {
	data, err := os.ReadFile(claimPath(root, name))
	return err == nil && strings.TrimSpace(string(data)) == name
}

// unregisterVerifyDistro 移除試還原用的暫時 distro，連同它的資料夾與認領檔。
// distro 沒有註冊（匯入失敗、或上次已移除）時只清資料夾。
func unregisterVerifyDistro(root, name string) error {
	if !verifyNameRe.MatchString(name) {
		return errFenceName
	}
	// 每次都重新讀登錄檔，緊接著才動手：名稱與位置要以當下為準。
	distros, err := readLxss()
	if err != nil {
		return err
	}
	switch err := fenceAllows(name, distros, root, hasClaim(root, name)); {
	case err == nil:
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out, err := runSystem(ctx, "", system32("wsl.exe"), "--unregister", name)
		if err != nil {
			return fmt.Errorf("wsl --unregister %s: %w: %s", name, err, strings.TrimSpace(decodeWSLText(out)))
		}
		debugf("unregistered %s", name)
	case errors.Is(err, errFenceMissing):
		// 沒有註冊，只剩資料夾要清。
	default:
		return fmt.Errorf("refusing to remove %s: %w", name, err)
	}
	return removeOwnedDir(root, name)
}

// removeOwnedDir 刪除 root\name 與它的認領檔。name 必須是我們產生的格式，
// 所以刪除的範圍永遠是 verify 資料夾裡的單一子資料夾。
func removeOwnedDir(root, name string) error {
	if !verifyNameRe.MatchString(name) || !filepath.IsAbs(root) {
		return errFenceName
	}
	if !hasClaim(root, name) {
		return errFenceClaim
	}
	if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
		return err
	}
	return os.Remove(claimPath(root, name))
}

// sweepStale 清掉上次沒有收乾淨的暫時 distro（例如試還原途中被強制關機）。
// 以認領檔為準：只處理我們確實建立過的名稱。
func sweepStale(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), claimSuffix)
		if !ok || e.IsDir() {
			continue
		}
		if err := unregisterVerifyDistro(root, name); err != nil {
			debugf("stale verify distro %s: %v", name, err)
		} else {
			debugf("removed stale verify distro %s", name)
		}
	}
}
