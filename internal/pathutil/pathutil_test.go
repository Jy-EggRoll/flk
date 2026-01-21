package pathutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestNormalizePath_Windows 仅在 Windows 上运行，验证最小行为：
// 1. "~" 能展开为用户主目录
// 2. "~/a\b/c" 能被展开为 home\a\b\c（不检查错误返回值）
func TestNormalizePath_Windows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅在 Windows 上运行")
	}

	home, _ := os.UserHomeDir()

	// 1) ~ 展开
	got, _ := NormalizePath("~")
	if got != home {
		t.Fatalf("~ 展开失败: got=%q want=%q", got, home)
	}

	// 2) mixed separators, 用户输入 ~/a\b/c
	got2, _ := NormalizePath(`~/a\b/c`)
	want2 := filepath.Join(home, "a", "b", "c")
	if got2 != want2 {
		t.Fatalf("~/a\\b/c 展开失败: got=%q want=%q", got2, want2)
	}
}

// TestNormalizePath_Clean 验证一般路径会被 Clean
func TestNormalizePath_Clean(t *testing.T) {
	input := "./aa.txt"
	got, err := NormalizePath(input)
	if err != nil {
		t.Fatalf("NormalizePath 返回错误: %v", err)
	}
	want := filepath.Clean(input)
	if got != want {
		t.Fatalf("路径未被 Clean: got=%q want=%q", got, want)
	}
}
