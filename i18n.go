package main

import (
	"strings"

	"golang.org/x/sys/windows"
)

// 介面文字集中在這個檔案：catalog 的每個欄位是一句話，zhTW 與 enUS 各有一份。
// 新增文字時兩份都要填，TestCatalogParity 會檢查有沒有漏掉，以及兩邊的格式參數是否一致。
// 需要調換參數順序時用 %[1]s 這種寫法。

type language int

const (
	langEN language = iota
	langZhTW
)

// envLang 是指定語言的環境變數；在 WSL 裡由啟動器轉成 --lang 參數。
const envLang = "WSLBAK_LANG"

// yesWords 是確認提示接受的肯定回答，不分介面語言。
var yesWords = []string{"y", "yes", "是", "好"}

// T 是目前使用的語言；由 setLanguage 在啟動時決定。
var T = &enUS

func setLanguage(l language) {
	if l == langZhTW {
		T = &zhTW
	} else {
		T = &enUS
	}
}

// parseLanguage 解析使用者明確指定的語言（--lang 或環境變數）。
func parseLanguage(s string) (language, bool) {
	tag := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "_", "-"))
	switch {
	case tag == "en" || strings.HasPrefix(tag, "en-"):
		return langEN, true
	case tag == "zh" || strings.HasPrefix(tag, "zh-"):
		return langZhTW, true
	}
	return langEN, false
}

// languageFromSystem 由 Windows 的顯示語言清單決定預設語言：
// 第一順位是繁體中文（台灣、香港、澳門）才用中文，其他一律英文。
func languageFromSystem(tags []string) language {
	if len(tags) == 0 {
		return langEN
	}
	tag := strings.ToLower(strings.ReplaceAll(tags[0], "_", "-"))
	if !strings.HasPrefix(tag, "zh") {
		return langEN
	}
	for _, mark := range []string{"hant", "-tw", "-hk", "-mo"} {
		if strings.Contains(tag, mark) {
			return langZhTW
		}
	}
	return langEN
}

func systemLanguageTags() []string {
	tags, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil {
		return nil
	}
	return tags
}

// pickLanguage 決定介面語言：--lang 參數優先（重複出現時以最後一個為準），
// 其次是環境變數，最後看系統語言。參數寫錯時先退回後面的來源，錯誤由 parseArgs 回報。
func pickLanguage(args []string, env string, system []string) language {
	chosen, found := langEN, false
	for i, a := range args {
		value := ""
		switch {
		case a == "--lang" && i+1 < len(args):
			value = args[i+1]
		case strings.HasPrefix(a, "--lang="):
			value = strings.TrimPrefix(a, "--lang=")
		default:
			continue
		}
		if l, ok := parseLanguage(value); ok {
			chosen, found = l, true
		}
	}
	if found {
		return chosen
	}
	if l, ok := parseLanguage(env); ok {
		return l
	}
	return languageFromSystem(system)
}

type catalog struct {
	Usage string

	// 參數錯誤
	ErrorWithHint     string // err
	ErrorLine         string // err
	UnknownFlag       string // 參數
	UnknownCommand    string // 指令
	NeedCommand       string
	FlagNotForCommand string // 旗標, 指令
	UnexpectedArg     string // 參數
	NeedValue         string // 旗標
	BadLang           string // 值
	BadKeep           string // 值
	BadTime           string // 值
	BadWebhook        string // 值
	BadDistroName     string // 名稱

	// 共用
	ListSep             string
	AskProceed          string
	DryRunNothingDone   string
	AlreadyRunning      string
	AlreadyRunningLong  string // 小時數
	NotSetUp            string
	NothingConfigured   string
	DistroNotConfigured string // distro
	WhichDistro         string // 清單
	NoSuchDistro        string // 名稱
	NoEligibleDistro    string
	DistroNotEligible   string // 名稱, 原因
	DistroGone          string // 名稱
	DistroReplaced      string // 名稱
	NeedStoreWSL        string
	PathInsideWSL       string // 路徑
	NoBackups           string // 資料夾
	NoSuchBackup        string // 編號

	// 不能備份的原因
	SkipOwn     string
	SkipName    string
	SkipBusy    string
	SkipWSL1    string
	SkipManaged string
	CanBackUp   string

	// 時間與長度
	JustNow         string
	MinutesAgo      string // 分鐘
	HoursAgo        string // 小時
	DaysAgo         string // 天
	DurationSeconds string // 秒
	DurationMinutes string // 分, 秒
	DurationHours   string // 時, 分

	// init
	InitProbing       string // distro
	ProbeFailed       string // distro, err
	InitNeedsPackage  string
	AskDest           string // 預設位置
	DestUnreachable   string // 路徑, err
	DestFAT           string // 磁碟, 格式
	DestNotWritable   string // 路徑, err
	InitPlanTitle     string
	InitLabels        [6]string
	InitDistroLine    string // 名稱, 系統, 已使用
	InitKeepLine      string // 份數
	InitAtLine        string // 時間
	InitVerifyLine    string // 磁碟, 大小
	InitVerifyOff     string
	InitNotifyToast   string
	InitNotifyWebhook string // 主機
	InitChangesTitle  string
	InitChangeLabels  [5]string
	InitTaskLine      string // 工作名稱, 程式, 參數
	InitWhatHappens   string
	InitNever         string
	WarnSameVolume    string // 磁碟
	WarnSameDisk      string // 備份的磁碟, distro 的磁碟
	WarnRemovable     string // 磁碟
	WarnOneDrive      string
	WarnDestSpace     string // 磁碟, 可用空間
	WarnVerifySpace   string // 磁碟, 可用空間
	InstallFailed     string // 資料夾, err
	TaskFailed        string // err
	TaskDescription   string
	InitDone          string // 時間
	AskRunNow         string
	InitRunHint       string

	// run 與 verify
	RunBackingUp      string // distro, 資料夾
	ProgressReading   string
	ProgressImporting string
	RunWritten        string // 檔名, 大小, 耗時
	RunWarnings       string // 數量
	RunSkippedMount   string // 掛載點
	RunDropped        string // 選項
	RunACLs           string // 數量
	RunVerifying      string
	RunVerifySkipped  string
	RunVerified       string // 檔案數, 耗時
	RunNotVerified    string // 原因
	RunVerifyFailed   string // 原因
	RunPruned         string // 數量
	RunDone           string
	DryRunWouldVerify string
	DryRunWouldPrune  string // 編號
	VerifyStart       string // 編號, distro

	// 備份失敗的原因
	BackupNotGNUTar   string
	BackupNoStream    string // 細節
	BackupWriteFailed string // 細節
	BackupStalled     string
	BackupTruncated   string
	BackupTarFailed   string // 細節

	// 試還原沒過或沒做的原因
	ReasonNoIndex  string
	ReasonEtc      string
	ReasonNoSpace  string
	ReasonChanged  string
	ReasonUnread   string
	ReasonImport   string
	ReasonNotInert string
	ReasonCheck    string
	ReasonUser     string
	ReasonSamples  string

	// 通知
	NotifyFailedTitle     string // distro
	NotifyStuckTitle      string // 小時數
	NotifyStuckBody       string
	NotifyUnverifiedTitle string // distro
	NotifyOKTitle         string // distro
	NotifyOKBody          string // 編號, 大小

	// list 與 status
	ListHeaders          [4]string
	LabelVerified        string
	LabelNotVerified     string
	LabelVerifyFailed    string
	StatusSchedule       string // 時間
	StatusNoTask         string
	StatusInstallMissing string // 路徑
	StatusDisabled       string
	StatusCounts         string // 總數, 已驗證, 保留
	StatusNeverSucceeded string
	StatusLastSuccess    string // 多久以前
	StatusStale          string // 多久以前
	StatusLastProblem    string // 多久以前, 訊息

	// restore
	RestorePlan       string // 編號, 原 distro, 大小, 狀態, 新名稱, 資料夾
	RestoreUnverified string
	RestoreNameTaken  string // 名稱
	RestoreTargetUsed string // 資料夾
	RestoreChecking   string
	RestoreCorrupt    string // 檔名
	RestoreImporting  string
	RestoreUserHint   string // UID
	RestoreDone       string // 名稱, 耗時
	RestoreNext       string // 名稱
	RestoreTwins      string // 原 distro

	// uninstall
	UninstallPlanTitle string
	UninstallTask      string // 名稱
	UninstallProgram   string // 資料夾
	UninstallRegistry  string // 機碼
	UninstallKeeps     string
	UninstallDone      string
	AskRemoveConfig    string // 資料夾

	BadCount            string // 值
	BadNotify           string // 值
	BadVerify           string // 值
	BadPattern          string // 值
	FlagConflict        string // 旗標, 旗標
	KeepWeeklyMore      string // 週數
	KeepMonthlyMore     string // 月數
	ConfigVerifyOn      string
	ConfigVerifyOff     string
	ConfigNotifyFailure string // 管道
	ConfigNotifyAlways  string // 管道
	ConfigKeep          string // 規則
	ConfigExcludes      string // 清單
	ConfigChanged       string // 旗標, 舊值, 新值
	ConfigNone          string
	ConfigNoChange      string
	ConfigSaved         string
	ConfigTaskUpdated   string // 時間
	ExcludeBuiltin      string // 樣式
	InitSkipped         string // distro, 原因
	AskPick             string
	WhichDistroOrAll    string // 清單

	DoctorOK            string
	DoctorLabels        [3]string
	DocSectionWSL       string
	DocSectionDistros   string
	DocSectionWindows   string
	DocSectionSchedule  string
	DocSectionBackups   string
	DocSectionSpace     string
	DocWSL              string // 版本
	DocDistroOK         string // 名稱, 系統, 已使用
	DocDistroSkip       string // 名稱, 原因
	DocDistroNotRunning string // 名稱
	DocTarBad           string // 名稱
	DocTarFix           string
	DocNoSHA            string // 名稱
	DocNoSHAFix         string
	DocSACOn            string
	DocSACFix           string
	DocSACEval          string
	DocSACOff           string
	DocCFAOn            string
	DocCFAFix           string
	DocCFAOff           string
	DocVerifyAttr       string // 資料夾
	DocVerifyAttrFix    string
	DocInstalled        string // 資料夾
	DocDestOK           string // 資料夾, 磁碟, 可用空間
	DocDestShared       string // 資料夾
	DocDestSharedFix    string
	PrivatePlan         string // 資料夾
	PrivateDone         string // 資料夾
	PrivateForeign      string // 資料夾, 名稱
	PrivateFailed       string // 資料夾, 錯誤
	PrivateStillShared  string // 資料夾
	PrivateNote         string
	DocDestMissing      string // 資料夾
	DocDestMissingFix   string
	DocDestLow          string // 磁碟, 可用空間, 需要
	DocLastOK           string // 名稱, 多久以前
	DocLastNever        string // 名稱
	DocLastStale        string // 名稱, 多久以前
	DocStaleFix         string
	DocLastProblem      string // 名稱, 訊息
	DocSeeLog           string // 路徑
	DocCache            string // distro, 大小, 路徑
	DocCacheFix         string // distro, 樣式
	DocDocker           string // distro, 大小
	DocNoCaches         string
	DocSummaryOK        string
	DocSummaryWarn      string // 數量
	DocSummaryFail      string // 數量

	RunPruneSkipped string

	BadPath            string // 路徑
	FilesBuildingIndex string
	FilesNoIndex       string // 編號, err
	FilesTitle         string // 編號, distro, 路徑
	FilesNotFound      string // 路徑, 編號
	FilesFindNone      string // 文字, 編號

	WarmUpFailed    string // 路徑
	WarmUpSlow      string // 耗時
	DocAntivirus    string // 名稱
	DocAntivirusFix string // 資料夾

	FlagNeeds          string // 旗標, 旗標
	BadInto            string // 值
	PathRoot           string
	PathDistroGone     string // distro
	PathWrongDistro    string // 備份編號, 紀錄裡的 distro, 資料夾所屬的 distro
	PathPlan           string // 數量, 大小, 編號, distro, 資料夾
	PathTargetUsed     string // 資料夾
	PathTargetNotDir   string // 路徑
	PathTargetNoParent string // 資料夾
	PathPrepareFailed  string // 資料夾, 代碼
	PathExtracting     string
	ProgressScanning   string
	PathFailed         string // 細節
	PathMoveFailed     string // 目的地, 暫存資料夾
	WindowsPathGiven   string // 值
	PathArchiveChanged string // 檔名
	PathDone           string // 數量, 資料夾
	PathExplorer       string // 路徑

	BriefOther    string
	BriefNoStream string
	BriefWrite    string
	BriefTar      string
	BriefSeeLog   string

	BackupNoTar   string
	DocTarMissing string // 名稱

	// 放在備份資料夾裡的說明檔；兩種語言都會寫進去。
	RestoreReadme string
}

