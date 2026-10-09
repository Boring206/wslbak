# wslbak

[English](README.md) | **繁體中文**

替 WSL distro 做排程備份：備份時不用停機、每次備份完自動試還原、失敗會通知，還原只要一行指令。

WSL 裡的檔案不在 OneDrive 與大多數備份工具的範圍內。它們全都放在一個虛擬磁碟裡，磁碟損壞或重灌
Windows 之後就什麼都不剩。常見的做法是排程執行 `wsl --export`，但 `wsl --export` 會先把 distro
終止再匯出，每跑一次，你開著的 shell、開發伺服器與容器就全部被關掉。`wslbak` 改成從 distro 裡面讀，
distro 照常執行：

```
> wslbak init
> wslbak run
備份 Ubuntu-24.04 → D:\WSLBackup\Ubuntu-24.04
  已寫入 20260115T030000Z.tar.gz（7.2 GB，1 分 48 秒）
  1 個檔案或目錄在讀取途中有變動（不停機備份的正常現象）。
試還原…
  試還原通過：抽查的 512 個檔案全部相符（1 分 33 秒）
完成。
```

- **distro 不會被停止。** 由 distro 裡以 root 執行的 `tar` 把內容串流出來；不會關閉 WSL，也不會寫入
  distro 裡的任何東西。
- **每份備份都試還原過。** 備份檔會被匯入成一個暫時的 distro，檢查後移除。
- **標準格式。** 備份就是一般的 `.tar.gz`，有沒有 wslbak 都能用 `wsl --import` 還原。
- **一個指令設定好**：每日排程、保留幾份、放在哪裡。
- **還原絕不覆蓋。** 永遠是在你現有的 distro 旁邊建立一個新的。

## 安裝

需要 Node.js 18 以上、Windows 10 或 11，以及從 Microsoft Store 安裝的 WSL（`wsl --version` 要能執行；
不行的話先執行 `wsl --update`）。在 Windows 的終端機或 WSL 裡安裝都可以：

```
npm install -g wslbak
```

套件內含編好的 Windows 執行檔（x64 與 arm64），不需要安裝 Go。裝在 WSL 裡時，同一支執行檔會透過
WSL 的 Windows 互通（interop）執行。

## 開始使用

```
wslbak init
```

`init` 會檢查你的 distro、建議一個存放位置，然後把即將建立的東西全部列出來（檔案、登錄機碼、
排程工作與它完整的指令列），再問你 `確定嗎？[y/N]`。回答「是」之前不會更動任何東西；加上 `--dry-run`
則只顯示同樣的計畫就結束。整個過程不需要系統管理員權限。

預設的存放位置是 `<磁碟>:\WSLBackup`，選的是 distro 所在磁碟以外、可用空間最多的內接磁碟，
這樣重灌 Windows 之後備份還在。放到外接碟或 NAS（`--dest`）還能防磁碟本身故障；
存放位置和 distro 在同一顆實體磁碟上時，`init` 會告訴你。

## 用法

```
wslbak init              設定備份與每日排程
wslbak run               立刻備份、試還原，並清掉過舊的備份
wslbak list              列出備份
wslbak status            排程、上次結果，以及已安裝的程式是否完好
wslbak verify [編號]     對既有的備份重新試還原（預設是最新一份）
wslbak restore [編號]    把備份還原成新的 distro（預設是最新一份通過試還原的）
wslbak uninstall         移除排程與已安裝的程式；備份不會被刪除

  -d, --distro <名稱>    指定 distro（預設是唯一可備份的那個）
      --dest <資料夾>    init：備份要放在哪裡
      --keep <份數>      init：保留最近幾份已驗證的備份（預設 7）
      --at <HH:MM>       init：每天幾點備份（預設 03:00）
      --webhook <網址>   init：失敗時另外通知這個網址
      --no-verify        run：這次不試還原
      --name <名稱>      restore：還原出來的 distro 要叫什麼
      --to <資料夾>      restore：還原出來的 distro 要放在哪裡
  -n, --dry-run          只列出會做什麼，不實際執行
  -y, --yes              不詢問直接進行
      --lang <語言>      介面語言：en 或 zh-TW
      --debug            顯示每個步驟的細節與耗時
```

結束碼：`0` 成功；`1` 備份已寫入但沒有驗證，或 `status` 發現備份過期；`2` 失敗；
`3` 已經有另一個 wslbak 在執行。

在 WSL 裡可以直接給 Linux 路徑（`--dest /mnt/d/WSLBackup`），會自動轉換。

## 還原

```
wslbak restore
```

會還原最新一份通過試還原的備份。原本名稱的 distro 如果還在，還原出來的會叫 `<名稱>-restored-<日期>`；
也可以用 `--name` 自己指定。既有的 distro 不會被覆蓋、更動或移除，還原出來的那個也不會被自動啟動。

