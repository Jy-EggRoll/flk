package jsonfile

import (
	"strings"
	"testing"
)

// TestMarshalSortsKeys 断言键按字典序输出，保证文件内容稳定、diff 可评审。
func TestMarshalSortsKeys(t *testing.T) {
	got, err := Marshal(map[string]string{"zeta": "z", "alpha": "a", "mid": "m"})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	iAlpha := strings.Index(string(got), "alpha")
	iMid := strings.Index(string(got), "mid")
	iZeta := strings.Index(string(got), "zeta")
	if !(iAlpha < iMid && iMid < iZeta) {
		t.Errorf("键未按字典序排列:\n%s", got)
	}
}

// TestMarshalDoesNotEscapeHTML 断言 & < > 不被转义成 \u0026 之类。
//
// 这是本包存在的核心理由：Go 标准库默认开启 HTML 转义，而配置里的路径与
// 语言文件里的分桶标签（如 ">{{.High}}MB"）都含这些字符。
func TestMarshalDoesNotEscapeHTML(t *testing.T) {
	got, err := Marshal(map[string]string{"a&b": "<{{.Low}}MB > tail"})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	s := string(got)
	if strings.Contains(s, `\u0026`) || strings.Contains(s, `\u003c`) || strings.Contains(s, `\u003e`) {
		t.Errorf("HTML 字符被转义了:\n%s", s)
	}
	if !strings.Contains(s, "a&b") || !strings.Contains(s, "<{{.Low}}MB > tail") {
		t.Errorf("原始字符丢失:\n%s", s)
	}
}

// TestMarshalIndentAndTrailingNewline 断言 2 空格缩进且结尾带换行。
func TestMarshalIndentAndTrailingNewline(t *testing.T) {
	got, err := Marshal(map[string]string{"k": "v"})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	const want = "{\n  \"k\": \"v\"\n}\n"
	if string(got) != want {
		t.Errorf("输出形态不符:\n实得 %q\n期望 %q", got, want)
	}
}