var zhTW = catalog{
	Usage: `用法：wslbak <指令> [選項]

指令：
  init             設定備份：選 distro、目的地、保留份數，並建立每日排程
  config           顯示目前的設定；加上選項則修改設定
  run              立刻備份一次（備份後自動試還原，再清掉過舊的備份）
  list             列出現有的備份
  files [編號] [路徑]  列出備份裡某個資料夾的內容（預設是最新一份的根目錄）
  status           顯示排程、上次結果，以及已安裝的執行檔是否完好
  doctor           逐項檢查環境與設定，找出備份跑不起來的原因與修法
  verify [編號]    對既有的備份重新試還原（預設是最新一份）
  restore [編號]   把備份還原成新的 distro（預設是最新一份通過試還原的，沒有就用最新一份；不會覆蓋既有的 distro）
  uninstall        移除排程與已安裝的執行檔；備份不會被刪除

選項：
  -d, --distro <名稱>      指定 distro（預設是唯一可備份的那個）
      --all                init：一次設定所有可備份的 distro
      --dest <資料夾>      init：備份要放在哪裡
      --keep <份數>        init、config：保留最新幾份已驗證的備份（預設 7）
      --keep-weekly <週>   init、config：另外每週留一份，留幾週（預設 0）
      --keep-monthly <月>  init、config：另外每月留一份，留幾個月（預設 0）
      --at <HH:MM>         init、config：每天幾點備份（預設 03:00）
      --webhook <網址>     init、config：失敗時另外通知這個網址（ntfy、Discord、Slack）；config 可用 off 取消
      --notify <時機>      config：failure 只在失敗時通知，always 每次都通知
      --verify <方式>      config：restore 每次備份後試還原，none 不做
      --exclude <樣式>     config：多排除一個路徑樣式，例如 /home/*/Downloads/*（可重複）
      --unexclude <樣式>   config：取消一個排除（可重複）
      --enable, --disable  config：啟用或停用某個 distro 的備份
      --private            config：把備份資料夾收緊成只有你的帳號能存取
      --no-verify          run：這次不試還原
      --find <文字>        files：列出檔名或路徑包含這段文字的項目
      --name <名稱>        restore：還原出來的 distro 要叫什麼
      --to <資料夾>        restore：還原出來的 distro 要放在哪裡
      --path <路徑>        restore：只取回備份裡的這個檔案或資料夾（可重複），要搭配 --into
      --into <資料夾>      restore：取回的檔案放進 distro 裡的這個資料夾（必須不存在或是空的）
  -n, --dry-run            只列出會做什麼，不實際執行
  -y, --yes                不詢問直接進行
      --lang <語言>        介面語言：en 或 zh-TW（也可以設定環境變數 WSLBAK_LANG）
      --debug              顯示每個步驟的細節與耗時
  -h, --help               顯示這份說明
  -v, --version            顯示版本

結束碼：0 成功；1 備份已寫入但未驗證，或備份已過期；2 失敗；3 已有另一個 wslbak 在執行
`,

	ErrorWithHint:         "wslbak：%v\n執行 wslbak --help 查看用法。\n",
	ErrorLine:             "wslbak：%v",
	UnknownFlag:           "不認得的參數：%s",
	UnknownCommand:        "不認得的指令：%s",
	NeedCommand:           "要指定一個指令，例如 wslbak run",
	FlagNotForCommand:     "%[1]s 不能用在 %[2]s 指令",
	UnexpectedArg:         "多出來的參數：%s",
	NeedValue:             "%s 後面要接一個值",
	BadLang:               "不支援的語言：%s（可用 en 或 zh-TW）",
	BadKeep:               "保留份數要是 1 以上的整數：%s",
	BadTime:               "時間要寫成 24 小時制的 HH:MM：%s",
	BadWebhook:            "webhook 要是 http 或 https 開頭的網址：%s",
	BadDistroName:         "distro 名稱只能用英文字母、數字、句點、底線與連字號：%s",
	ListSep:               "、",
	AskProceed:            "確定嗎？[y/N] ",
	DryRunNothingDone:     "（--dry-run：以上都沒有實際執行）",
	AlreadyRunning:        "已經有另一個 wslbak 在執行（可能是排程的備份）。等它結束後再試。",
	AlreadyRunningLong:    "它已經執行了 %d 小時，很可能卡住了。重新開機（或登出再登入）會結束它。",
	NotSetUp:              "還沒有設定備份。執行 wslbak init 開始設定。",
	NothingConfigured:     "沒有啟用備份的 distro。執行 wslbak init 設定。",
	DistroNotConfigured:   "還沒有替 %[1]s 設定備份。執行 wslbak init -d %[1]s",
	WhichDistro:           "有不只一個 distro，請用 -d 指定：%s",
	NoSuchDistro:          "沒有名為 %s 的 distro。",
	NoEligibleDistro:      "沒有可以備份的 distro。執行 wslbak status 查看原因。",
	DistroNotEligible:     "不能備份 %[1]s：%[2]s",
	DistroGone:            "找不到 distro %s（可能已經被移除）。",
	DistroReplaced:        "%[1]s 已經不是當初設定的那個 distro（同名，但重新安裝過）。確認之後重新執行 wslbak init -d %[1]s",
	NeedStoreWSL:          "需要從 Microsoft Store 安裝的 WSL（wsl --version 要能執行）。請先執行 wsl --update。",
	PathInsideWSL:         "%s 在 WSL 的檔案系統裡。請改用 Windows 磁碟上的資料夾。",
	NoBackups:             "%s 裡沒有備份。",
	NoSuchBackup:          "找不到編號 %s 的備份。執行 wslbak list 查看有哪些。",
	SkipOwn:               "wslbak 自己的暫時 distro",
	SkipName:              "名稱含有不支援的字元",
	SkipBusy:              "正在安裝、匯入或移除中",
	SkipWSL1:              "是 WSL1（只支援 WSL2）",
	SkipManaged:           "由 Docker Desktop 這類程式自行管理",
	CanBackUp:             "可以備份",
	JustNow:               "剛剛",
	MinutesAgo:            "%d 分鐘前",
	HoursAgo:              "%d 小時前",
	DaysAgo:               "%d 天前",
	DurationSeconds:       "%d 秒",
	DurationMinutes:       "%d 分 %d 秒",
	DurationHours:         "%d 小時 %d 分",
	InitProbing:           "正在檢查 %s…",
	ProbeFailed:           "無法在 %[1]s 裡執行檢查：%[2]v",
	InitNeedsPackage:      "init 要從安裝好的 wslbak 執行（備份資料夾裡的那一份只用來還原，旁邊沒有排程要用的 wslbakw.exe）。",
	AskDest:               "備份要放在哪裡？直接按 Enter 使用 %s：",
	DestUnreachable:       "無法使用 %[1]s：%[2]v",
	DestFAT:               "%[1]s 是 %[2]s 格式，單一檔案不能超過 4 GB，放不下備份。",
	DestNotWritable:       "無法寫入 %[1]s：%[2]v\n如果開啟了「受控資料夾存取」，請允許 wslbak 或換一個資料夾。",
	InitPlanTitle:         "即將設定：",
	InitLabels:            [6]string{"要備份的 distro", "備份放在", "保留", "排程", "試還原", "失敗通知"},
	InitDistroLine:        "%[1]s（%[2]s，已使用 %[3]s）",
	InitKeepLine:          "最近 %d 份通過試還原的備份",
	InitAtLine:            "每天 %s（當時沒開機的話，下次登入後補跑）",
	InitVerifyLine:        "每次備份後（暫時需要 %[1]s 上最多約 %[2]s 的可用空間）",
	InitVerifyOff:         "不做（設定檔裡的 verify 是 none）",
	InitNotifyToast:       "Windows 通知",
	InitNotifyWebhook:     "Windows 通知，以及 %s 的 webhook",
	InitChangesTitle:      "會建立或修改：",
	InitChangeLabels:      [5]string{"程式", "設定", "排程", "登錄", "還原包"},
	InitTaskLine:          "工作排程器的「%[1]s」，執行 %[2]s %[3]s",
	InitWhatHappens:       "每次備份會做的事：在 distro 裡以 root 讀取所有檔案並打包（distro 照常執行，不會被寫入）；\n把備份匯入成一個暫時的 distro，檢查後移除；刪掉超過保留份數的舊備份。\ndistro 當時沒有在執行的話會被啟動。",
	InitNever:             "不會做的事：不會關閉 WSL 或停止 distro、不會修改或移除任何既有的 distro、不需要系統管理員權限。",
	WarnSameVolume:        "注意：備份和 distro 放在同一個磁碟區（%s）。重灌 Windows 或磁碟故障時，備份會一起消失。",
	WarnSameDisk:          "注意：%[1]s 和 distro 所在的 %[2]s 是同一顆實體磁碟。磁碟本身故障的話備份會一起消失；要防這種情況，請把備份放到外接碟或 NAS。",
	WarnRemovable:         "注意：%s 不是內接磁碟。備份時它沒有接上的話會失敗並通知你。",
	WarnOneDrive:          "注意：這個資料夾在 OneDrive 裡，每份備份（可能有好幾 GB）都會被上傳。",
	WarnDestSpace:         "注意：%[1]s 只剩 %[2]s，可能放不下備份。",
	WarnVerifySpace:       "注意：%[1]s 只剩 %[2]s，可能不夠做試還原；不夠的時候會略過試還原並通知你。",
	InstallFailed:         "無法把程式複製到 %[1]s：%[2]v",
	TaskFailed:            "建立排程工作失敗：%v",
	TaskDescription:       "wslbak：每天備份 WSL distro，並試還原確認備份可用。",
	InitDone:              "設定完成。之後每天 %s 會自動備份。",
	AskRunNow:             "現在就備份一次嗎？[y/N] ",
	InitRunHint:           "要立刻備份一次，執行 wslbak run。",
	RunBackingUp:          "備份 %[1]s → %[2]s",
	ProgressReading:       "讀取中…",
	ProgressImporting:     "匯入暫時的 distro…",
	RunWritten:            "已寫入 %[1]s（%[2]s，%[3]s）",
	RunWarnings:           "%d 個檔案或目錄在讀取途中有變動（不停機備份的正常現象）。",
	RunSkippedMount:       "沒有備份 %s：它掛在另一個磁碟上，不屬於這個 distro 的根檔案系統。",
	RunDropped:            "這個 distro 的 tar 不支援 %s，對應的屬性沒有備份。",
	RunACLs:               "%d 個檔案帶有 POSIX ACL：已存進備份，但 WSL 匯入時不會套用，還原後要自己重設。",
	RunVerifying:          "試還原…",
	RunVerifySkipped:      "這次略過了試還原（--no-verify）。",
	RunVerified:           "試還原通過：抽查的 %[1]d 個檔案全部相符（%[2]s）",
	RunNotVerified:        "已寫入，但沒有試還原：%s",
	RunVerifyFailed:       "試還原沒有通過：%s",
	RunPruned:             "已刪除 %d 份過舊的備份。",
	RunDone:               "完成。",
	DryRunWouldVerify:     "備份後會試還原",
	DryRunWouldPrune:      "會刪除舊的備份 %s",
	VerifyStart:           "試還原 %[1]s（%[2]s）…",
	BackupNotGNUTar:       "這個 distro 的 tar 不是 GNU tar，wslbak 目前只支援 GNU tar。Alpine 可以執行 apk add tar 安裝。",
	BackupNoStream:        "無法從 distro 取得資料：%s",
	BackupWriteFailed:     "寫入備份檔失敗：%s",
	BackupStalled:         "備份中途超過 10 分鐘沒有收到任何資料，已經放棄。",
	BackupTruncated:       "備份的資料沒有完整送達（distro 可能在中途被關閉）。",
	BackupTarFailed:       "tar 回報錯誤：%s",
	ReasonNoIndex:         "wslbak 讀不懂這份 tar 的結構，無法試還原",
	ReasonEtc:             "這個 distro 的 /etc 不是一般的目錄，無法安全地試還原",
	ReasonNoSpace:         "放暫時 distro 的磁碟空間不夠",
	ReasonChanged:         "備份檔和寫入當時不一樣了（可能已損壞）",
	ReasonUnread:          "備份檔讀不出來（可能已損壞）",
	ReasonImport:          "wsl --import 失敗（細節在紀錄檔裡）",
	ReasonNotInert:        "無法確保暫時的 distro 不會啟動服務，所以沒有啟動它",
	ReasonCheck:           "檢查沒有跑完",
	ReasonUser:            "還原出來的 distro 裡找不到原本的預設使用者或家目錄",
	ReasonSamples:         "還原出來的檔案內容和備份時不同",
	NotifyFailedTitle:     "wslbak：%s 備份失敗",
	NotifyStuckTitle:      "wslbak：有一次備份已經執行了 %d 小時",
	NotifyStuckBody:       "它很可能卡住了；在它結束之前，新的備份無法開始。請重新開機（或登出再登入），然後執行 wslbak run。",
	NotifyUnverifiedTitle: "wslbak：%s 的備份沒有驗證",
	NotifyOKTitle:         "wslbak：%s 備份完成",
	NotifyOKBody:          "%[1]s（%[2]s）已通過試還原。",
	ListHeaders:           [4]string{"編號", "時間", "大小", "狀態"},
	LabelVerified:         "已驗證",
	LabelNotVerified:      "未驗證",
	LabelVerifyFailed:     "驗證失敗",
	StatusSchedule:        "排程：每天 %s",
	StatusNoTask:          "排程工作不見了。重新執行 wslbak init 可以建回來。",
	StatusInstallMissing:  "找不到 %s：排程無法執行。它可能被防毒軟體隔離了；重新執行 wslbak init 可以補回來。",
	StatusDisabled:        "已停用",
	StatusCounts:          "%[1]d 份備份，其中 %[2]d 份通過試還原（保留 %[3]d 份）",
	StatusNeverSucceeded:  "還沒有成功備份過。",
	StatusLastSuccess:     "上次成功：%s",
	StatusStale:           "備份過期了：上次成功是 %s。",
	StatusLastProblem:     "最近一次（%[1]s）的問題：%[2]s",
	RestorePlan:           "將把備份 %[1]s（%[2]s，%[3]s，%[4]s）還原成新的 distro %[5]s，放在 %[6]s\n現有的 distro 不會被更動。",
	RestoreUnverified:     "注意：這份備份沒有通過試還原。",
	RestoreNameTaken:      "已經有名為 %s 的 distro。wslbak 不會覆蓋既有的 distro，請用 --name 指定別的名稱。",
	RestoreTargetUsed:     "%s 已經存在而且不是空的。請用 --to 指定別的資料夾。",
	RestoreChecking:       "正在核對備份檔…",
	RestoreCorrupt:        "%s 和備份當時不一樣了（可能已損壞），沒有還原。可以用 wslbak restore <編號> 改用別份。",
	RestoreImporting:      "正在匯入…",
	RestoreUserHint:       "無法設定預設使用者（UID %d），目前會以 root 登入。請在 distro 的 /etc/wsl.conf 加上 [user] 區段與 default=你的使用者名稱。",
	RestoreDone:           "已還原成 distro %[1]s（%[2]s）。",
	RestoreNext:           "進入：    wsl -d %[1]s\n設為預設：wsl --set-default %[1]s",
	RestoreTwins:          "原本的 %s 還在。兩個 distro 的服務設定相同，同時執行時可能會搶用同樣的 port。",
	UninstallPlanTitle:    "即將移除：",
	UninstallTask:         "排程工作 %s",
	UninstallProgram:      "%s 裡的程式",
	UninstallRegistry:     "登錄機碼 %s",
	UninstallKeeps:        "備份不會被刪除，仍然留在：",
	UninstallDone:         "已移除排程與程式。備份都還在。",
	AskRemoveConfig:       "也要刪除設定與紀錄（%s）嗎？[y/N] ",

	BadCount:            "份數要是 0 以上的整數：%s",
	BadNotify:           "--notify 只能是 failure 或 always：%s",
	BadVerify:           "--verify 只能是 restore 或 none：%s",
	BadPattern:          "排除樣式要寫成 distro 裡的絕對路徑，例如 /home/*/Downloads/*：%s",
	FlagConflict:        "%[1]s 和 %[2]s 不能同時使用",
	KeepWeeklyMore:      "，另外每週一份、留 %d 週",
	KeepMonthlyMore:     "，每月一份、留 %d 個月",
	ConfigVerifyOn:      "試還原：每次備份後",
	ConfigVerifyOff:     "試還原：不做",
	ConfigNotifyFailure: "通知：失敗時（%s）",
	ConfigNotifyAlways:  "通知：每次備份後（%s）",
	ConfigKeep:          "保留：%s",
	ConfigExcludes:      "排除：%s",
	ConfigChanged:       "%[1]s：%[2]s → %[3]s",
	ConfigNone:          "（無）",
	ConfigNoChange:      "設定沒有變動。",
	ConfigSaved:         "已儲存。",
	ConfigTaskUpdated:   "排程工作已改成每天 %s。",
	ExcludeBuiltin:      "%s 一律排除，不能取消。",
	InitSkipped:         "跳過 %[1]s：%[2]s",
	AskPick:             "要設定哪一個？輸入編號，或輸入 a 全部設定：",
	WhichDistroOrAll:    "有不只一個 distro：用 -d 指定其中一個，或用 --all 全部設定：%s",
	DoctorOK:            "正常",
	DoctorLabels:        [3]string{"資訊", "注意", "有問題"},
	DocSectionWSL:       "WSL",
	DocSectionDistros:   "Distro",
	DocSectionWindows:   "這台電腦的設定",
	DocSectionSchedule:  "排程與程式",
	DocSectionBackups:   "備份",
	DocSectionSpace:     "可以省空間的地方",
	DocWSL:              "WSL %s（Microsoft Store 版）",
	DocDistroOK:         "%[1]s：可以備份（%[2]s，已使用 %[3]s）",
	DocDistroSkip:       "%[1]s：不備份，%[2]s",
	DocDistroNotRunning: "%s：可以備份；目前沒有在執行，所以沒有進去檢查",
	DocTarBad:           "%s：裡面的 tar 不是 GNU tar",
	DocTarFix:           "在那個 distro 裡安裝 GNU tar。Alpine：apk add tar",
	DocNoSHA:            "%s：裡面沒有 sha256sum，試還原時無法比對檔案內容",
	DocNoSHAFix:         "在那個 distro 裡安裝 coreutils",
	DocSACOn:            "智慧型應用程式控制是開啟的：沒有簽章的程式會被擋下，排程的備份無法執行",
	DocSACFix:           "wslbak 的執行檔沒有程式碼簽章。要用的話，只能在「Windows 安全性 → 應用程式與瀏覽器控制」關閉智慧型應用程式控制；關閉之後無法再開啟。",
	DocSACEval:          "智慧型應用程式控制在評估模式：Windows 之後可能自行開啟它，到時沒有簽章的 wslbak 會被擋下",
	DocSACOff:           "智慧型應用程式控制沒有開啟",
	DocCFAOn:            "受控資料夾存取是開啟的",
	DocCFAFix:           "備份資料夾如果在受保護的位置（例如「文件」），要在「Windows 安全性 → 勒索軟體防護」允許 wslbak.exe 與 wslbakw.exe",
	DocCFAOff:           "受控資料夾存取沒有開啟",
	DocVerifyAttr:       "%s 設了壓縮或加密，wsl --import 可能無法在裡面建立虛擬磁碟",
	DocVerifyAttrFix:    "在檔案總管的「內容 → 進階」取消這個資料夾的壓縮與加密",
	DocInstalled:        "排程用的程式在 %s，檔案完好",
	DocDestOK:           "%[1]s 可以使用，%[2]s 還有 %[3]s",
	DocDestShared:       "這台電腦上的其他帳號也讀得到 %s。備份沒有加密，裡面是 distro 的全部檔案，金鑰也在內；只有你一個人用這台電腦的話可以不管。",
	DocDestSharedFix:    "和別人共用電腦時，收緊成只有你的帳號能存取：wslbak config --private",
	PrivatePlan:         "會把 %s 的權限改成只有你的帳號能存取（另外保留系統與系統管理員）。",
	PrivateDone:         "%s 現在只有你的帳號打得開（另外保留系統與系統管理員）。",
	PrivateForeign:      "%[1]s 裡還有不是 wslbak 放的東西（%[2]s），所以不去動它的權限。請把備份改放到專用的資料夾，或自己調整權限。",
	PrivateFailed:       "無法更改 %[1]s 的權限：%[2]v",
	PrivateStillShared:  "已經設定 %s 的權限，但其他帳號仍然讀得到；這個磁碟可能不支援權限設定。",
	PrivateNote:         "注意：重灌 Windows 之後，新的帳號不在名單上。還原之前，先在檔案總管打開那個資料夾並同意它的詢問，或用「以系統管理員身分執行」的終端機。",
	DocDestMissing:      "連不到 %s",
	DocDestMissingFix:   "如果是外接碟或網路磁碟，接上之後再試",
	DocDestLow:          "%[1]s 只剩 %[2]s，下一份備份大約需要 %[3]s",
	DocLastOK:           "%[1]s：上次成功是 %[2]s",
	DocLastNever:        "%s：還沒有成功備份過",
	DocLastStale:        "%[1]s：備份過期了，上次成功是 %[2]s",
	DocStaleFix:         "確認電腦在排定的時間是開著而且有登入；也可以手動執行 wslbak run",
	DocLastProblem:      "%[1]s：最近一次的問題：%[2]s",
	DocSeeLog:           "細節在紀錄檔：%s",
	DocCache:            "%[1]s：%[3]s 佔了 %[2]s，是可以重新下載的快取",
	DocCacheFix:         "不想備份它的話：wslbak config -d %[1]s --exclude \"%[2]s\"",
	DocDocker:           "%[1]s：Docker 的資料（/var/lib/docker）佔了 %[2]s。映像可以重新下載，但 volume 裡的資料不行，要不要排除請自行判斷",
	DocNoCaches:         "沒有發現值得排除的大型快取",
	DocSummaryOK:        "沒有發現問題。",
	DocSummaryWarn:      "有 %d 項要注意。",
	DocSummaryFail:      "有 %d 項問題要處理。",
	RunPruneSkipped:     "這個資料夾裡有較新版本的 wslbak 寫的備份，這次沒有刪除任何舊備份。",
	BadPath:             "路徑要寫成 distro 裡的位置（例如 /home/me/project），而且不能有 ..：%s",
	FilesBuildingIndex:  "這份備份還沒有檔案索引，正在掃描備份檔來建立（只需要做一次）…",
	FilesNoIndex:        "無法讀取備份 %[1]s 的內容：%[2]v",
	FilesTitle:          "備份 %[1]s（%[2]s）裡的 %[3]s",
	FilesNotFound:       "%[1]s 不在備份 %[2]s 裡。用 wslbak files --find <文字> 可以搜尋。",
	FilesFindNone:       "備份 %[2]s 裡沒有名稱包含「%[1]s」的項目。",
	WarmUpFailed:        "剛安裝的程式啟動不了：%s\n它可能被防毒軟體擋下或隔離了。請在防毒軟體裡放行這個檔案（或把它所在的資料夾加入例外），再執行一次 wslbak init。",
	WarmUpSlow:          "防毒軟體花了 %s 檢查新安裝的程式；這只會發生在每個新版本第一次執行時。",
	DocAntivirus:        "啟用中的防毒軟體：%s。新版的 wslbak 第一次執行時可能被它扣住檢查，甚至關進沙箱。",
	DocAntivirusFix:     "排程的備份如果一直沒有執行，把 %s 加入防毒軟體的例外清單",
	FlagNeeds:           "%[1]s 要和 %[2]s 一起使用",
	BadInto:             "--into 要是 distro 裡的絕對路徑，例如 /home/me/restored：%s",
	PathRoot:            "要取回整個 distro 的話，直接用 wslbak restore，不要加 --path。",
	PathDistroGone:      "要把檔案放回 %s，但這個 distro 已經不在了。可以先用 wslbak restore 把整個 distro 還原回來。",
	PathWrongDistro:     "備份 %[1]s 的紀錄寫著它屬於 %[2]s，卻放在 %[3]s 的備份資料夾裡。為了不把檔案放進錯的 distro，這裡不繼續。",
	PathPlan:            "將從備份 %[3]s 取回 %[1]d 個項目（共 %[2]s），放到 %[4]s 裡的 %[5]s\n完整的路徑會在那個資料夾底下重建，不會覆蓋任何現有的檔案。",
	PathTargetUsed:      "%s 已經存在而且不是空的。請用 --into 指定一個不存在、或是空的資料夾。",
	PathTargetNotDir:    "%s 已經存在，而且不是資料夾。",
	PathTargetNoParent:  "%s 的上一層資料夾不存在。",
	PathPrepareFailed:   "無法在 distro 裡準備 %[1]s（%[2]s）。細節在紀錄檔裡。",
	PathExtracting:      "正在讀取備份並取回檔案…",
	ProgressScanning:    "已讀取",
	PathFailed:          "取回失敗：%s",
	PathMoveFailed:      "檔案已經從備份讀出來，但沒辦法放到 %[1]s（那個位置在這段時間被換成了別的東西？）。它們留在 distro 裡的 %[2]s，只有 root 打得開。",
	WindowsPathGiven:    "%s 是 Windows 的路徑，這裡要的是 distro 裡的路徑，例如 /home/me。在 Git Bash 裡，斜線開頭的參數會在 wslbak 收到之前被改寫：請在指令前面加上 MSYS_NO_PATHCONV=1，或改用 PowerShell、cmd 或 WSL 的 shell。",
	PathArchiveChanged:  "檔案已經取回，但 %s 和備份當時不一樣了（可能已損壞），取回的內容不一定正確。建議改用另一份備份再取一次。",
	PathDone:            "已取回 %[1]d 個項目到 %[2]s",
	PathExplorer:        "在檔案總管可以從這裡打開：%s",
	BriefOther:          "備份失敗。",
	BriefNoStream:       "無法從 distro 取得資料。",
	BriefWrite:          "寫入備份檔失敗（磁碟滿了，或連不到備份資料夾？）。",
	BriefTar:            "tar 回報錯誤。",
	BriefSeeLog:         " 細節在那台電腦的紀錄檔裡（wslbak status 也看得到）。",
	BackupNoTar:         "這個 distro 沒有安裝 tar。請在 distro 裡安裝 GNU tar，例如 openSUSE：zypper install tar；Fedora：dnf install tar；Alpine：apk add tar。",
	DocTarMissing:       "%s：裡面沒有安裝 tar",
	RestoreReadme: `這個資料夾是 wslbak 做的 WSL 備份（https://github.com/Boring206/wslbak）

每個子資料夾是一個 distro。一份備份有三個檔案：
  <編號>.tar.gz   整個 distro，標準的 tar 封存
  <編號>.json     這份備份的資訊（大小、檢查碼、試還原的結果）
  <編號>.idx.gz   裡面的檔案清單（wslbak files 用的，還原時用不到）

要還原時，在這個資料夾開啟 PowerShell 或命令提示字元，執行：

  .\wslbak.exe restore

它會把最新一份通過試還原的備份還原成一個「新的」distro，
不會覆蓋或移除你已經有的 distro。
要看這裡有哪些備份，或指定 distro、指定較舊的一份：

  .\wslbak.exe list
  .\wslbak.exe restore -d <distro> <編號>

只想找回某個檔案的話，先看備份裡有什麼，再把它取回到 distro 裡的一個新資料夾：

  .\wslbak.exe files /home/me
  .\wslbak.exe restore --path /home/me/notes.md --into /home/me/recovered

沒有 wslbak 也能手動還原（備份檔就是一般的 tar.gz）：

  wsl --import <新名稱> <放虛擬磁碟的資料夾> <distro>\<編號>.tar.gz --version 2

手動匯入的 distro 會以 root 登入。要改回來，在它裡面建立 /etc/wsl.conf，內容是：
  [user]
  default=<你的使用者名稱>
`,
}

