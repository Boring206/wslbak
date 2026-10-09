package main

import (
	"archive/tar"
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/klauspost/compress/gzip"
)

// 檔案索引：每份備份旁邊的 <編號>.idx.gz，列出封存裡的每一個項目。
// 備份時掃描 tar 的同時順手寫出；瀏覽備份內容、找檔案、只取回某個檔案都靠它，不必把整個封存讀一遍。
// 索引裡的東西封存裡本來就有（檔名、大小、時間、擁有者），沒有多洩漏什麼。
//
// 格式是 gzip 壓縮的 JSON，一行一筆：第一行是檔頭，之後每行一個項目。

const (
	indexSuffix  = ".idx.gz"
	indexVersion = 1
)

type indexHeader struct {
	Version int    `json:"wslbakIndex"`
	ID      string `json:"id"`
}

// 項目的類型。
const (
	typeFile     = "f"
	typeDir      = "d"
	typeSymlink  = "l"
	typeHardlink = "h"
	typeChar     = "c"
	typeBlock    = "b"
	typeFifo     = "p"
	typeOther    = "?"
)

type indexEntry struct {
	// Path 是封存裡的名稱，去掉結尾的斜線：./etc/passwd；根目錄是「.」。
	Path string `json:"p,omitempty"`
	// PathB 只在檔名不是合法的 UTF-8 時使用：原始位元組的 base64。JSON 存不了那種檔名。
	PathB string `json:"pb,omitempty"`
	Type  string `json:"t"`
	Size  int64  `json:"s,omitempty"`
	MTime int64  `json:"m"`
	Mode  uint32 `json:"x"` // 權限位元，含 setuid／setgid／sticky
	UID   int    `json:"u"`
	GID   int    `json:"g"`
	Link  string `json:"l,omitempty"` // 符號連結或硬連結指向的名稱
}

// name 回傳項目的名稱（原始位元組）。
func (e indexEntry) name() string {
	if e.PathB != "" {
		if raw, err := base64.StdEncoding.DecodeString(e.PathB); err == nil {
			return string(raw)
		}
	}
	return e.Path
}

// cleanMember 把 tar 裡的名稱整理成索引用的樣子：去掉結尾的斜線，根目錄寫成「.」。
func cleanMember(name string) string {
	name = strings.TrimRight(name, "/")
	if name == "" {
		return "."
	}
	return name
}

func entryFromHeader(h *tar.Header) indexEntry {
	e := indexEntry{
		Size:  h.Size,
		MTime: h.ModTime.Unix(),
		Mode:  uint32(h.Mode) & 0o7777,
		UID:   h.Uid,
		GID:   h.Gid,
	}
	name := cleanMember(h.Name)
	if utf8.ValidString(name) {
		e.Path = name
	} else {
		e.PathB = base64.StdEncoding.EncodeToString([]byte(name))
	}
	switch h.Typeflag {
	case tar.TypeReg, tar.TypeGNUSparse:
		e.Type = typeFile
	case tar.TypeDir:
		e.Type, e.Size = typeDir, 0
	case tar.TypeSymlink:
		e.Type, e.Link, e.Size = typeSymlink, h.Linkname, 0
	case tar.TypeLink:
		e.Type, e.Link, e.Size = typeHardlink, cleanMember(h.Linkname), 0
	case tar.TypeChar:
		e.Type = typeChar
	case tar.TypeBlock:
		e.Type = typeBlock
	case tar.TypeFifo:
		e.Type = typeFifo
	default:
		e.Type = typeOther
	}
	return e
}

// indexWriter 把項目寫進索引檔。先寫到 .partial，close 成功才改名。
type indexWriter struct {
	path string
	file *os.File
	buf  *bufio.Writer
	zw   *gzip.Writer
	enc  *json.Encoder
	err  error
}

