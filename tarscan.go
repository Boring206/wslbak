package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"path"
	"strings"
)

// 備份時 tar 串流會同時流過這裡的掃描器。它不改動任何位元組，只記下三件事：
//   - 結尾標記從哪裡開始（試還原要在那裡接上覆寫用的項目）
//   - 一批小檔案的 SHA-256（試還原後拿來和還原出來的內容比對）
//   - ./etc 是不是真的目錄（不是的話，覆寫 ./etc/wsl.conf 就不可靠）

const (
	tarBlock = 512
	// sampleMaxSize 以下的一般檔案都會算雜湊，再從中隨機留下 sampleCount 個。
	sampleMaxSize = 1 << 20
	sampleCount   = 512
)

type fileSample struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type tarIndex struct {
	Entries   int64 `json:"entries"`
	EndOffset int64 `json:"endOffset"` // 結尾標記（兩個全零區塊）的起點
	Size      int64 `json:"size"`      // 整個 tar 的長度，含結尾的補零
	EtcIsDir  bool  `json:"etcIsDir"`
	// ACLs 是帶著 POSIX ACL 的項目數（不算 systemd 的日誌目錄）。
	// ACL 有存進封存，但 WSL 的匯入不會套用它，還原後會消失。
	ACLs    int64        `json:"acls"`
	Samples []fileSample `json:"samples"`
	// Largest 是最大的幾個檔案。它們太大，不適合抽樣算雜湊，但試還原時至少要確認大小對得上：
	// 匯入時最容易出事的正是這些檔案。
	Largest []fileSize `json:"largest,omitempty"`
}

type fileSize struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// largestCount 是記下大小的檔案數。
const largestCount = 8

// noteLargest 把一個檔案放進「最大的幾個」裡（由大到小排列）。
func (idx *tarIndex) noteLargest(path string, size int64) {
	at := len(idx.Largest)
	for at > 0 && idx.Largest[at-1].Size < size {
		at--
	}
	if at >= largestCount {
		return
	}
	idx.Largest = append(idx.Largest, fileSize{})
	copy(idx.Largest[at+1:], idx.Largest[at:])
	idx.Largest[at] = fileSize{Path: path, Size: size}
	if len(idx.Largest) > largestCount {
		idx.Largest = idx.Largest[:largestCount]
	}
}

var errTrailingData = errors.New("data after the end-of-archive marker")

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// sampleable 回報這個項目適不適合拿來抽樣比對。
// 檔名帶控制字元或反斜線的不取：比對是用 sha256sum -c，這些字元在它的清單格式裡要跳脫。
func sampleable(h *tar.Header) bool {
	if h.Typeflag != tar.TypeReg || h.Size <= 0 || h.Size > sampleMaxSize {
		return false
	}
	return plainName(h.Name) && !volatilePaths[h.Name]
}

// plainName：名稱裡沒有控制字元與反斜線，可以放進交給檢查腳本的清單（一行一個）。
func plainName(name string) bool {
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return false
		}
	}
	return true
}

// volatilePaths 是 WSL 在 distro 啟動時會自己改寫的檔案，以及試還原時被我們覆寫的檔案。
var volatilePaths = map[string]bool{
	"./etc/wsl.conf":              true,
	"./etc/wsl-distribution.conf": true,
	"./etc/hosts":                 true,
	"./etc/resolv.conf":           true,
	"./etc/ld.so.cache":           true,
}

func isZero(p []byte) bool {
	for _, b := range p {
		if b != 0 {
			return false
		}
	}
	return true
}

