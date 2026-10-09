package main

import (
	"fmt"
	"io"
	"strconv"
)

// sizeFixReader 讓 tar 串流原樣通過，只補一個欄位。
//
// GNU tar 的 pax 格式遇到 8 GiB 以上的檔案時，把大小寫在延伸標頭裡（size=…），標頭本身的大小欄位
// 留成 0。照規格是延伸標頭說了算，但 WSL 2.7 內附的 bsdtar（libarchive 3.7.7）改信標頭上的 0：
// 那個檔案匯入後是空的，它的內容被當成一個個壞掉的標頭跳過去，而 wsl --import 照樣回報成功。
// 這裡把真正的大小也填回標頭（八進位放不下就用 GNU 的二進位寫法），兩處一致，哪一種讀法都對。
//
// 串流的長度不變。看不懂的內容原樣通過，而且從那裡開始不再修改任何東西。
type sizeFixReader struct {
	r       io.Reader
	meta    []byte // 標頭（附加資料的話連同內容）的暫存區，重複使用
	ready   []byte // 已處理、等著交出去的位元組
	body    int64  // 目前項目的資料（含補齊）還有多少要原樣通過
	paxSize int64  // 延伸標頭替下一個項目指定的大小；沒有時是 -1
	raw     bool   // 之後全部原樣通過
	err     error  // 讀來源時遇到的結束或錯誤，等 ready 交完再回報
	// Fixed 是補過大小欄位的標頭數。
	Fixed int
}

func newSizeFixReader(r io.Reader) *sizeFixReader {
	return &sizeFixReader{r: r, paxSize: -1}
}

func (f *sizeFixReader) Read(p []byte) (int, error) {
	for {
		switch {
		case len(f.ready) > 0:
			n := copy(p, f.ready)
			f.ready = f.ready[n:]
			return n, nil
		case f.err != nil:
			return 0, f.err
		case f.raw:
			return f.r.Read(p)
		case f.body > 0:
			if int64(len(p)) > f.body {
				p = p[:f.body]
			}
			n, err := f.r.Read(p)
			f.body -= int64(n)
			return n, err
		}
		f.next()
	}
}

// fill 從來源再讀 n 個位元組接在 meta 後面；來源提早結束時把讀到的留著，錯誤記在 f.err。
func (f *sizeFixReader) fill(n int64) bool {
	start := len(f.meta)
	f.meta = append(f.meta, make([]byte, n)...)
	got, err := io.ReadFull(f.r, f.meta[start:])
	if err != nil {
		f.meta = f.meta[:start+got]
		if err == io.ErrUnexpectedEOF {
			// 對下游而言就是「到這裡沒有了」；封存完不完整由下游判斷。
			err = io.EOF
		}
		f.err = err
		return false
	}
	return true
}

// next 讀進下一個標頭，必要時補上大小，放進 ready。
func (f *sizeFixReader) next() {
	f.meta = f.meta[:0]
	ok := f.fill(tarBlock)
	f.ready = f.meta
	if !ok {
		return
	}
	block := f.meta[:tarBlock]
	size, err := tarNumber(block[124:136])
	if isZero(block) || !checksumOK(block) || err != nil {
		// 結尾標記，或不是看得懂的標頭：到此為止。
		f.raw = true
		return
	}
	switch typeflag := block[156]; typeflag {
	case 'x':
		// 下一個項目的延伸標頭：讀進來找 size。
		if size > maxMetaSize {
			f.raw = true
			return
		}
		ok := f.fill(padded(size))
		f.ready = f.meta
		if !ok {
			return
		}
		f.paxSize = -1
		records, err := parsePax(f.meta[tarBlock : tarBlock+size])
		if err != nil {
			f.raw = true
			return
		}
		if v, found := records["size"]; found {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				f.raw = true
				return
			}
			f.paxSize = n
		}
	case 'g', 'L', 'K':
		// 全域延伸標頭、GNU 的長檔名：內容原樣通過，不影響下一個項目的大小。
		f.body = padded(size)
	case '0', 0, '7':
		stored := size
		if f.paxSize >= 0 {
			stored = f.paxSize
			if size != stored {
				putTarSize(block[124:136], stored)
				setTarChecksum(block)
				f.Fixed++
			}
		}
		f.paxSize = -1
		f.body = padded(stored)
	case '1', '2', '3', '4', '5', '6':
		// 連結、裝置、目錄、具名管道：只有標頭，沒有資料。
		f.paxSize = -1
	default:
		// 舊式的稀疏檔、多卷封存等等：標頭後面的結構不一樣，不碰。
		f.raw = true
	}
}

// putTarSize 把大小寫進標頭的欄位：放得下就用八進位，否則用最高位元為 1 的二進位寫法。
func putTarSize(field []byte, n int64) {
	if n < 1<<(3*(len(field)-1)) {
		copy(field, fmt.Sprintf("%0*o\x00", len(field)-1, n))
		return
	}
	for i := len(field) - 1; i > 0; i-- {
		field[i] = byte(n)
		n >>= 8
	}
	field[0] = 0x80
}

// setTarChecksum 重算標頭的檢查碼：六位八進位、一個 NUL、一個空白，和 GNU tar 的寫法相同。
func setTarChecksum(block []byte) {
	var sum int64
	for i, b := range block {
		if i >= 148 && i < 156 {
			b = ' '
		}
		sum += int64(b)
	}
	copy(block[148:156], fmt.Sprintf("%06o\x00 ", sum))
}
