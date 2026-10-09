package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// 其他測試檔的預期輸出都是中文，所以測試預設用中文介面；要測英文的地方用 withLanguage 切換。
func TestMain(m *testing.M) {
	setLanguage(langZhTW)
	os.Exit(m.Run())
}

func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) {
			return true
		}
	}
	return false
}

var verbRe = regexp.MustCompile(`%(?:\[(\d+)\])?[-+# 0]*\d*(?:\.\d+)?([a-zA-Z%])`)

// formatArgs 回傳格式字串的每個參數位置需要哪一類的值：'d' 是整數，'s' 是其他。
func formatArgs(t *testing.T, name, format string) map[int]byte {
	t.Helper()
	args := map[int]byte{}
	next := 1
	for _, m := range verbRe.FindAllStringSubmatch(format, -1) {
		if m[2] == "%" {
			continue
		}
		idx := next
		if m[1] != "" {
			idx, _ = strconv.Atoi(m[1])
		}
		next = idx + 1
		kind := byte('s')
		if m[2] == "d" {
			kind = 'd'
		}
		if prev, ok := args[idx]; ok && prev != kind {
			t.Errorf("%s: argument %d is used both as a number and as text in %q", name, idx, format)
		}
		args[idx] = kind
	}
	return args
}

// 兩種語言的每一句都要有，而且吃的參數要一樣：少翻一句、或參數對不上，都會在執行時印出 %!s(MISSING) 之類的東西。
func TestCatalogParity(t *testing.T) {
	zh, en := reflect.ValueOf(zhTW), reflect.ValueOf(enUS)
	for i := 0; i < zh.NumField(); i++ {
		name := zh.Type().Field(i).Name
		switch zh.Field(i).Kind() {
		case reflect.String:
			a, b := zh.Field(i).String(), en.Field(i).String()
			if a == "" || b == "" {
				t.Errorf("%s: missing translation (zh-TW %q, en %q)", name, a, b)
				continue
			}
			if hasCJK(b) {
				t.Errorf("%s: the English text contains Chinese characters: %q", name, b)
			}
			if za, ea := formatArgs(t, name, a), formatArgs(t, name, b); !reflect.DeepEqual(za, ea) {
				t.Errorf("%s: format arguments differ\n  zh-TW %q\n  en    %q", name, a, b)
			}
		case reflect.Array:
			for j := 0; j < zh.Field(i).Len(); j++ {
				if zh.Field(i).Index(j).String() == "" || en.Field(i).Index(j).String() == "" {
					t.Errorf("%s[%d]: missing translation", name, j)
				}
			}
		default:
			t.Errorf("%s: unexpected field kind %s; teach this test about it", name, zh.Field(i).Kind())
		}
	}
	// 說明文字要提到每個指令與每個公開的旗標；--home 與 --scheduled 是內部用的，不列出來。
	hidden := map[string]bool{"--home": true, "--scheduled": true}
	for _, c := range []catalog{zhTW, enUS} {
		for command := range commandFlags {
			if !strings.Contains(c.Usage, "\n  "+command+" ") {
				t.Errorf("usage text does not list the %s command:\n%s", command, c.Usage)
			}
		}
		for _, flags := range []map[string]bool{valueFlags, boolFlags} {
			for flag := range flags {
				if !hidden[flag] && !strings.Contains(c.Usage, flag) {
					t.Errorf("usage text does not mention %s:\n%s", flag, c.Usage)
				}
			}
		}
		for short := range shortFlags {
			if !strings.Contains(c.Usage, "  "+short+", ") {
				t.Errorf("usage text does not mention %s:\n%s", short, c.Usage)
			}
		}
	}
}

func TestParseLanguage(t *testing.T) {
	cases := map[string]language{
		"en": langEN, "EN": langEN, "en-US": langEN, "en_GB": langEN,
		"zh": langZhTW, "zh-TW": langZhTW, "zh_tw": langZhTW, "zh-Hant": langZhTW, " zh-HK ": langZhTW,
	}
	for in, want := range cases {
		if got, ok := parseLanguage(in); !ok || got != want {
			t.Errorf("parseLanguage(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "fr", "ja-JP", "english", "zhongwen"} {
		if _, ok := parseLanguage(in); ok {
			t.Errorf("parseLanguage(%q) should be rejected", in)
		}
	}
}

func TestLanguageFromSystem(t *testing.T) {
	cases := []struct {
		tags []string
		want language
	}{
		{[]string{"zh-TW", "en-US"}, langZhTW},
		{[]string{"zh-Hant-TW"}, langZhTW},
		{[]string{"zh-HK"}, langZhTW},
		{[]string{"zh-MO"}, langZhTW},
		// 只有繁體才用中文介面；簡體系統沒有對應的翻譯，用英文。
		{[]string{"zh-CN"}, langEN},
		{[]string{"zh-Hans-CN", "zh-TW"}, langEN},
		// 只看第一順位：顯示語言是英文的人，即使清單裡有中文也給英文。
		{[]string{"en-US", "zh-TW"}, langEN},
		{[]string{"ja-JP"}, langEN},
		{nil, langEN},
	}
	for _, c := range cases {
		if got := languageFromSystem(c.tags); got != c.want {
			t.Errorf("languageFromSystem(%v) = %v, want %v", c.tags, got, c.want)
		}
	}
}

func TestPickLanguage(t *testing.T) {
	zhSystem, enSystem := []string{"zh-TW"}, []string{"en-US"}
	cases := []struct {
		name   string
		args   []string
		env    string
		system []string
		want   language
	}{
		{"flag beats everything", []string{"run", "--lang", "en"}, "zh-TW", zhSystem, langEN},
		{"flag with equals sign", []string{"--lang=zh-TW", "run"}, "", enSystem, langZhTW},
		{"the last flag wins", []string{"--lang", "en", "run", "--lang", "zh-TW"}, "", enSystem, langZhTW},
		{"environment beats the system", nil, "en", zhSystem, langEN},
		{"environment with underscore", nil, "zh_TW", enSystem, langZhTW},
		{"system language by default", []string{"run"}, "", zhSystem, langZhTW},
		{"English when nothing says otherwise", nil, "", nil, langEN},
		// 寫錯的值不能讓程式掛掉：先退回後面的來源，錯誤由 parseArgs 回報。
		{"bad flag value falls through", []string{"--lang", "fr"}, "", zhSystem, langZhTW},
		{"bad environment value falls through", nil, "klingon", zhSystem, langZhTW},
		{"flag without a value falls through", []string{"--lang"}, "", zhSystem, langZhTW},
	}
	for _, c := range cases {
		if got := pickLanguage(c.args, c.env, c.system); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// 給使用者看的文字只能放在 i18n.go：其他原始檔的字串裡出現中文，代表有一句話沒有英文版。
func TestNoHardCodedChinese(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	if len(files) == 0 {
		t.Skip("source files are not available next to the test binary")
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "i18n.go" {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && (lit.Kind == token.STRING || lit.Kind == token.CHAR) && hasCJK(lit.Value) {
				t.Errorf("%s: hard-coded Chinese text %s; move it into the catalog in i18n.go", fset.Position(lit.Pos()), lit.Value)
			}
			return true
		})
	}
}