// scanTar 把 r 讀到底並回傳索引。每遇到一個項目就呼叫一次 sink（可以是 nil）。
// 解析失敗時仍然會把剩下的資料讀完（上游的備份不能因為掃描器看不懂而中斷），
// 這時回傳的錯誤不是 nil，索引只有 Size 可信。
func scanTar(r io.Reader, sink func(indexEntry)) (tarIndex, error) {
	var idx tarIndex
	src := &countingReader{r: r}
	tr := tar.NewReader(src)
	seen := 0 // 到目前為止符合抽樣條件的檔案數

	var scanErr error
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			scanErr = err
			break
		}
		idx.Entries++
		if sink != nil {
			sink(entryFromHeader(h))
		}
		if h.Typeflag == tar.TypeDir && strings.TrimSuffix(h.Name, "/") == "./etc" {
			idx.EtcIsDir = true
		}
		// systemd 會在每次開機時替日誌目錄重新設好 ACL，那些不用提醒使用者。
		if (h.PAXRecords["SCHILY.acl.access"] != "" || h.PAXRecords["SCHILY.acl.default"] != "") &&
			!strings.HasPrefix(h.Name, "./var/log/journal") {
			idx.ACLs++
		}
		if h.Typeflag == tar.TypeReg && h.Size > sampleMaxSize && plainName(h.Name) && !volatilePaths[h.Name] {
			idx.noteLargest(h.Name, h.Size)
		}
		if !sampleable(h) {
			continue
		}
		sum := sha256.New()
		if _, err := io.Copy(sum, tr); err != nil {
			scanErr = err
			break
		}
		sample := fileSample{Path: h.Name, Size: h.Size, SHA256: hex.EncodeToString(sum.Sum(nil))}
		// 水塘抽樣：不必事先知道總數，每個檔案被留下的機率相同。
		seen++
		if len(idx.Samples) < sampleCount {
			idx.Samples = append(idx.Samples, sample)
		} else if j := rand.IntN(seen); j < sampleCount {
			idx.Samples[j] = sample
		}
	}
	if scanErr == nil {
		// archive/tar 讀完兩個全零區塊就回報結束，所以結尾標記的起點是目前位置往回兩個區塊。
		idx.EndOffset = src.n - 2*tarBlock
	}

	// 結尾標記之後 tar 還會補零到整個紀錄的大小；把它讀完，順便確認真的全是零。
	buf := make([]byte, 256<<10)
	for {
		n, err := src.Read(buf)
		if scanErr == nil && !isZero(buf[:n]) {
			scanErr = errTrailingData
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			idx.Size = src.n
			return idx, err
		}
	}
	idx.Size = src.n
	if scanErr != nil {
		return tarIndex{Size: idx.Size}, scanErr
	}
	return idx, nil
}

// 試還原時，暫時 distro 絕對不能「活起來」：所有 WSL2 distro 共用同一個網路命名空間，
// 忠實的複本一開機就會多跑一份原本的每個服務與排程工作，還帶著使用者的憑證。
// 做法是在 tar 的結尾接上同名的項目：解開時後出現的會蓋掉前面的，原本的資料一個位元組都不用改。

// inertWSLConf 關掉 systemd、開機指令、Windows 磁碟的掛載、/etc/fstab 與 Windows 互通。
// 沒有 [boot] command，也沒有 [user] default。
const inertWSLConf = `# written by wslbak for a test restore; this copy must not start any service
[boot]
systemd=false
[automount]
enabled=false
mountFsTab=false
[interop]
enabled=false
appendWindowsPath=false
[network]
generateHosts=false
generateResolvConf=false
[gpu]
enabled=false
`

// inertDistConf 讓 WSL 不要替暫時 distro 建立開始功能表捷徑與 Windows Terminal 設定檔。
const inertDistConf = `# written by wslbak for a test restore
[shortcut]
enabled=false
[windowsterminal]
enabled=false
`

// buildOverrideTail 產生接在封存結尾的那一段：兩個覆寫用的檔案，加上新的結尾標記。
func buildOverrideTail() []byte {
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, f := range []struct{ name, body string }{
		{"./etc/wsl.conf", inertWSLConf},
		{"./etc/wsl-distribution.conf", inertDistConf},
	} {
		// 固定用最單純的 ustar 標頭；時間用零值，產生的內容每次都一樣。
		h := &tar.Header{Typeflag: tar.TypeReg, Name: f.name, Mode: 0o644, Size: int64(len(f.body)), Format: tar.FormatUSTAR}
		// 寫進記憶體裡的緩衝不會失敗，標頭也是固定的。
		if err := w.WriteHeader(h); err != nil {
			panic(err)
		}
		w.Write([]byte(f.body))
	}
	w.Close()
	return b.Bytes()
}

var errShortArchive = errors.New("the archive is shorter than its recorded end offset")

// overrideReader 依序吐出：r 的前 endOffset 個位元組、然後是 tail。
// r 在 endOffset 之後剩下的部分（原本的結尾標記與補零）會被讀完並丟掉；
// 那一段如果不是全零，代表 endOffset 記錯了，回報錯誤而不是送出壞掉的封存。
// 把 r 讀到底也讓上游的 gzip 檢查碼與整個檔案的雜湊都能算完。
type overrideReader struct {
	r         io.Reader
	remaining int64
	tail      []byte
	drained   bool
}