var enUS = catalog{
	Usage: `Usage: wslbak <command> [options]

Commands:
  init             Set up backups: pick the distro, destination and how many to keep, and create a daily task
  config           Show the current settings; with options, change them
  run              Back up now (then test-restore the backup and prune old ones)
  list             List existing backups
  files [id] [path]  List what a backup holds in a folder (default: the root of the newest backup)
  status           Show the schedule, the last result, and whether the installed program is intact
  doctor           Check the environment and settings one by one, with a fix for whatever would stop backups
  verify [id]      Test-restore an existing backup again (default: the newest)
  restore [id]     Restore a backup as a new distro (default: the newest verified one, else the newest; never overwrites a distro)
  uninstall        Remove the scheduled task and the installed program; backups are kept

Options:
  -d, --distro <name>        Which distro (default: the only one that can be backed up)
      --all                  init: set up every distro that can be backed up
      --dest <folder>        init: where to store backups
      --keep <count>         init, config: how many of the newest verified backups to keep (default 7)
      --keep-weekly <weeks>  init, config: also keep one per week, for this many weeks (default 0)
      --keep-monthly <months>  init, config: also keep one per month, for this many months (default 0)
      --at <HH:MM>           init, config: time of the daily backup (default 03:00)
      --webhook <url>        init, config: also report failures to this URL (ntfy, Discord, Slack); with config, off removes it
      --notify <when>        config: failure notifies only on failure, always after every backup
      --verify <how>         config: restore test-restores every backup, none turns that off
      --exclude <pattern>    config: exclude one more path pattern, such as /home/*/Downloads/* (repeatable)
      --unexclude <pattern>  config: stop excluding a pattern (repeatable)
      --enable, --disable    config: turn backups of one distro on or off
      --private              config: let only your account open the backup folder
      --no-verify            run: skip the test restore this time
      --find <text>          files: list entries whose name or path contains this text
      --name <name>          restore: name of the restored distro
      --to <folder>          restore: where to put the restored distro
      --path <path>          restore: bring back only this file or folder from the backup (repeatable); needs --into
      --into <folder>        restore: put those files into this folder inside the distro (it must not exist, or be empty)
  -n, --dry-run              Only show what would be done
  -y, --yes                  Do not ask for confirmation
      --lang <language>      Interface language: en or zh-TW (or set WSLBAK_LANG)
      --debug                Show details and timing of each step
  -h, --help                 Show this help
  -v, --version              Show the version

Exit codes: 0 success; 1 backup written but not verified, or backups are stale; 2 failure; 3 another wslbak is running
`,

	ErrorWithHint:         "wslbak: %v\nRun wslbak --help for usage.\n",
	ErrorLine:             "wslbak: %v",
	UnknownFlag:           "unknown option: %s",
	UnknownCommand:        "unknown command: %s",
	NeedCommand:           "a command is required, for example wslbak run",
	FlagNotForCommand:     "%[1]s cannot be used with the %[2]s command",
	UnexpectedArg:         "unexpected argument: %s",
	NeedValue:             "%s needs a value",
	BadLang:               "unsupported language: %s (use en or zh-TW)",
	BadKeep:               "the number of backups to keep must be a whole number of 1 or more: %s",
	BadTime:               "the time must be written as 24-hour HH:MM: %s",
	BadWebhook:            "the webhook must be an http or https URL: %s",
	BadDistroName:         "a distro name may only contain letters, digits, dots, underscores and hyphens: %s",
	ListSep:               ", ",
	AskProceed:            "Proceed? [y/N] ",
	DryRunNothingDone:     "(--dry-run: nothing above was actually done)",
	AlreadyRunning:        "Another wslbak is already running (possibly the scheduled backup). Try again when it has finished.",
	AlreadyRunningLong:    "It has been running for %d hours, so it is probably stuck. Restarting Windows (or signing out and in again) ends it.",
	NotSetUp:              "Backups are not set up yet. Run wslbak init to get started.",
	NothingConfigured:     "No distro is set up for backup. Run wslbak init.",
	DistroNotConfigured:   "%[1]s is not set up for backup. Run wslbak init -d %[1]s",
	WhichDistro:           "There is more than one distro; choose one with -d: %s",
	NoSuchDistro:          "There is no distro named %s.",
	NoEligibleDistro:      "There is no distro that can be backed up. Run wslbak status to see why.",
	DistroNotEligible:     "%[1]s cannot be backed up: %[2]s",
	DistroGone:            "The distro %s was not found (it may have been removed).",
	DistroReplaced:        "%[1]s is no longer the distro that was set up (same name, but reinstalled). After checking, run wslbak init -d %[1]s again",
	NeedStoreWSL:          "The Microsoft Store version of WSL is required (wsl --version must work). Run wsl --update first.",
	PathInsideWSL:         "%s is inside a WSL file system. Use a folder on a Windows drive instead.",
	NoBackups:             "There are no backups in %s.",
	NoSuchBackup:          "There is no backup with the id %s. Run wslbak list to see what exists.",
	SkipOwn:               "a temporary distro that belongs to wslbak",
	SkipName:              "its name contains unsupported characters",
	SkipBusy:              "it is being installed, imported or removed",
	SkipWSL1:              "it is WSL1 (only WSL2 is supported)",
	SkipManaged:           "it is managed by another program such as Docker Desktop",
	CanBackUp:             "can be backed up",
	JustNow:               "just now",
	MinutesAgo:            "%d min ago",
	HoursAgo:              "%d hr ago",
	DaysAgo:               "%d days ago",
	DurationSeconds:       "%d s",
	DurationMinutes:       "%d min %d s",
	DurationHours:         "%d hr %d min",
	InitProbing:           "Checking %s…",
	ProbeFailed:           "Could not run the check inside %[1]s: %[2]v",
	InitNeedsPackage:      "Run init from an installed wslbak (the copy in the backup folder is only for restoring; wslbakw.exe, which the schedule needs, is not next to it).",
	AskDest:               "Where should backups be stored? Press Enter for %s: ",
	DestUnreachable:       "Cannot use %[1]s: %[2]v",
	DestFAT:               "%[1]s is formatted as %[2]s, which cannot hold files over 4 GB; a backup will not fit.",
	DestNotWritable:       "Cannot write to %[1]s: %[2]v\nIf Controlled folder access is on, allow wslbak or choose another folder.",
	InitPlanTitle:         "About to set up:",
	InitLabels:            [6]string{"Distro", "Backups go to", "Keep", "Schedule", "Test restore", "On failure"},
	InitDistroLine:        "%[1]s (%[2]s, %[3]s used)",
	InitKeepLine:          "the newest %d backups that passed their test restore",
	InitAtLine:            "every day at %s (if the PC is off then, after the next sign-in)",
	InitVerifyLine:        "after every backup (temporarily needs up to about %[2]s free on %[1]s)",
	InitVerifyOff:         "off (verify is none in the configuration)",
	InitNotifyToast:       "a Windows notification",
	InitNotifyWebhook:     "a Windows notification and the webhook at %s",
	InitChangesTitle:      "This will create or change:",
	InitChangeLabels:      [5]string{"Program", "Settings", "Schedule", "Registry", "Restore kit"},
	InitTaskLine:          "the task \"%[1]s\" in Task Scheduler, running %[2]s %[3]s",
	InitWhatHappens:       "Each backup: reads every file inside the distro as root and archives it (the distro keeps running and is not written to);\nimports the backup as a temporary distro, checks it, and removes it; deletes backups beyond the number to keep.\nIf the distro is not running at that time, it is started.",
	InitNever:             "It never shuts down WSL or stops a distro, never changes or removes an existing distro, and needs no administrator rights.",
	WarnSameVolume:        "Note: the backups and the distro are on the same volume (%s). Reinstalling Windows or a disk failure would take the backups too.",
	WarnSameDisk:          "Note: %[1]s is on the same physical disk as %[2]s, where the distro lives. If that disk fails the backups go with it; use an external drive or a NAS to guard against that.",
	WarnRemovable:         "Note: %s is not an internal drive. A backup fails, and you are notified, when it is not connected.",
	WarnOneDrive:          "Note: this folder is inside OneDrive, so every backup (possibly several GB) will be uploaded.",
	WarnDestSpace:         "Note: %[1]s has only %[2]s free, which may not be enough for a backup.",
	WarnVerifySpace:       "Note: %[1]s has only %[2]s free, which may not be enough for the test restore; when it is not, the test restore is skipped and you are notified.",
	InstallFailed:         "Could not copy the program to %[1]s: %[2]v",
	TaskFailed:            "Could not create the scheduled task: %v",
	TaskDescription:       "wslbak: backs up WSL distros every day and test-restores each backup.",
	InitDone:              "Set up. A backup now runs every day at %s.",
	AskRunNow:             "Run the first backup now? [y/N] ",
	InitRunHint:           "To back up right away, run wslbak run.",
	RunBackingUp:          "Backing up %[1]s → %[2]s",
	ProgressReading:       "reading…",
	ProgressImporting:     "importing a temporary distro…",
	RunWritten:            "wrote %[1]s (%[2]s, %[3]s)",
	RunWarnings:           "%d files or folders changed while being read (normal for a backup of a running system).",
	RunSkippedMount:       "%s was not backed up: it is mounted from another disk and is not part of this distro's root file system.",
	RunDropped:            "tar in this distro does not support %s, so those attributes were not backed up.",
	RunACLs:               "%d files carry POSIX ACLs: they are stored in the backup, but WSL does not apply them on import, so they must be set again after a restore.",
	RunVerifying:          "Test restore…",
	RunVerifySkipped:      "The test restore was skipped this time (--no-verify).",
	RunVerified:           "test restore passed: all %[1]d sampled files match (%[2]s)",
	RunNotVerified:        "written, but not test-restored: %s",
	RunVerifyFailed:       "the test restore failed: %s",
	RunPruned:             "deleted %d old backup(s).",
	RunDone:               "Done.",
	DryRunWouldVerify:     "the backup would then be test-restored",
	DryRunWouldPrune:      "the old backup %s would be deleted",
	VerifyStart:           "Test-restoring %[1]s (%[2]s)…",
	BackupNotGNUTar:       "tar in this distro is not GNU tar, which is the only one wslbak supports for now. On Alpine, install it with apk add tar.",
	BackupNoStream:        "Could not read from the distro: %s",
	BackupWriteFailed:     "Could not write the backup file: %s",
	BackupStalled:         "No data arrived for over 10 minutes during the backup, so it was abandoned.",
	BackupTruncated:       "The backup data did not arrive completely (the distro may have been shut down part-way).",
	BackupTarFailed:       "tar reported an error: %s",
	ReasonNoIndex:         "wslbak could not parse the structure of this tar, so it cannot be test-restored",
	ReasonEtc:             "/etc in this distro is not an ordinary directory, so a test restore cannot be done safely",
	ReasonNoSpace:         "there is not enough free space for the temporary distro",
	ReasonChanged:         "the backup file is no longer what was written (it may be damaged)",
	ReasonUnread:          "the backup file cannot be read (it may be damaged)",
	ReasonImport:          "wsl --import failed (details are in the log file)",
	ReasonNotInert:        "the temporary distro could not be guaranteed to start no services, so it was not started",
	ReasonCheck:           "the check did not finish",
	ReasonUser:            "the original default user or their home directory is missing from the restored distro",
	ReasonSamples:         "the contents of restored files differ from what was backed up",
	NotifyFailedTitle:     "wslbak: backup of %s failed",
	NotifyStuckTitle:      "wslbak: a backup has been running for %d hours",
	NotifyStuckBody:       "It is probably stuck, and no new backup can start until it ends. Restart Windows (or sign out and in again), then run wslbak run.",
	NotifyUnverifiedTitle: "wslbak: backup of %s was not verified",
	NotifyOKTitle:         "wslbak: backup of %s finished",
	NotifyOKBody:          "%[1]s (%[2]s) passed its test restore.",
	ListHeaders:           [4]string{"ID", "TIME", "SIZE", "STATUS"},
	LabelVerified:         "verified",
	LabelNotVerified:      "not verified",
	LabelVerifyFailed:     "verification failed",
	StatusSchedule:        "Schedule: every day at %s",
	StatusNoTask:          "The scheduled task is missing. Run wslbak init again to recreate it.",
	StatusInstallMissing:  "%s is missing, so the scheduled backup cannot run. Antivirus software may have quarantined it; run wslbak init again to put it back.",
	StatusDisabled:        "disabled",
	StatusCounts:          "%[1]d backup(s), %[2]d of them passed a test restore (keeping %[3]d)",
	StatusNeverSucceeded:  "No backup has succeeded yet.",
	StatusLastSuccess:     "Last success: %s",
	StatusStale:           "Backups are stale: the last success was %s.",
	StatusLastProblem:     "Problem in the latest run (%[1]s): %[2]s",
	RestorePlan:           "Backup %[1]s (%[2]s, %[3]s, %[4]s) will be restored as a new distro %[5]s in %[6]s\nExisting distros are not touched.",
	RestoreUnverified:     "Note: this backup has not passed a test restore.",
	RestoreNameTaken:      "A distro named %s already exists. wslbak never overwrites a distro; choose another name with --name.",
	RestoreTargetUsed:     "%s already exists and is not empty. Choose another folder with --to.",
	RestoreChecking:       "Checking the backup file…",
	RestoreCorrupt:        "%s is no longer what was written (it may be damaged); nothing was restored. Use wslbak restore <id> to pick another backup.",
	RestoreImporting:      "Importing…",
	RestoreUserHint:       "Could not set the default user (UID %d), so the distro logs in as root for now. Add a [user] section with default=<your user name> to /etc/wsl.conf inside it.",
	RestoreDone:           "Restored as the distro %[1]s (%[2]s).",
	RestoreNext:           "Enter it:      wsl -d %[1]s\nMake default:  wsl --set-default %[1]s",
	RestoreTwins:          "The original %s still exists. Both distros have the same services configured, so running them together may make them compete for the same ports.",
	UninstallPlanTitle:    "About to remove:",
	UninstallTask:         "the scheduled task %s",
	UninstallProgram:      "the program in %s",
	UninstallRegistry:     "the registry key %s",
	UninstallKeeps:        "Backups are not deleted and stay in:",
	UninstallDone:         "The scheduled task and the program were removed. All backups are still there.",
	AskRemoveConfig:       "Also delete the settings and logs (%s)? [y/N] ",

	BadCount:            "the count must be a whole number of 0 or more: %s",
	BadNotify:           "--notify must be failure or always: %s",
	BadVerify:           "--verify must be restore or none: %s",
	BadPattern:          "an exclude pattern must be an absolute path inside the distro, such as /home/*/Downloads/*: %s",
	FlagConflict:        "%[1]s and %[2]s cannot be used together",
	KeepWeeklyMore:      ", plus one per week for %d weeks",
	KeepMonthlyMore:     ", plus one per month for %d months",
	ConfigVerifyOn:      "Test restore: after every backup",
	ConfigVerifyOff:     "Test restore: off",
	ConfigNotifyFailure: "Notify: on failure (%s)",
	ConfigNotifyAlways:  "Notify: after every backup (%s)",
	ConfigKeep:          "Keep: %s",
	ConfigExcludes:      "Excluded: %s",
	ConfigChanged:       "%[1]s: %[2]s → %[3]s",
	ConfigNone:          "(none)",
	ConfigNoChange:      "Nothing changed.",
	ConfigSaved:         "Saved.",
	ConfigTaskUpdated:   "The scheduled task now runs every day at %s.",
	ExcludeBuiltin:      "%s is always excluded and cannot be removed.",
	InitSkipped:         "Skipping %[1]s: %[2]s",
	AskPick:             "Which one? Enter a number, or a for all of them: ",
	WhichDistroOrAll:    "There is more than one distro: choose one with -d, or use --all for all of them: %s",
	DoctorOK:            "ok",
	DoctorLabels:        [3]string{"info", "warning", "problem"},
	DocSectionWSL:       "WSL",
	DocSectionDistros:   "Distros",
	DocSectionWindows:   "This PC",
	DocSectionSchedule:  "Schedule and program",
	DocSectionBackups:   "Backups",
	DocSectionSpace:     "Ways to save space",
	DocWSL:              "WSL %s (Microsoft Store version)",
	DocDistroOK:         "%[1]s: can be backed up (%[2]s, %[3]s used)",
	DocDistroSkip:       "%[1]s: not backed up, %[2]s",
	DocDistroNotRunning: "%s: can be backed up; it is not running, so it was not checked further",
	DocTarBad:           "%s: its tar is not GNU tar",
	DocTarFix:           "Install GNU tar inside that distro. On Alpine: apk add tar",
	DocNoSHA:            "%s: it has no sha256sum, so a test restore cannot compare file contents",
	DocNoSHAFix:         "Install coreutils inside that distro",
	DocSACOn:            "Smart App Control is on: unsigned programs are blocked, so the scheduled backup cannot run",
	DocSACFix:           "wslbak's executables are not code-signed. To use it, Smart App Control has to be turned off in Windows Security → App & browser control; once off, it cannot be turned back on.",
	DocSACEval:          "Smart App Control is in evaluation mode: Windows may turn it on by itself later, and the unsigned wslbak would then be blocked",
	DocSACOff:           "Smart App Control is off",
	DocCFAOn:            "Controlled folder access is on",
	DocCFAFix:           "If the backup folder is in a protected place (such as Documents), allow wslbak.exe and wslbakw.exe in Windows Security → Ransomware protection",
	DocCFAOff:           "Controlled folder access is off",
	DocVerifyAttr:       "%s is compressed or encrypted; wsl --import may be unable to create a virtual disk there",
	DocVerifyAttrFix:    "Turn compression and encryption off for that folder in Explorer: Properties → Advanced",
	DocInstalled:        "The program the schedule runs is in %s and is intact",
	DocDestOK:           "%[1]s is usable; %[2]s has %[3]s free",
	DocDestShared:       "Other accounts on this PC can read %s. Backups are not encrypted and hold every file of the distro, keys included; if you are the only one using this PC, nothing needs doing.",
	DocDestSharedFix:    "On a shared PC, to keep it to your account: wslbak config --private",
	PrivatePlan:         "Would restrict %s to your account (plus SYSTEM and Administrators).",
	PrivateDone:         "%s can now only be opened by your account (plus SYSTEM and Administrators).",
	PrivateForeign:      "%[1]s also holds things that wslbak did not put there (%[2]s), so its permissions are left alone. Keep the backups in a folder of their own, or change the permissions yourself.",
	PrivateFailed:       "Could not change the permissions of %[1]s: %[2]v",
	PrivateStillShared:  "The permissions of %s were set, but other accounts can still read it; this drive may not support permissions.",
	PrivateNote:         "Note: after reinstalling Windows, your new account will not be on that list. Before restoring, open the folder in Explorer once and confirm its question, or use a terminal run as administrator.",
	DocDestMissing:      "%s cannot be reached",
	DocDestMissingFix:   "If it is on an external or network drive, connect it and try again",
	DocDestLow:          "%[1]s has only %[2]s free; the next backup needs about %[3]s",
	DocLastOK:           "%[1]s: the last success was %[2]s",
	DocLastNever:        "%s: no backup has succeeded yet",
	DocLastStale:        "%[1]s: backups are stale; the last success was %[2]s",
	DocStaleFix:         "Check that the PC is on and signed in at the scheduled time; you can also run wslbak run by hand",
	DocLastProblem:      "%[1]s: problem in the latest run: %[2]s",
	DocSeeLog:           "Details are in the log file: %s",
	DocCache:            "%[1]s: %[3]s takes %[2]s and is a cache that can be downloaded again",
	DocCacheFix:         "To leave it out of backups: wslbak config -d %[1]s --exclude \"%[2]s\"",
	DocDocker:           "%[1]s: Docker's data (/var/lib/docker) takes %[2]s. Images can be pulled again, but data in volumes cannot; whether to exclude it is your call",
	DocNoCaches:         "No large caches worth excluding were found",
	DocSummaryOK:        "No problems found.",
	DocSummaryWarn:      "%d item(s) need attention.",
	DocSummaryFail:      "%d problem(s) need fixing.",
	RunPruneSkipped:     "This folder holds backups written by a newer wslbak, so no old backups were deleted this time.",
	BadPath:             "a path must be a location inside the distro (such as /home/me/project) and must not contain ..: %s",
	FilesBuildingIndex:  "This backup has no file index yet; scanning the archive to build one (needed only once)…",
	FilesNoIndex:        "Could not read the contents of backup %[1]s: %[2]v",
	FilesTitle:          "%[3]s in backup %[1]s (%[2]s)",
	FilesNotFound:       "%[1]s is not in backup %[2]s. Use wslbak files --find <text> to search.",
	FilesFindNone:       "Nothing in backup %[2]s has \"%[1]s\" in its name.",
	WarmUpFailed:        "The program that was just installed does not start: %s\nAntivirus software may have blocked or quarantined it. Allow the file in your antivirus (or add its folder as an exception), then run wslbak init again.",
	WarmUpSlow:          "Antivirus software took %s to check the newly installed program; this happens only the first time each new version runs.",
	DocAntivirus:        "Active antivirus: %s. It may hold a new version of wslbak for checking the first time it runs, or even run it in a sandbox.",
	DocAntivirusFix:     "If scheduled backups never run, add %s to the antivirus exceptions",
	FlagNeeds:           "%[1]s must be used together with %[2]s",
	BadInto:             "--into must be an absolute path inside the distro, such as /home/me/restored: %s",
	PathRoot:            "To bring back the whole distro, use wslbak restore without --path.",
	PathDistroGone:      "The files would go back into %s, but that distro no longer exists. Restore the whole distro with wslbak restore first.",
	PathWrongDistro:     "The record of backup %[1]s says it belongs to %[2]s, but it sits in the backup folder of %[3]s. Stopping, so that no files go into the wrong distro.",
	PathPlan:            "%[1]d item(s) (%[2]s) from backup %[3]s will be put into %[5]s inside %[4]s\nTheir full paths are recreated under that folder, so no existing file is overwritten.",
	PathTargetUsed:      "%s already exists and is not empty. Give --into a folder that does not exist yet, or an empty one.",
	PathTargetNotDir:    "%s already exists and is not a folder.",
	PathTargetNoParent:  "The folder that should contain %s does not exist.",
	PathPrepareFailed:   "Could not prepare %[1]s inside the distro (%[2]s). Details are in the log file.",
	PathExtracting:      "Reading the backup and bringing the files back…",
	ProgressScanning:    "read",
	PathFailed:          "Bringing the files back failed: %s",
	PathMoveFailed:      "The files were read from the backup, but could not be put in place at %[1]s (was something else put there in the meantime?). They are in %[2]s inside the distro, which only root can open.",
	WindowsPathGiven:    "%s is a Windows path, but this needs a path inside the distro, such as /home/me. In Git Bash, arguments that start with / are rewritten before wslbak sees them: put MSYS_NO_PATHCONV=1 in front of the command, or use PowerShell, cmd or a WSL shell.",
	PathArchiveChanged:  "The files were brought back, but %s is no longer what was written (it may be damaged), so they may not be correct. Try again from another backup.",
	PathDone:            "Brought back %[1]d item(s) into %[2]s",
	PathExplorer:        "In Explorer it is here: %s",
	BriefOther:          "The backup failed.",
	BriefNoStream:       "Could not read from the distro.",
	BriefWrite:          "Could not write the backup file (disk full, or the backup folder unreachable?).",
	BriefTar:            "tar reported an error.",
	BriefSeeLog:         " Details are in the log file on that PC (wslbak status shows them too).",
	BackupNoTar:         "This distro has no tar installed. Install GNU tar inside it, for example zypper install tar on openSUSE, dnf install tar on Fedora, apk add tar on Alpine.",
	DocTarMissing:       "%s: it has no tar installed",
	RestoreReadme: `WSL backups made by wslbak (https://github.com/Boring206/wslbak)

Each subfolder is one distro. A backup is three files:
  <id>.tar.gz   the whole distro as a standard tar archive
  <id>.json     details about that backup (size, checksum, test-restore result)
  <id>.idx.gz   the list of files in it (for wslbak files; not needed to restore)

To restore, open PowerShell or Command Prompt in this folder and run:

  .\wslbak.exe restore

It restores the newest backup that passed its test restore, as a NEW distro.
It never overwrites or removes a distro you already have.
To see what is here, or to pick a distro or an older backup:

  .\wslbak.exe list
  .\wslbak.exe restore -d <distro> <id>

To get back just one file, look at what a backup holds, then bring the file back into a
new folder inside the distro:

  .\wslbak.exe files /home/me
  .\wslbak.exe restore --path /home/me/notes.md --into /home/me/recovered

Without wslbak, the same thing by hand (the archive is a plain tar.gz):

  wsl --import <NewName> <FolderForItsDisk> <distro>\<id>.tar.gz --version 2

After importing by hand the distro logs in as root. To change that, create
/etc/wsl.conf inside it containing:
  [user]
  default=<your user name>
`,
}
