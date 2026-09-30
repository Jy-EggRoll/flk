package main

import (
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jy-eggroll/flk/pkg/l10n/scan"
)

// 本文件只测 export 的报告职责，不测 check：check 才是语言文件的门禁
// 测试在临时目录里搭一个最小仓库（一份源码 + 一份默认语言文件 + 一份译文文件），
// 端到端跑 cmdExport，避免动到真实的 internal/locales

// writeFile 在测试仓库里落一个文件，目录不存在时自动创建
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("写入 %s 失败: %v", path, err)
	}
}

// captureStdout 临时接管 os.Stdout，收集一次调用期间打印的全部内容
// reportTranslations 直接写 fmt.Printf，若不接管就没法断言它到底报了什么
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("创建管道失败: %v", err)
	}
	os.Stdout = w
	runErr := fn()
	_ = w.Close()
	os.Stdout = orig

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("读取输出失败: %v", err)
	}
	_ = r.Close()
	return string(out), runErr
}

// setupRepo 搭一个最小仓库：源码引用一条消息，默认语言与译文文件按参数给定
func setupRepo(t *testing.T, enJSON, zhJSON string) options {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "src", "demo", "demo.go"),
		"package demo\n\nimport l10n \"github.com/jy-eggroll/flk/pkg/l10n\"\n\nvar _ = l10n.T(\"Hello\", nil)\n")
	locales := filepath.Join(root, "internal", "locales")
	writeFile(t, filepath.Join(locales, "en.json"), enJSON)
	writeFile(t, filepath.Join(locales, "zh-CN.json"), zhJSON)

	return options{
		root:    root,
		pkg:     "github.com/jy-eggroll/flk/pkg/l10n",
		srcDirs: []string{"src"},
		dir:     locales,
		def:     "en",
	}
}

// TestExportReportsStaleTranslations 覆盖「存在失效译文时 export 会提示」
//
// 失效译文指默认语言（= 源码）里已经没有、却仍留在译文文件里的条目
// 它以前只能等 l10n:check 判失败才发现，本测试锁定的正是提示被前移到 export
func TestExportReportsStaleTranslations(t *testing.T) {
	cases := map[string]struct {
		zh       string
		wantHint bool
	}{
		"存在失效译文": {
			zh:       `{"Hello": "你好", "This message was removed": "这条原文已被删除"}`,
			wantHint: true,
		},
		"无失效译文": {
			zh:       `{"Hello": "你好"}`,
			wantHint: false,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			o := setupRepo(t, `{"Hello": "Hello"}`, c.zh)
			out, err := captureStdout(t, func() error { return cmdExport(o) })
			if err != nil {
				t.Fatalf("cmdExport 失败: %v", err)
			}
			hasHint := strings.Contains(out, "失效译文")
			if hasHint != c.wantHint {
				t.Errorf("失效提示 = %v, want %v；输出:\n%s", hasHint, c.wantHint, out)
			}
			if c.wantHint && !strings.Contains(out, "This message was removed") {
				t.Errorf("失效清单应列出具体条目，实得输出:\n%s", out)
			}

			// export 只提示、不代删：译文文件是译者手工维护的，直接删会掩盖「原文被误改」
			// 因此这里断言那条失效条目仍在文件里，锁定「不增删内容」这一既有契约
			got, err := os.ReadFile(filepath.Join(o.dir, "zh-CN.json"))
			if err != nil {
				t.Fatalf("读取译文文件失败: %v", err)
			}
			if c.wantHint && !strings.Contains(string(got), "This message was removed") {
				t.Errorf("export 不得擅自删除失效条目，实得文件内容:\n%s", got)
			}
		})
	}
}