func newIndexWriter(path, id string) (*indexWriter, error) {
	file, err := os.Create(path + ".partial")
	if err != nil {
		return nil, err
	}
	w := &indexWriter{path: path, file: file, buf: bufio.NewWriterSize(file, 256<<10)}
	w.zw = gzip.NewWriter(w.buf)
	w.enc = json.NewEncoder(w.zw)
	w.err = w.enc.Encode(indexHeader{Version: indexVersion, ID: id})
	return w, nil
}

func (w *indexWriter) add(e indexEntry) {
	if w.err == nil {
		w.err = w.enc.Encode(e)
	}
}

// close 把索引寫完。ok 為 false（備份失敗）或中途出錯時把半成品刪掉。
func (w *indexWriter) close(ok bool) error {
	err := w.err
	if cerr := w.zw.Close(); err == nil {
		err = cerr
	}
	if cerr := w.buf.Flush(); err == nil {
		err = cerr
	}
	if cerr := w.file.Close(); err == nil {
		err = cerr
	}
	if !ok || err != nil {
		os.Remove(w.path + ".partial")
		return err
	}
	return os.Rename(w.path+".partial", w.path)
}

var errBadIndex = errors.New("not a wslbak file index")

// scanIndex 逐筆讀索引，交給 visit；visit 回傳 false 就提早結束。
func scanIndex(file string, visit func(indexEntry) bool) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(bufio.NewReaderSize(f, 256<<10))
	if err != nil {
		return fmt.Errorf("%w: %v", errBadIndex, err)
	}
	defer zr.Close()
	dec := json.NewDecoder(zr)
	var head indexHeader
	if err := dec.Decode(&head); err != nil || head.Version < 1 {
		return errBadIndex
	}
	if head.Version > indexVersion {
		return fmt.Errorf("%w: version %d is newer than this program understands", errBadIndex, head.Version)
	}
	for {
		var e indexEntry
		if err := dec.Decode(&e); err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("%w: %v", errBadIndex, err)
		}
		if !visit(e) {
			return nil
		}
	}
}

var errBadMemberPath = errors.New("bad path")

// normalizeMemberPath 把使用者給的路徑換成封存裡的名稱：/etc/passwd、etc/passwd、./etc/passwd
// 都變成 ./etc/passwd，根目錄是「.」。帶有 .. 的路徑、空字串、含 NUL 的一律拒絕：
// 這個結果之後只拿來和索引比對，但也不該讓任何奇怪的東西走到那一步。
func normalizeMemberPath(p string) (string, error) {
	if p == "" || strings.ContainsRune(p, 0) {
		return "", errBadMemberPath
	}
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimLeft(p, "/")
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", errBadMemberPath
		}
	}
	cleaned := path.Clean("/" + p)
	if cleaned == "/" {
		return ".", nil
	}
	return "." + cleaned, nil
}

// displayPath 把封存裡的名稱寫成使用者習慣的樣子：./etc/passwd → /etc/passwd。
func displayPath(member string) string {
	if member == "." {
		return "/"
	}
	return strings.TrimPrefix(member, ".")
}

// modeString 把類型與權限寫成 ls -l 的樣子，例如 drwxr-xr-x。
func modeString(e indexEntry) string {
	kind := map[string]byte{typeFile: '-', typeDir: 'd', typeSymlink: 'l', typeHardlink: '-', typeChar: 'c', typeBlock: 'b', typeFifo: 'p'}[e.Type]
	if kind == 0 {
		kind = '?'
	}
	out := []byte{kind, '-', '-', '-', '-', '-', '-', '-', '-', '-'}
	for i, c := range "rwxrwxrwx" {
		if e.Mode&(1<<uint(8-i)) != 0 {
			out[i+1] = byte(c)
		}
	}
	special := func(bit uint32, pos int, set, unset byte) {
		if e.Mode&bit != 0 {
			if out[pos] == 'x' {
				out[pos] = set
			} else {
				out[pos] = unset
			}
		}
	}
	special(0o4000, 3, 's', 'S')
	special(0o2000, 6, 's', 'S')
	special(0o1000, 9, 't', 'T')
	return string(out)
}
