package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"
)

// Windows 的通知要有一個登錄過的應用程式識別碼（AUMID）才會顯示，沒登錄的會被默默丟掉。
// init 時在目前使用者的登錄檔裡登錄一次，uninstall 時移除。

const (
	toastAppID  = "Boring206.wslbak"
	toastAppKey = `Software\Classes\AppUserModelId\` + toastAppID
)

func registerToastApp() error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, toastAppKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue("DisplayName", "wslbak")
}

func unregisterToastApp() error {
	err := registry.DeleteKey(registry.CURRENT_USER, toastAppKey)
	if err == registry.ErrNotExist {
		return nil
	}
	return err
}

// toastScript 由 Windows PowerShell 5.1 執行；標題、內文與識別碼從環境變數讀，不拼進腳本裡。
// PowerShell 7 載入不了這些 WinRT 型別，所以一律指定 System32 底下的 5.1。
const toastScript = `
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[void][Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime]
[void][Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime]
$doc = New-Object Windows.Data.Xml.Dom.XmlDocument
$doc.LoadXml('<toast><visual><binding template="ToastGeneric"><text></text><text></text></binding></visual></toast>')
$nodes = $doc.GetElementsByTagName('text')
[void]$nodes.Item(0).AppendChild($doc.CreateTextNode($env:WSLBAK_TOAST_TITLE))
[void]$nodes.Item(1).AppendChild($doc.CreateTextNode($env:WSLBAK_TOAST_BODY))
$toast = New-Object Windows.UI.Notifications.ToastNotification $doc
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($env:WSLBAK_TOAST_APPID).Show($toast)
`

// toast 顯示一則 Windows 通知。
func toast(title, body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	exe := system32(`WindowsPowerShell\v1.0\powershell.exe`)
	cmd := exec.CommandContext(ctx, exe, "-NoProfile", "-NonInteractive", "-Command", "-")
	cmd.Dir = systemRoot()
	cmd.Env = append(os.Environ(),
		"WSLBAK_TOAST_TITLE="+title, "WSLBAK_TOAST_BODY="+body, "WSLBAK_TOAST_APPID="+toastAppID)
	cmd.Stdin = strings.NewReader(toastScript)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	cmd.WaitDelay = waitDelay
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("powershell: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// webhookHost 回傳網址的主機名稱。webhook 的網址本身就是密碼，畫面與紀錄檔裡只顯示主機。
func webhookHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "?"
	}
	return u.Hostname()
}

// webhookRequest 依網址決定訊息的格式：Discord 與 Slack 各有自己的 JSON，
// 其他的當成 ntfy 那一類「內文就是訊息、標題放在標頭」的服務。
func webhookRequest(raw, title, body string) (*http.Request, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	host := strings.ToLower(u.Hostname())
	post := func(contentType string, payload []byte) (*http.Request, error) {
		req, err := http.NewRequest(http.MethodPost, raw, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("User-Agent", "wslbak/"+version)
		return req, nil
	}
	switch {
	case host == "discord.com" || host == "discordapp.com" || strings.HasSuffix(host, ".discord.com"):
		payload, _ := json.Marshal(map[string]string{"content": title + "\n" + body})
		return post("application/json", payload)
	case host == "hooks.slack.com":
		payload, _ := json.Marshal(map[string]string{"text": title + "\n" + body})
		return post("application/json", payload)
	}
	req, err := post("text/plain; charset=utf-8", []byte(body))
	if err != nil {
		return nil, err
	}
	// 標頭只能放 ASCII；非英文的標題用 RFC 2047 編碼，ntfy 看得懂。
	req.Header.Set("Title", mime.QEncoding.Encode("utf-8", title))
	return req, nil
}

func sendWebhook(raw, title, body string) error {
	req, err := webhookRequest(raw, title, body)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// 錯誤訊息裡會帶著完整的網址，不能原樣寫進紀錄檔。
		return fmt.Errorf("request to %s failed", webhookHost(raw))
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s answered %s", webhookHost(raw), resp.Status)
	}
	return nil
}

// notify 把結果告訴使用者：Windows 通知，以及有設定的話再送一份到 webhook。
// 通知發不出去不影響備份本身的結果，只記在紀錄檔裡。
func notify(cfg *config, title, body string) {
	if cfg.Notify.Toast && toastAllowed() {
		if err := toast(title, body); err != nil {
			logf("toast: %v", err)
		}
	}
	if cfg.Notify.Webhook != "" {
		if err := sendWebhook(cfg.Notify.Webhook, title, body); err != nil {
			logf("webhook: %v", err)
		}
	}
}
