//go:build ignore

// mkdist 把 bin/ 裡編好的執行檔包成發佈用的檔案，放在 dist/：
//
//	wslbak-<版本>-windows-x64.zip、wslbak-<版本>-windows-arm64.zip
//	SHA256SUMS
//	winget/ 與 scoop/ 底下填好版本與雜湊的套件清單
//
// 用法：go run scripts/mkdist.go <版本>（通常由 node scripts/build.mjs --dist 呼叫）。
// 同樣的輸入會產生位元組完全相同的 zip：檔案順序與時間都是固定的。
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// zip 裡每個檔案的時間。固定下來，重新打包才會得到同樣的雜湊。
var fixedTime = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

type entry struct{ source, name string }

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkdist:", err)
		os.Exit(1)
	}
}

func makeZip(target string, entries []entry) string {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		data, err := os.ReadFile(e.source)
		must(err)
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: fixedTime}
		h.SetMode(0o644)
		f, err := w.CreateHeader(h)
		must(err)
		_, err = f.Write(data)
		must(err)
	}
	must(w.Close())
	must(os.WriteFile(target, buf.Bytes(), 0o644))
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

func main() {
	if len(os.Args) != 2 || os.Args[1] == "" {
		fmt.Fprintln(os.Stderr, "usage: go run scripts/mkdist.go <version>")
		os.Exit(2)
	}
	version := os.Args[1]
	must(os.MkdirAll("dist", 0o755))

	sums := map[string]string{}
	var lines []string
	for _, arch := range []string{"x64", "arm64"} {
		name := fmt.Sprintf("wslbak-%s-windows-%s.zip", version, arch)
		sums[arch] = makeZip(filepath.Join("dist", name), []entry{
			// 解壓之後兩個執行檔要放在一起，檔名不帶架構：init 會照這個名字找旁邊的無視窗版。
			{"bin/wslbak-" + arch + ".exe", "wslbak.exe"},
			{"bin/wslbakw-" + arch + ".exe", "wslbakw.exe"},
			{"README.md", "README.md"},
			{"README.zh-TW.md", "README.zh-TW.md"},
			{"LICENSE", "LICENSE"},
		})
		lines = append(lines, sums[arch]+"  "+name)
		fmt.Printf("dist/%s  %s\n", name, sums[arch])
	}
	must(os.WriteFile("dist/SHA256SUMS", []byte(strings.Join(lines, "\n")+"\n"), 0o644))

	// 套件清單：把範本裡的版本與雜湊填進去。
	fill := strings.NewReplacer(
		"{{VERSION}}", version,
		"{{SHA256_X64}}", strings.ToUpper(sums["x64"]),
		"{{SHA256_ARM64}}", strings.ToUpper(sums["arm64"]),
		"{{sha256_x64}}", sums["x64"],
		"{{sha256_arm64}}", sums["arm64"],
	)
	for _, dir := range []string{"winget", "scoop"} {
		templates, err := filepath.Glob(filepath.Join("packaging", dir, "*"))
		must(err)
		must(os.MkdirAll(filepath.Join("dist", dir), 0o755))
		for _, t := range templates {
			data, err := os.ReadFile(t)
			must(err)
			out := filepath.Join("dist", dir, filepath.Base(t))
			must(os.WriteFile(out, []byte(fill.Replace(string(data))), 0o644))
			fmt.Println(filepath.ToSlash(out))
		}
	}
}
