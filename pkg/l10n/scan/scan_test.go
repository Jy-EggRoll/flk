package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testPkg 是测试用的"消息函数所在包"。刻意用一个与测试自身无关的路径，
// 以证明扫描器不含任何硬编码的包名假设。
const testPkg = "example.com/app/l10n"

// scanRoot 用固定的 Config 扫描临时仓库。
func scanRoot(t *testing.T, root string) (*Result, error) {
	t.Helper()
	return Scan(Config{Root: root, ImportPath: testPkg, SrcDirs: []string{"cmd", "internal"}})
}

// writeGo 在临时仓库里写一个 Go 源文件。
func writeGo(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入 %s 失败: %v", rel, err)
	}
}

// msgFile 拼出一个导入了消息包、并使用给定表达式体的源文件。
func msgFile(expr string) string {
	return `package cmd

import "` + testPkg + `"

` + expr + `
`
}

// TestScanIgnoresBareT 断言裸 T(...) 不算消息调用。
//
// 这是"只认显式选择器"的直接后果，也是它带来的最大好处：别的包里同名函数或
// 泛型类型参数（如 func Map[I any, T any] 里的 T）不会被误判成消息调用，
// 无关重构也就不会让门禁意外变红。
func TestScanIgnoresBareT(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "internal/worker/worker.go", `package worker

func Map[I any, T any](items []I) []T { return nil }

func convert(x int) {
	_ = T(x)
}

var _ = T("looks like a message but is not")
`)

	res, err := scanRoot(t, root)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if len(res.Messages) != 0 {
		t.Errorf("裸 T(...) 不应被识别为消息调用，实得 %d 条: %+v", len(res.Messages), res.Messages)
	}
}

// TestScanIgnoresOtherPackages 断言导入的是别的包时不算消息调用。
// 若不加这条约束，任何同名方法（如某个类型自己的 T 方法）都会被误收。
func TestScanIgnoresOtherPackages(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "cmd/x.go", `package cmd

import l10n "example.com/other/l10n"

var _ = l10n.T("from another library")
`)

	res, err := scanRoot(t, root)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if len(res.Messages) != 0 {
		t.Errorf("非目标包的调用不应被识别，实得 %+v", res.Messages)
	}
}

// TestScanRecognizesSelectorForm 断言目标包的显式选择器调用能被识别，
// 包括未起别名与起了别名两种写法。
func TestScanRecognizesSelectorForm(t *testing.T) {
	cases := map[string]string{
		"未起别名": `import "` + testPkg + `"

var _ = l10n.T("Message from another package", nil)`,
		"起了别名": `import loc "` + testPkg + `"

var _ = loc.T("Message from another package", nil)`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeGo(t, root, "internal/config/c.go", "package config\n\n"+body+"\n")

			res, err := scanRoot(t, root)
			if err != nil {
				t.Fatalf("扫描失败: %v", err)
			}
			if len(res.Messages) != 1 || res.Messages[0].Text != "Message from another package" {
				t.Errorf("选择器形式的调用未被识别: %+v", res.Messages)
			}
		})
	}
}

// TestScanRejectsNonLiteralArgument 断言首参不是字符串字面量时必须报错。
//
// 这是"源串即 id"模型的基础约束：id 必须能在源码里静态读出来，否则无法生成语言文件。
// 在调用方再包一层转发函数正是靠这条规则被拦下的。
func TestScanRejectsNonLiteralArgument(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "cmd/x.go", msgFile(`func use(v string) {
	_ = l10n.T(v, nil)
}`))

	_, err := scanRoot(t, root)
	if err == nil {
		t.Fatal("首参为变量时应报错")
	}
	if !strings.Contains(err.Error(), "字符串字面量") {
		t.Errorf("错误信息应说明首参必须是字符串字面量，实得: %v", err)
	}
}

// TestScanRejectsReservedWord 断言命中 go-i18n 保留字的消息会被拒绝。
func TestScanRejectsReservedWord(t *testing.T) {
	for _, id := range []string{"Other", "hash", "translation"} {
		root := t.TempDir()
		writeGo(t, root, "cmd/x.go", msgFile(`var _ = l10n.T("`+id+`", nil)`))

		if _, err := scanRoot(t, root); err == nil {
			t.Errorf("消息 id %q 命中保留字，应被拒绝", id)
		}
	}
}

// TestScanRejectsDirtyMultiLineMessage 断言多行消息不得含制表符或行尾空白。
//
// id 就是原文，源码里任何缩进或行尾空白的变化都会让 id 漂移、既有译文静默失效，
// 所以这条约束必须前移到提取阶段。
func TestScanRejectsDirtyMultiLineMessage(t *testing.T) {
	cases := map[string]string{
		"含制表符": `var _ = l10n.T("line one\n\tline two", nil)`,
		"行尾空白": `var _ = l10n.T("line one\nline two ", nil)`,
		"首尾空白": `var _ = l10n.T(" leading", nil)`,
	}
	for name, expr := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeGo(t, root, "cmd/x.go", msgFile(expr))

			if _, err := scanRoot(t, root); err == nil {
				t.Errorf("%s 应被拒绝", name)
			}
		})
	}
}

// TestScanSeparatesWrappedAndUnwrapped 断言已迁移与未迁移文案被正确分开统计。
func TestScanSeparatesWrappedAndUnwrapped(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "cmd/x.go", msgFile(`var wrapped = l10n.T("Already migrated", nil)
var legacy = "尚未迁移的中文"
var plain = "ascii only"`))

	res, err := scanRoot(t, root)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Text != "Already migrated" {
		t.Errorf("消息集合不符: %+v", res.Messages)
	}
	if len(res.Unwrapped) != 1 || res.Unwrapped[0].Text != "尚未迁移的中文" {
		t.Errorf("待迁移集合不符: %+v", res.Unwrapped)
	}
}

// TestScanSkipsTestFiles 断言测试文件不参与扫描。
// 测试里会出现刻意不存在的消息 id，扫进来会造成误报。
func TestScanSkipsTestFiles(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "cmd/x_test.go", msgFile(`var _ = l10n.T("test-only message", nil)`))

	res, err := scanRoot(t, root)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if len(res.Messages) != 0 {
		t.Errorf("测试文件不应参与扫描，实得 %+v", res.Messages)
	}
}

// TestScanIgnoresTypeParamT 断言泛型类型参数 T 不会被误当成消息函数。
//
// 这条其实已由"只认选择器"覆盖，但它是历史上真实踩过的坑（worker 包的
// func Map[I any, T any] 曾让裸 T 匹配方案产生误报），留作回归测试。
func TestScanIgnoresTypeParamT(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "cmd/x.go", `package cmd

func convert[V any, T any](x V) {
	_ = T
}
`)

	res, err := scanRoot(t, root)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if len(res.Messages) != 0 {
		t.Errorf("不应识别出任何消息，实得 %+v", res.Messages)
	}
}

// TestScanRequiresImportPath 断言 ImportPath 为空时直接报错，
// 而不是静默扫出 0 条消息——那正是最坏的门禁失效形态。
func TestScanRequiresImportPath(t *testing.T) {
	if _, err := Scan(Config{Root: t.TempDir()}); err == nil {
		t.Fatal("ImportPath 为空时应报错")
	}
}