**重灌 Windows 之後**不需要 Node 或 npm。每次備份都會在備份資料夾裡放一份程式與
`README-RESTORE.txt`。裝好 WSL 之後執行：

```
D:\WSLBackup\wslbak.exe restore
```

**完全不用 wslbak 也可以**，備份就是一般的封存檔：

```
wsl --import Ubuntu-24.04 C:\WSL\Ubuntu-24.04 D:\WSLBackup\Ubuntu-24.04\20260115T030000Z.tar.gz --version 2
```

手動匯入的 distro 會以 root 登入；在它的 `/etc/wsl.conf` 加上 `[user]` 與 `default=<你的使用者名稱>`
就能改回來。用 `wslbak restore` 的話這一步會幫你做好。

## 備份了什麼

distro 根檔案系統上的所有東西，包含擁有者、權限、延伸屬性、檔案 capabilities、硬連結與稀疏檔。
預設不備份：`/tmp/*`、`/var/tmp/*`，以及家目錄裡的 `.cache`。`/mnt` 底下的 Windows 磁碟不屬於 distro，
一律不會備份。

設定檔在 `%LOCALAPPDATA%\wslbak\config.json`：

```json
{
  "schema": 1,
  "at": "03:00",
  "verify": "restore",
  "notify": { "toast": true, "webhook": "", "on": "failure" },
  "distros": {
    "Ubuntu-24.04": {
      "id": "{…}",
      "dest": "D:\\WSLBackup",
      "keep": 7,
      "exclude": ["./home/*/Downloads/*"],
      "defaultExcludes": true,
      "enabled": true
    }
  }
}
```

- `exclude` 是 `tar --exclude` 的樣式，相對於 `/`，開頭要寫 `./`。
- `verify: "none"` 會關掉試還原；這時不分是否驗證過，保留最新的 `keep` 份。
- `notify.on: "always"` 連成功也通知，這樣沒收到通知就代表排程沒有在跑。
- 要加入另一個 distro，執行 `wslbak init -d <另一個 distro>`。所有 distro 共用同一個每日排程。

