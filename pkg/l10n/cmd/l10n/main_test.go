package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