// TestHTMLTranslationProblems 覆盖页面内嵌翻译表的缺口判定。
//
// 这是本次把 WebUI 纳入门禁的核心：页面缺译文只会静默回退英文，不留任何痕迹，
// 所以它必须被判为错误（与译文文件的"缺口只提示"刻意不同），否则门禁形同虚设
func TestHTMLTranslationProblems(t *testing.T) {
	cases := map[string]struct {
		table    scan.HTMLTable
		wantSubs []string // 期望问题里的关键片段，为空表示期望无问题
	}{
		"完整覆盖": {
			table: scan.HTMLTable{
				Langs: []string{"zh-CN"},
				Msgs:  []scan.HTMLMessage{{Text: "Save", Langs: []string{"zh-CN"}}},
			},
		},
		"某条缺译文": {
			table: scan.HTMLTable{
				Langs: []string{"zh-CN"},
				Msgs:  []scan.HTMLMessage{{Text: "Save", Langs: []string{"zh-CN"}}, {Text: "Cancel"}},
			},
			wantSubs: []string{"Cancel", "静默回退英文"},
		},
		"整门语言缺语言段": {
			table:    scan.HTMLTable{Msgs: []scan.HTMLMessage{{Text: "Save"}}},
			wantSubs: []string{`缺少语言段 "zh-CN"`},
		},
	}
	// 默认语言不需要语言段：查不到译文时回退的英文源串就是 key 本身
	langs := []string{"en", "zh-CN"}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tbl := c.table
			tbl.Pos = token.Position{Filename: "cmd/ui/config.html", Line: 265}
			res := &scan.Result{HTMLTables: []scan.HTMLTable{tbl}}
			got := htmlTranslationProblems(res, langs, "en")
			if len(c.wantSubs) == 0 {
				if len(got) != 0 {
					t.Fatalf("期望无问题，实得 %q", got)
				}
				return
			}
			joined := strings.Join(got, "\n")
			for _, sub := range c.wantSubs {
				if !strings.Contains(joined, sub) {
					t.Errorf("问题里应含 %q，实得:\n%s", sub, joined)
				}
			}
			if !strings.Contains(joined, "config.html:265") {
				t.Errorf("问题应带页面位置，实得:\n%s", joined)
			}
		})
	}
}

// TestExportReportsHTMLTableCoverage 覆盖 export 对页面翻译表的报告职责：
//   - 页面已提供的译文不算缺口（否则报告里会一直挂着一批并不存在的缺口）
//   - 页面缺语言段要提示
func TestExportReportsHTMLTableCoverage(t *testing.T) {
	const page = `<html><body><script>
var MSG = { 'zh-CN': { 'Save': '保存' } };
</script></body></html>
`
	cases := map[string]struct {
		html       string
		wantSubs   []string
		rejectSubs []string
	}{
		"页面提供了译文就不算缺口": {
			html:     page,
			wantSubs: []string{"1 条译文由 WebUI 内嵌翻译表提供", "语言段 \"zh-CN\" 已覆盖全部消息"},
			// 页面已提供译文，它就不该出现在待翻译清单里
			rejectSubs: []string{"未翻译: Save"},
		},
		"页面缺语言段要提示": {
			html:     `<html><body><script>var MSG = { 'en': { 'Save': 'Save' } };</script></body></html>`,
			wantSubs: []string{"缺少语言段 \"zh-CN\""},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			// 默认语言里含 Save（来自页面）、Hello（来自 Go）；译文文件只翻了 Hello
			o := setupRepo(t, `{"Hello": "Hello", "Save": "Save"}`, `{"Hello": "你好"}`)
			writeFile(t, filepath.Join(o.root, "ui", "page.html"), c.html)
			o.htmlSrc = []string{"ui"}

			out, err := captureStdout(t, func() error { return cmdExport(o) })
			if err != nil {
				t.Fatalf("cmdExport 失败: %v", err)
			}
			for _, sub := range c.wantSubs {
				if !strings.Contains(out, sub) {
					t.Errorf("输出里应含 %q，实得:\n%s", sub, out)
				}
			}
			for _, sub := range c.rejectSubs {
				if strings.Contains(out, sub) {
					t.Errorf("输出里不应含 %q，实得:\n%s", sub, out)
				}
			}
		})
	}
}
