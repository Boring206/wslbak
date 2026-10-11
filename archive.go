package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"

	"github.com/klauspost/compress/gzip"
)

// 讀備份檔：解壓的同時算整個檔案的雜湊，讀完才知道它是不是備份當時的那個檔案。
// 還原差異的還原點要同時讀兩個封存（完整＋差異），合併的部分也在這裡。

// archiveStream 是一個開著的封存檔。Read 給出解壓後的 tar。
type archiveStream struct {
	file   *os.File
	sum    hash.Hash
	hashed io.Reader
	zr     *gzip.Reader
}

func openArchive(path string) (*archiveStream, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	a := &archiveStream{file: file, sum: sha256.New()}
	a.hashed = io.TeeReader(bufio.NewReaderSize(file, 1<<20), a.sum)
	// 解壓用單執行緒的版本：它不會在背景預讀，雜湊算到哪裡完全由我們讀到哪裡決定。
	if a.zr, err = gzip.NewReader(a.hashed); err != nil {
		file.Close()
		return nil, err
	}
	return a, nil
}

func (a *archiveStream) Read(p []byte) (int, error) { return a.zr.Read(p) }

func (a *archiveStream) Close() {
	a.zr.Close()
	a.file.Close()
}

// finish 把檔案剩下的部分讀完，回傳整個檔案的 SHA-256。tar 讀到結尾標記就停了，
// 後面只該剩下補的零；解壓到結尾時 gzip 的檢查碼也跟著核對過了。
func (a *archiveStream) finish() (string, error) {
	buf := make([]byte, 256<<10)
	for {
		n, err := a.zr.Read(buf)
		if !isZero(buf[:n]) {
			return "", errTrailingData
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	// gzip 的結尾之後不該還有東西；有的話也算進雜湊，雜湊自然對不上。
	if _, err := io.Copy(io.Discard, a.hashed); err != nil {
		return "", err
	}
	return hex.EncodeToString(a.sum.Sum(nil)), nil
}

// errArchiveChanged：封存檔和備份當時不一樣了。
type errArchiveChanged struct{ Name string }

func (e *errArchiveChanged) Error() string {
	return e.Name + " is not the file that was written at backup time"
}

// mergeOptions 是合併一個差異的還原點時可以調整的部分。
type mergeOptions struct {
	// Want 挑出要的項目；nil 表示整個 distro。
	Want func(rawEntry) bool
	// OnBase 在走到完整備份的每個項目時呼叫（可以是 nil）；dropped 表示這個還原點不要它。
	OnBase func(name string, dropped bool)
	// Progress 累計讀進來的 tar 位元組數（可以是 nil）。
	Progress *progressReader
}

// writeMerged 把一個差異的還原點寫成一條 tar 串流（不含結尾標記），寫到 out。
// 完整備份與差異兩個檔案一開始就都開著：排程的清理刪不掉開著的檔案，還原到一半不會少一個。
// 兩個檔案都會從頭讀到尾並核對雜湊；完整備份的索引還在的話，也順便確認它和封存是同一批項目。
func writeMerged(m *manifest, out io.Writer, opts mergeOptions) error {
	if m.orphan() {
		return errMergeMismatch
	}
	drop, err := unpackBits(m.Drop)
	if err != nil {
		return fmt.Errorf("%w: %v", errMergeMismatch, err)
	}
	if int64(len(drop))*8 < m.BaseEntries || countBits(drop) != m.DropCount {
		return fmt.Errorf("%w: its list of dropped entries is damaged", errMergeMismatch)
	}
	base, err := openArchive(m.base.archivePath())
	if err != nil {
		return err
	}
	defer base.Close()
	// 差異要走兩遍：先目錄，後其他。兩遍各開一次，雜湊在第二遍（讀到底的那一遍）核對。
	first, err := openArchive(m.archivePath())
	if err != nil {
		return err
	}
	defer first.Close()
	second, err := openArchive(m.archivePath())
	if err != nil {
		return err
	}
	defer second.Close()

	// 索引是選用的：沒有它也能還原，有的話多一層把關。
	var index *indexReader
	if fileExists(m.base.indexPath()) {
		if index, err = openIndex(m.base.indexPath()); err != nil {
			index = nil
		} else {
			defer index.close()
		}
	}
	counted := func(r io.Reader) io.Reader {
		if opts.Progress == nil {
			return r
		}
		return &progressTee{r: r, into: opts.Progress}
	}
	diffs := []io.Reader{counted(first), counted(second)}
	err = mergeTar(mergeInput{
		Base: counted(base),
		Diff: func() (io.Reader, error) {
			next := diffs[0]
			diffs = diffs[1:]
			return next, nil
		},
		Drop: drop, BaseEntries: m.BaseEntries, Want: opts.Want,
		OnBase: func(ordinal int64, e rawEntry) error {
			name := cleanMember(e.Name)
			if index != nil {
				if want, err := index.next(); err != nil || want.name() != name {
					return fmt.Errorf("%w: entry %d is not the one its index names", errMergeMismatch, ordinal)
				}
			}
			if opts.OnBase != nil {
				opts.OnBase(name, bitSet(drop, ordinal))
			}
			return nil
		},
	}, out)
	if err != nil {
		return err
	}
	if sum, err := base.finish(); err != nil {
		return err
	} else if sum != m.base.SHA256 {
		return &errArchiveChanged{m.base.Archive}
	}
	if sum, err := second.finish(); err != nil {
		return err
	} else if sum != m.SHA256 {
		return &errArchiveChanged{m.Archive}
	}
	return nil
}

// progressTee 把讀到的位元組數加到另一個計數器上。
type progressTee struct {
	r    io.Reader
	into *progressReader
}

func (p *progressTee) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.into.n.Add(int64(n))
	return n, err
}

// checkArchive 把一個封存從頭讀到尾：解得開、是一條走得完的 tar、項目數與雜湊都和 manifest 記的一樣。
func checkArchive(m *manifest) (reason, detail string) {
	a, err := openArchive(m.archivePath())
	if err != nil {
		return reasonUnread, err.Error()
	}
	defer a.Close()
	entries, err := walkTar(a, nil, nil)
	if err != nil {
		return reasonUnread, err.Error()
	}
	sum, err := a.finish()
	switch {
	case errors.Is(err, errTrailingData):
		return reasonUnread, err.Error()
	case err != nil:
		return reasonUnread, err.Error()
	case sum != m.SHA256:
		return reasonChanged, "sha256 is " + sum
	case m.Index != nil && entries != m.Index.Entries:
		return reasonChanged, fmt.Sprintf("%d entries, %d were written", entries, m.Index.Entries)
	}
	return "", ""
}