func newOverrideReader(r io.Reader, endOffset int64, tail []byte) *overrideReader {
	return &overrideReader{r: r, remaining: endOffset, tail: tail}
}

func (o *overrideReader) Read(p []byte) (int, error) {
	if o.remaining > 0 {
		if int64(len(p)) > o.remaining {
			p = p[:o.remaining]
		}
		n, err := o.r.Read(p)
		o.remaining -= int64(n)
		if err == io.EOF {
			if o.remaining > 0 {
				return n, errShortArchive
			}
			err = nil
		}
		return n, err
	}
	if !o.drained {
		buf := make([]byte, 64<<10)
		for {
			n, err := o.r.Read(buf)
			if !isZero(buf[:n]) {
				return 0, errTrailingData
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return 0, err
			}
		}
		o.drained = true
	}
	if len(o.tail) == 0 {
		return 0, io.EOF
	}
	n := copy(p, o.tail)
	o.tail = o.tail[n:]
	return n, nil
}

// overrideSeen 記下某個覆寫檔在串流裡最後一次出現時的樣子。
type overrideSeen struct {
	found bool
	// 那一次是不是出現在我們接上去的那一段裡（而不是封存原本的內容）。
	ours    bool
	content string
}

// overrideCheck 是掃描「實際送去匯入的串流」得到的結論。
type overrideCheck struct {
	conf, dist overrideSeen
	// 串流裡最後一個名為 etc 的項目是目錄。
	etcIsDir bool
}

// inert 回報覆寫是否確定生效：兩個檔案最後一次出現都是我們接上去的那一份、內容正是我們寫的，
// 而且 /etc 是真正的目錄。只看「最後一份的內容對不對」是不夠的：封存如果是別人動過手腳的，
// 它可以自己帶一份內容相同的 wsl.conf，再讓我們接上去的那一段被當成某個檔案的資料吞掉。
func (c overrideCheck) inert() bool {
	return c.etcIsDir &&
		c.conf.found && c.conf.ours && c.conf.content == inertWSLConf &&
		c.dist.found && c.dist.ours && c.dist.content == inertDistConf
}

func (c overrideCheck) summary() string {
	return fmt.Sprintf("etc is a directory=%v, wsl.conf found=%v ours=%v, wsl-distribution.conf found=%v ours=%v",
		c.etcIsDir, c.conf.found, c.conf.ours, c.dist.found, c.dist.ours)
}

// memberKey 把封存裡的名稱化成比對用的樣子。./etc/wsl.conf、etc/wsl.conf、/etc//wsl.conf
// 解開之後是同一個檔案，所以都要算成同一個名稱。
func memberKey(name string) string {
	return strings.TrimPrefix(path.Clean("/"+name), "/")
}

// scanOverride 掃描送去匯入的 tar 串流；tailStart 是我們接上去的那一段在串流裡開始的位置。
// 試還原送出資料的同時用它確認覆寫確實生效，確認不過就不啟動那個暫時 distro。
func scanOverride(r io.Reader, tailStart int64) (overrideCheck, error) {
	var c overrideCheck
	counted := &progressReader{r: r}
	tr := tar.NewReader(counted)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return c, nil
		}
		if err != nil {
			return overrideCheck{}, err
		}
		// 標頭剛讀完，讀到的位置在它的結尾；標頭本身佔一個區塊。
		ours := counted.n.Load()-tarBlock >= tailStart
		switch memberKey(h.Name) {
		case "etc":
			c.etcIsDir = h.Typeflag == tar.TypeDir
		case "etc/wsl.conf":
			c.conf, err = seeOverride(tr, h, ours)
		case "etc/wsl-distribution.conf":
			c.dist, err = seeOverride(tr, h, ours)
		}
		if err != nil {
			return overrideCheck{}, err
		}
	}
}

func seeOverride(tr *tar.Reader, h *tar.Header, ours bool) (overrideSeen, error) {
	seen := overrideSeen{found: true, ours: ours}
	if h.Typeflag == tar.TypeReg && h.Size <= 1<<20 {
		data, err := io.ReadAll(tr)
		if err != nil {
			return overrideSeen{}, err
		}
		seen.content = string(data)
	}
	return seen, nil
}
