package main

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	fenceRoot = `C:\Users\someone\AppData\Local\wslbak\verify`
	fenceName = "wslbak-verify-20261008T130403Z-23668d5a8d5ab13e"
)

func TestNewVerifyName(t *testing.T) {
	a, err := newVerifyName(time.Date(2026, 10, 8, 21, 4, 3, 0, time.FixedZone("CST", 8*3600)))
	if err != nil {
		t.Fatal(err)
	}
	if !verifyNameRe.MatchString(a) {
		t.Errorf("%q does not match the name pattern", a)
	}
	// 名稱裡的時間是 UTC，不受時區與日光節約時間影響。
	if !strings.HasPrefix(a, "wslbak-verify-20261008T130403Z-") {
		t.Errorf("%q should carry the UTC time", a)
	}
	b, _ := newVerifyName(time.Now())
	c, _ := newVerifyName(time.Now())
	if b == c {
		t.Error("two names generated in a row are identical")
	}
}

func TestFenceAllows(t *testing.T) {
	ours := regDistro{Name: fenceName, BasePath: fenceRoot + `\` + fenceName}
	real := regDistro{Name: "Ubuntu", BasePath: `C:\Users\someone\AppData\Local\wsl\{guid}`}
	at := func(path string) []regDistro { return []regDistro{real, {Name: fenceName, BasePath: path}} }

	cases := []struct {
		label   string
		name    string
		distros []regDistro
		claimed bool
		want    error
	}{
		{"everything holds", fenceName, []regDistro{real, ours}, true, nil},
		{"extended-length prefix on the path", fenceName, at(`\\?\` + fenceRoot + `\` + fenceName), true, nil},
		{"different case and a trailing separator", fenceName, at(strings.ToUpper(fenceRoot) + `\` + fenceName + `\`), true, nil},
		{"forward slashes", fenceName, at(strings.ReplaceAll(fenceRoot, `\`, "/") + "/" + fenceName), true, nil},

		{"a real distro", "Ubuntu", []regDistro{real, ours}, true, errFenceName},
		{"empty name", "", []regDistro{real, ours}, true, errFenceName},
		{"suffix after the token", fenceName + "-x", at(fenceRoot + `\` + fenceName + "-x"), true, errFenceName},
		{"prefix before the name", "x" + fenceName, at(fenceRoot + `\x` + fenceName), true, errFenceName},
		{"upper-case token", strings.ToUpper(fenceName), at(fenceRoot + `\` + fenceName), true, errFenceName},
		{"token too short", fenceName[:len(fenceName)-1], at(fenceRoot + `\` + fenceName), true, errFenceName},
		{"path separator in the name", `wslbak-verify-20261008T130403Z-23668d5a8d5ab13e\..`, []regDistro{ours}, true, errFenceName},
		{"line break in the name", fenceName + "\n", []regDistro{ours}, true, errFenceName},

		{"not registered", fenceName, []regDistro{real}, true, errFenceMissing},
		{"two distros differing only in case", fenceName, []regDistro{ours, {Name: strings.ToUpper(fenceName), BasePath: ours.BasePath}}, true, errFenceAmbigous},
		{"registered under a different case", fenceName, []regDistro{{Name: strings.ToUpper(fenceName), BasePath: ours.BasePath}}, true, errFencePath},

		// 位置必須「就是」verify 資料夾裡以名稱命名的那個子資料夾。
		{"the verify folder itself", fenceName, at(fenceRoot), true, errFencePath},
		{"a sibling whose name starts the same", fenceName, at(fenceRoot + `\` + fenceName + "-evil"), true, errFencePath},
		{"a subfolder of the right one", fenceName, at(fenceRoot + `\` + fenceName + `\sub`), true, errFencePath},
		{"dot-dot back out of the verify folder", fenceName, at(fenceRoot + `\` + fenceName + `\..\..\..\wsl\real`), true, errFencePath},
		{"where WSL keeps real distros", fenceName, at(real.BasePath), true, errFencePath},
		{"same path on another drive", fenceName, at("D" + fenceRoot[1:] + `\` + fenceName), true, errFencePath},
		{"empty path", fenceName, at(""), true, errFencePath},

		{"no claim file", fenceName, []regDistro{real, ours}, false, errFenceClaim},
	}
	for _, c := range cases {
		if got := fenceAllows(c.name, c.distros, fenceRoot, c.claimed); !errors.Is(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.label, got, c.want)
		}
	}
}

func TestClaimAndRemoveOwnedDir(t *testing.T) {
	root := t.TempDir()
	if hasClaim(root, fenceName) {
		t.Fatal("no claim should exist yet")
	}
	if err := writeClaim(root, "Ubuntu"); !errors.Is(err, errFenceName) {
		t.Errorf("writeClaim with a foreign name: %v", err)
	}
	if err := writeClaim(root, fenceName); err != nil {
		t.Fatal(err)
	}
	if !hasClaim(root, fenceName) {
		t.Fatal("the claim should exist now")
	}

	dir := filepath.Join(root, fenceName)
	os.MkdirAll(filepath.Join(dir, "nested"), 0o755)
	os.WriteFile(filepath.Join(dir, "nested", "ext4.vhdx"), []byte("x"), 0o644)
	// 旁邊別人的東西不能被動到。
	bystander := filepath.Join(root, "keep-me")
	os.MkdirAll(bystander, 0o755)
	other := "wslbak-verify-20261008T130403Z-ffffffffffffffff"
	os.MkdirAll(filepath.Join(root, other), 0o755)

	if err := removeOwnedDir(root, "keep-me"); !errors.Is(err, errFenceName) {
		t.Errorf("removeOwnedDir with a foreign name: %v", err)
	}
	if err := removeOwnedDir(root, other); !errors.Is(err, errFenceClaim) {
		t.Errorf("removeOwnedDir without a claim: %v", err)
	}
	if err := removeOwnedDir("", fenceName); !errors.Is(err, errFenceName) {
		t.Errorf("removeOwnedDir with a relative root: %v", err)
	}
	if err := removeOwnedDir(root, fenceName); err != nil {
		t.Fatal(err)
	}
	for path, wantExists := range map[string]bool{dir: false, claimPath(root, fenceName): false, bystander: true, filepath.Join(root, other): true} {
		if _, err := os.Stat(path); (err == nil) != wantExists {
			t.Errorf("%s: exists=%v, want %v", path, err == nil, wantExists)
		}
	}
}

// 破壞性的操作只能出現在 fence.go 的指定函式裡：移除 distro 只在 unregisterVerifyDistro，
// 遞迴刪除資料夾只在 removeOwnedDir。別處出現就是繞過了檢查。
func TestDestructiveCallsAreFenced(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	if len(files) == 0 {
		t.Skip("source files are not available next to the test binary")
	}
	found := map[string][]string{}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, decl := range f.Decls {
			where := file + ":(top level)"
			if fn, ok := decl.(*ast.FuncDecl); ok {
				where = file + ":" + fn.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.BasicLit:
					if v.Kind == token.STRING && strings.Contains(v.Value, "--unregister") {
						found["--unregister"] = append(found["--unregister"], where)
					}
				case *ast.SelectorExpr:
					if pkg, ok := v.X.(*ast.Ident); ok && pkg.Name == "os" && v.Sel.Name == "RemoveAll" {
						found["os.RemoveAll"] = append(found["os.RemoveAll"], where)
					}
				}
				return true
			})
		}
	}
	want := map[string]string{"--unregister": "fence.go:unregisterVerifyDistro", "os.RemoveAll": "fence.go:removeOwnedDir"}
	for what, where := range want {
		if got := found[what]; len(got) == 0 {
			t.Errorf("%s was not found at all; this test no longer guards anything", what)
		} else {
			for _, g := range got {
				if g != where {
					t.Errorf("%s is used in %s; it is only allowed in %s", what, g, where)
				}
			}
		}
	}
}

// 開始功能表裡的清理只碰我們自己的暫時 distro 留下的「空」資料夾。
func TestSweepStartMenu(t *testing.T) {
	programs := t.TempDir()
	ours := "wslbak-verify-20260115T030000Z-0123456789abcdef"
	oursWithShortcut := "wslbak-verify-20260115T030001Z-0123456789abcdef"
	for _, dir := range []string{ours, oursWithShortcut, "Ubuntu", "wslbak-verify-notes", "Accessories"} {
		if err := os.Mkdir(filepath.Join(programs, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{filepath.Join(oursWithShortcut, "something.lnk"), "wslbak-verify-20260115T030002Z-0123456789abcdef"} {
		if err := os.WriteFile(filepath.Join(programs, file), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sweepStartMenu(programs)
	left := map[string]bool{}
	entries, _ := os.ReadDir(programs)
	for _, e := range entries {
		left[e.Name()] = true
	}
	if left[ours] {
		t.Errorf("the empty folder of a temporary distro was not removed")
	}
	for _, name := range []string{oursWithShortcut, "Ubuntu", "wslbak-verify-notes", "Accessories", "wslbak-verify-20260115T030002Z-0123456789abcdef"} {
		if !left[name] {
			t.Errorf("%s was removed, but it is not an empty folder of a temporary distro", name)
		}
	}
}
