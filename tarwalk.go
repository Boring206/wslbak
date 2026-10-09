package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// walkTar 逐項走訪 tar 串流，並把選中的項目「原封不動」地寫出去：
// 延伸標頭、標頭、資料與補齊的位元組一個都不改。只取回備份裡的某幾個檔案時用它來過濾。
//
// 標準庫的 archive/tar 讀得出每個項目的內容，但拿不到原始的位元組；重新編碼一次
// 又會遇到它寫不出稀疏檔之類的限制。這裡只判讀「這個項目叫什麼、資料有多長」，其餘不碰。
// 測試會拿它和標準庫逐項比對名稱。

type rawEntry struct {
	Name     string
	Linkname string
	Typeflag byte
	Stored   int64 // 存在封存裡的資料長度（稀疏檔是壓縮過洞之後的長度）
}

var (
	errTarHeader      = errors.New("damaged tar header")
	errTarUnsupported = errors.New("unsupported tar entry")
)

// maxMetaSize 是延伸標頭與長檔名這類「附加資料」讀進記憶體的上限。
const maxMetaSize = 64 << 20

// tarNumber 解析標頭裡的數字欄位：八進位文字，或最高位元為 1 的二進位（GNU 用來放大數字）。
func tarNumber(field []byte) (int64, error) {
	if len(field) > 0 && field[0]&0x80 != 0 {
		var n int64
		for i, b := range field {
			if i == 0 {
				b &= 0x7f
			}
			if n>>55 != 0 {
				return 0, errTarHeader
			}
			n = n<<8 | int64(b)
		}
		return n, nil
	}
	text := strings.Trim(string(field), " \x00")
	if text == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(text, 8, 64)
	if err != nil || n < 0 {
		return 0, errTarHeader
	}
	return n, nil
}

func tarString(field []byte) string {
	if i := bytes.IndexByte(field, 0); i >= 0 {
		field = field[:i]
	}
	return string(field)
}

// checksumOK 檢查標頭的檢查碼：把檢查碼欄位當成空白，其餘位元組相加。
func checksumOK(block []byte) bool {
	want, err := tarNumber(block[148:156])
	if err != nil {
		return false
	}
	var sum int64
	for i, b := range block {
		if i >= 148 && i < 156 {
			b = ' '
		}
		sum += int64(b)
	}
	return sum == want
}

// parsePax 解析延伸標頭的內容：一筆筆「長度 鍵=值\n」，長度包含它自己。
func parsePax(data []byte) (map[string]string, error) {
	records := map[string]string{}
	for len(data) > 0 {
		space := bytes.IndexByte(data, ' ')
		if space <= 0 {
			return nil, errTarHeader
		}
		n, err := strconv.Atoi(string(data[:space]))
		if err != nil || n <= space+1 || n > len(data) || data[n-1] != '\n' {
			return nil, errTarHeader
		}
		record := data[space+1 : n-1]
		eq := bytes.IndexByte(record, '=')
		if eq <= 0 {
			return nil, errTarHeader
		}
		records[string(record[:eq])] = string(record[eq+1:])
		data = data[n:]
	}
	return records, nil
}

func padded(n int64) int64 { return (n + tarBlock - 1) / tarBlock * tarBlock }

// walkTar 走訪 r。每個項目呼叫一次 want；回傳 true 的項目會原樣寫到 out（out 可以是 nil，只走訪不輸出）。
// 走到結尾標記就停；結尾標記本身不會寫出去，由呼叫端決定後面要接什麼。
func walkTar(r io.Reader, out io.Writer, want func(rawEntry) bool) (entries int64, err error) {
	var pending bytes.Buffer // 屬於下一個項目的延伸標頭、長檔名
	var pax map[string]string
	longName, longLink := "", ""
	block := make([]byte, tarBlock)
	copyBuf := make([]byte, 256<<10)

	for {
		if _, err := io.ReadFull(r, block); err != nil {
			if err == io.EOF && pending.Len() == 0 {
				// 沒有結尾標記就結束了：當成結尾，由上游的長度檢查去判斷是不是被截斷。
				return entries, nil
			}
			return entries, fmt.Errorf("%w: %v", errTarHeader, err)
		}
		if isZero(block) {
			return entries, nil
		}
		if !checksumOK(block) {
			return entries, errTarHeader
		}
		size, err := tarNumber(block[124:136])
		if err != nil {
			return entries, err
		}
		typeflag := block[156]

		switch typeflag {
		case 'x', 'g', 'L', 'K':
			// 附加資料：連同這個標頭先存起來，等真正的項目出現再一起決定要不要。
			if size > maxMetaSize {
				return entries, fmt.Errorf("%w: a %d-byte extended header", errTarUnsupported, size)
			}
			data := make([]byte, padded(size))
			if _, err := io.ReadFull(r, data); err != nil {
				return entries, fmt.Errorf("%w: %v", errTarHeader, err)
			}
			pending.Write(block)
			pending.Write(data)
			switch typeflag {
			case 'x':
				if pax, err = parsePax(data[:size]); err != nil {
					return entries, err
				}
			case 'L':
				longName = tarString(data[:size])
			case 'K':
				longLink = tarString(data[:size])
			}
			continue
		case 'S':
			// 舊式的 GNU 稀疏格式，標頭後面還有不定長的延伸區塊。wslbak 產生的封存用的是 pax 格式，不會出現。
			return entries, fmt.Errorf("%w: old GNU sparse format", errTarUnsupported)
		}

		e := rawEntry{Typeflag: typeflag, Stored: size}
		e.Name = tarString(block[0:100])
		// 只有 POSIX 的 ustar 標頭有檔名前綴；GNU 格式把那一段拿去放別的東西。
		if string(block[257:263]) == "ustar\x00" {
			if prefix := tarString(block[345:500]); prefix != "" {
				e.Name = prefix + "/" + e.Name
			}
		}
		e.Linkname = tarString(block[157:257])
		if longName != "" {
			e.Name = longName
		}
		if longLink != "" {
			e.Linkname = longLink
		}
		if v, ok := pax["path"]; ok {
			e.Name = v
		}
		// 稀疏檔在 pax 格式裡，真正的檔名放在這個鍵；標頭上的是佔位用的名字。
		if v, ok := pax["GNU.sparse.name"]; ok {
			e.Name = v
		}
		if v, ok := pax["linkpath"]; ok {
			e.Linkname = v
		}
		if v, ok := pax["size"]; ok {
			if e.Stored, err = strconv.ParseInt(v, 10, 64); err != nil || e.Stored < 0 {
				return entries, errTarHeader
			}
		}
		// 這些類型只有標頭，沒有資料。
		switch typeflag {
		case '1', '2', '3', '4', '5', '6':
			e.Stored = 0
		}
		entries++

		keep := want != nil && want(e)
		sink := io.Discard
		if keep && out != nil {
			sink = out
			if _, err := out.Write(pending.Bytes()); err != nil {
				return entries, err
			}
			if _, err := out.Write(block); err != nil {
				return entries, err
			}
		}
		if n := padded(e.Stored); n > 0 {
			copied, err := io.CopyBuffer(sink, io.LimitReader(r, n), copyBuf)
			if err != nil {
				return entries, err
			}
			// 來源提早結束時 CopyBuffer 不會回報錯誤，要自己確認真的拿到了那麼多。
			if copied != n {
				return entries, fmt.Errorf("%w: %s is cut short", errTarHeader, e.Name)
			}
		}
		pending.Reset()
		pax, longName, longLink = nil, "", ""
	}
}
