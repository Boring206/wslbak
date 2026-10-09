package main

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"testing"
	"testing/iotest"
)

// rawHeader 做出一個 ustar 標頭，大小欄位照給定的位元組填。
func rawHeader(name string, sizeField []byte, typeflag byte) []byte {
	h := make([]byte, tarBlock)
	copy(h[0:], name)
	copy(h[100:], "0000644\x00")
	copy(h[108:], "0000000\x00")
	copy(h[116:], "0000000\x00")
	copy(h[124:136], sizeField)
	copy(h[136:], "15262140661\x00")
	h[156] = typeflag
	copy(h[257:], "ustar\x0000")
	setTarChecksum(h)
	return h
}

func octalField(n int64) []byte { return []byte(fmt.Sprintf("%011o\x00", n)) }

func paxRecord(key, value string) string {
	body := " " + key + "=" + value + "\n"
	n := len(body) + 1
	for len(fmt.Sprint(n))+len(body) != n {
		n = len(fmt.Sprint(n)) + len(body)
	}
	return fmt.Sprint(n) + body
}

func paxHeader(records string) []byte {
	out := rawHeader("./PaxHeaders/x", octalField(int64(len(records))), 'x')
	out = append(out, records...)
	return append(out, make([]byte, padded(int64(len(records)))-int64(len(records)))...)
}

func block(data string) []byte {
	return append([]byte(data), make([]byte, padded(int64(len(data)))-int64(len(data)))...)
}

func TestSizeFixReader(t *testing.T) {
	data := string(bytes.Repeat([]byte("x"), 1024))
	var in []byte
	in = append(in, rawHeader("./etc/", octalField(0), '5')...)
	// GNU tar 對 8 GiB 以上的檔案的寫法（這裡用小檔案模擬）：大小在延伸標頭，標頭上是 0。
	in = append(in, paxHeader(paxRecord("size", "1024")+paxRecord("mtime", "1791541681.5"))...)
	in = append(in, rawHeader("./a.bin", octalField(0), '0')...)
	in = append(in, block(data)...)
	// 延伸標頭裡沒有 size 的普通檔案。
	in = append(in, paxHeader(paxRecord("mtime", "1791541681.5"))...)
	in = append(in, rawHeader("./b.txt", octalField(5), '0')...)
	in = append(in, block("hello")...)
	in = append(in, rawHeader("./link", octalField(0), '2')...)
	in = append(in, make([]byte, 2*tarBlock)...)

	for name, wrap := range map[string]func(io.Reader) io.Reader{
		"whole":        func(r io.Reader) io.Reader { return r },
		"byte by byte": iotest.OneByteReader,
		"halves":       iotest.HalfReader,
	} {
		fix := newSizeFixReader(wrap(bytes.NewReader(in)))
		out, err := io.ReadAll(fix)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(out) != len(in) {
			t.Fatalf("%s: length changed from %d to %d", name, len(in), len(out))
		}
		if fix.Fixed != 1 {
			t.Errorf("%s: fixed %d headers, want 1", name, fix.Fixed)
		}
		// 只有那一個標頭的大小欄位與檢查碼變了。
		at := bytes.Index(in, []byte("./a.bin"))
		for i := range in {
			inSize := i >= at+124 && i < at+136
			inSum := i >= at+148 && i < at+156
			if in[i] != out[i] && !inSize && !inSum {
				t.Fatalf("%s: byte %d changed outside the size and checksum fields", name, i)
			}
		}
		if got, _ := tarNumber(out[at+124 : at+136]); got != 1024 {
			t.Errorf("%s: the header now says %d, want 1024", name, got)
		}
		if !checksumOK(out[at : at+tarBlock]) {
			t.Errorf("%s: the checksum of the changed header is wrong", name)
		}
		// 標準庫讀得出同樣的項目與內容。
		tr := tar.NewReader(bytes.NewReader(out))
		var names []string
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s: archive/tar: %v", name, err)
			}
			names = append(names, fmt.Sprintf("%s:%d", h.Name, h.Size))
			if h.Name == "./a.bin" {
				if body, _ := io.ReadAll(tr); string(body) != data {
					t.Errorf("%s: the contents of a.bin changed", name)
				}
			}
		}
		if got, want := fmt.Sprint(names), "[./etc/:0 ./a.bin:1024 ./b.txt:5 ./link:0]"; got != want {
			t.Errorf("%s: entries = %s, want %s", name, got, want)
		}
	}
}

// 8 GiB 以上放不進八進位，要用二進位寫法；標準庫與我們自己的解析都讀得回來。
func TestSizeFixReaderLarge(t *testing.T) {
	const big = 9 << 30
	var in []byte
	in = append(in, paxHeader(paxRecord("size", fmt.Sprint(int64(big))))...)
	in = append(in, rawHeader("./big.bin", octalField(0), '0')...)
	// 資料不放進來：只看標頭被改成什麼樣。
	fix := newSizeFixReader(bytes.NewReader(in))
	out, err := io.ReadAll(fix)
	if err != nil || len(out) != len(in) || fix.Fixed != 1 {
		t.Fatalf("err=%v length %d→%d fixed=%d", err, len(in), len(out), fix.Fixed)
	}
	header := out[len(out)-tarBlock:]
	if header[124]&0x80 == 0 {
		t.Errorf("a size of 9 GiB must be written in binary form, got % x", header[124:136])
	}
	if got, err := tarNumber(header[124:136]); err != nil || got != big {
		t.Errorf("the header says %d (%v), want %d", got, err, int64(big))
	}
	if !checksumOK(header) {
		t.Errorf("the checksum is wrong")
	}
	h, err := tar.NewReader(bytes.NewReader(out)).Next()
	if err != nil || h.Size != big {
		t.Errorf("archive/tar reads size %v (%v), want %d", h, err, int64(big))
	}
}

// 不需要補的封存一個位元組都不能變；壞掉或看不懂的內容也原樣通過。
func TestSizeFixReaderLeavesTheRestAlone(t *testing.T) {
	fixture, err := os.ReadFile("testdata/gnu.tar")
	if err != nil {
		t.Fatal(err)
	}
	garbage := bytes.Repeat([]byte("not a tar archive "), 100)
	oldSparse := append(rawHeader("./sparse", octalField(0), 'S'), bytes.Repeat([]byte{1}, 3*tarBlock)...)
	truncated := fixture[:len(fixture)/2+7]
	for name, in := range map[string][]byte{"fixture": fixture, "garbage": garbage, "old sparse": oldSparse, "truncated": truncated, "empty": nil} {
		fix := newSizeFixReader(iotest.HalfReader(bytes.NewReader(in)))
		out, err := io.ReadAll(fix)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if !bytes.Equal(in, out) {
			t.Errorf("%s: the stream was changed", name)
		}
		if fix.Fixed != 0 {
			t.Errorf("%s: %d headers were changed", name, fix.Fixed)
		}
	}
}