每份備份是 `<dest>\<distro>\` 裡的兩個檔案：`<編號>.tar.gz` 與 `<編號>.json`（大小、SHA-256、警告，
以及試還原的結果）。編號是備份當時的 UTC 時間。

## 試還原

備份寫好之後，wslbak 會用 `wsl --import` 把它匯入成一個暫時的 distro，在裡面執行檢查，再把它取消註冊。
檢查的內容是：預設使用者與家目錄都在，而且備份讀取過程中隨機挑出的 512 個檔案，SHA-256 和當時一樣。
在這之前，整個備份檔會先被重新讀過一遍，和寫入時記下的 SHA-256 比對。

這個暫時的 distro 不可以「活起來」：所有 WSL2 distro 共用同一個網路命名空間，一份忠實的複本一開機，
就會多跑一套你的服務與排程工作，還帶著你的憑證。所以 wslbak 在送去匯入的資料流結尾接上自己的
`/etc/wsl.conf`（磁碟上的備份檔不會被改動），關掉 systemd、開機指令、Windows 磁碟的掛載與互通；
而且除非確認最後留下來的是這一份設定，否則不會啟動複本。

只有通過驗證的備份才算進 `--keep`。沒能驗證的備份另外計算，最多留兩份，
所以連續幾次失敗也不會把好的備份擠掉。

## 通知

備份失敗或沒有驗證時，會跳出 Windows 通知。`--webhook <網址>` 可以再加一個管道：
[ntfy](https://ntfy.sh) 的主題會收到純文字訊息，Discord 與 Slack 的 webhook 網址會收到各自的 JSON 格式。
網址本身視為機密，畫面與紀錄檔只會顯示它的主機名稱。

上次成功已經超過兩天、排程工作不見了、或已安裝的程式消失時，`wslbak status` 會以結束碼 1 結束並說明原因。

## 安全措施

- wslbak 不會對你的 distro 執行 `wsl --shutdown`、`wsl --terminate` 或 `wsl --unregister`。
  程式裡唯一會取消註冊的地方只接受暫時的 distro：名稱是 wslbak 產生的、註冊的位置恰好是
  `%LOCALAPPDATA%\wslbak\verify` 底下屬於它的資料夾、而且 wslbak 替它留過認領檔。
  有一個測試專門確認沒有別的程式路徑能走到那個呼叫。
- 刪除舊備份時一次只刪一個檔案，而且只刪旁邊有 wslbak 的 manifest 的檔案。
  備份資料夾裡的其他檔案都不會動，就算看起來像備份也一樣。
- `restore` 會拒絕已經有人用的名稱，以及不是空的資料夾。
- 由其他程式管理的 distro（`docker-desktop*`、`rancher-desktop*`、`podman-machine-*`）與 WSL1 distro
  會被拒絕並說明原因；還沒設定之前，`wslbak status` 會把它們列出來。
- 同一時間只有一個 wslbak 在運作；第二個會以結束碼 3 結束。

## 運作方式

`wsl.exe -d <distro> -u root -e sh -s` 在 distro 裡執行一支小腳本。腳本對 `/` 執行 GNU `tar`
（加上 `--one-file-system`），把原始的封存寫到 stdout。Windows 這一端的 wslbak 用平行 gzip 壓縮、
計算雜湊，寫成 `<編號>.tar.gz.partial`；只有 tar 回報成功、而且收到的位元組數等於 tar 自己說它寫出的數量時，
才會改名成正式的檔案。

排程工作屬於你的 Windows 帳號，在你的登入工作階段裡執行。它啟動的是放在
`%LOCALAPPDATA%\Programs\wslbak` 的無視窗版程式，所以不依賴 Node，也不依賴 distro 裡的任何東西。
工作設定成錯過排定時間後盡快補跑，並在你登入十分鐘後再跑一次；
程式發現 20 小時內已經成功備份過，就會直接結束。

## 限制

- **備份當下正在寫入的檔案，在備份裡可能不一致。** 這不是快照。資料庫請用它自己的工具先匯出成檔案，
  那個檔案就能被可靠地備份。
- **POSIX ACL 不會被還原。** ACL 有存進備份檔，但 `wsl --import` 不會套用。`wslbak run`
  會告訴你有幾個檔案受影響（systemd 的日誌資料夾每次開機都會重設，不計入）。
- **每次都是完整備份。** 目前沒有增量或去重複的模式。
- **distro 裡要有 GNU tar。** Alpine 預設的 BusyBox tar 會被拒絕；請執行 `apk add tar`。
- **只備份根檔案系統上的東西。** 掛在另一個磁碟上的資料夾會被跳過，`wslbak run` 會指出是哪一個。
- **試還原需要可用空間**，位置是 `%LOCALAPPDATA%` 所在的磁碟，最多約等於 distro 裡檔案的總大小。
  空間不夠時，備份會保留並回報為「沒有驗證」，結束碼是 1。
- **排程只在你登入時執行。** distro 當時沒有在執行的話，會為了備份而被啟動。
- **試還原是抽查。** 它證明備份檔匯入得進去、512 個檔案完好，不是證明每個檔案的每個位元組都完好。
- 已在 Windows 11 x64、WSL 2.7、Ubuntu 與 Debian 上測試。Windows 10、arm64 版本與其他 distro
  還沒有試過，歡迎回報。

## 疑難排解

- **紀錄檔在哪裡？** `%LOCALAPPDATA%\wslbak\wslbak.log`，內容是英文。加上 `--debug` 可以即時看到。
- **Windows 拒絕執行程式。** 執行檔沒有程式碼簽章。智慧型應用程式控制（Smart App Control）
  會直接擋下沒有簽章的程式，開著它的時候 wslbak 無法執行；公司管理的電腦上，AppLocker 或 WDAC
  原則也可能有同樣的效果。
- **`status` 說已安裝的程式不見了。** 可能被防毒軟體隔離了。從防毒軟體的紀錄把它還原，
  再執行一次 `wslbak init` 把檔案補回來。
- **init 時出現「無法寫入…」。** 開啟了「受控資料夾存取」的話，請在 Windows 安全性裡允許這支程式，
  或換一個不受保護的資料夾。
- **備份很慢。** 如果防毒軟體會在寫入時掃描備份檔，把備份資料夾加入排除清單會有幫助。請不要關閉防毒軟體。
- **在 WSL 裡出現「無法執行 Windows 程式」。** Windows 互通被停用了；檢查 `/etc/wsl.conf` 的
  `[interop]` 區段，或改用 Windows 上的 Node 安裝 wslbak。

## 開發

需要 Go 1.25 以上與 Node.js。

```
npm test         # go vet 加上單元測試
npm run build    # 編出 bin/ 底下的四個執行檔
npm run e2e      # 端對端測試：在 WSL 裡對一個拋棄式的 distro 與沙箱資料夾執行
```

`npm run e2e` 第一次執行時會建立名為 `wslbak-e2e-<亂數>` 的 Debian distro，並留到下次再用；
`scripts/e2e-distro.sh destroy` 可以把它移除。測試不會碰其他的 distro，也不會碰你真正的 wslbak 設定。
在 WSL 裡開發而那裡沒有安裝 Go 時，建置腳本會退回使用 Windows 上的 `go.exe`；
要指定別的位置，設定環境變數 `GO`。

所有介面文字都在 `i18n.go`，每種語言各一份。改動訊息時兩份都要改，測試會檢查有沒有漏掉。

## 授權

[MIT](LICENSE)
