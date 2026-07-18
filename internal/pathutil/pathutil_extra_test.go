package pathutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestExpandHome_InvalidPrefix 验证非法 ~ 前缀（如 ~foo）返回明确错误，而不是静默返回空路径
// 对应修复点：ExpandHome 对非 ~/、~\ 的非法前缀原实现会返回 ("", nil)，导致后续逻辑误用空路径
func TestExpandHome_InvalidPrefix(t *testing.T) {
	_, err := ExpandHome("~foo/bar")
	if err == nil {
		t.Fatal("ExpandHome 对非法前缀 ~foo/bar 应返回错误，但实际返回 nil")
	}
}

// TestExpandHome_TildeOnly 验证单独的 ~ 能正确展开为家目录
func TestExpandHome_TildeOnly(t *testing.T) {
	home, _ := os.UserHomeDir()
	got, err := ExpandHome("~")
	if err != nil {
		t.Fatalf("ExpandHome(~) 失败: %v", err)
	}
	if got != home {
		t.Fatalf("ExpandHome(~) 结果不符: got %s, want %s", got, home)
	}
}

// TestExpandHome_TildeSlash 验证 ~/ 前缀正确拼接
func TestExpandHome_TildeSlash(t *testing.T) {
	home, _ := os.UserHomeDir()
	got, err := ExpandHome("~/config")
	if err != nil {
		t.Fatalf("ExpandHome(~/) 失败: %v", err)
	}
	want := filepath.Join(home, "config")
	if got != want {
		t.Fatalf("ExpandHome(~/) 结果不符: got %s, want %s", got, want)
	}
}

// TestValidateSafePath_HomeDirectChild 验证禁止删除家目录的顶层子项（如 ~/.ssh、~/.config）
// 这是修复点：旧实现只拦截家目录本身，无法阻止误删 ~/.ssh 等关键目录
func TestValidateSafePath_HomeDirectChild(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("无法获取家目录，跳过")
	}
	target := filepath.Join(home, ".ssh")
	if err := ValidateSafePath(target); err == nil {
		t.Fatalf("ValidateSafePath 应拦截家目录顶层子项 %s，但返回了 nil", target)
	}
}

// TestValidateSafePath_HomeItself 验证禁止删除家目录本身
func TestValidateSafePath_HomeItself(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("无法获取家目录，跳过")
	}
	if err := ValidateSafePath(home); err == nil {
		t.Fatalf("ValidateSafePath 应拦截家目录本身 %s，但返回了 nil", home)
	}
}

// TestValidateSafePath_DeepChild 验证允许删除家目录下的深层用户管理目录（不应误拦）
func TestValidateSafePath_DeepChild(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("无法获取家目录，跳过")
	}
	// 家目录下两层的路径（flk 管理的子目录）应被允许
	target := filepath.Join(home, ".config", "myapp", "conf")
	if err := ValidateSafePath(target); err != nil {
		t.Fatalf("ValidateSafePath 不应拦截家目录深层子目录 %s: %v", target, err)
	}
}

// TestValidateSafePath_Empty 验证空路径报错
func TestValidateSafePath_Empty(t *testing.T) {
	if err := ValidateSafePath(""); err == nil {
		t.Fatal("ValidateSafePath 对空路径应返回错误")
	}
}

// TestValidateSafePath_Root 验证禁止删除根目录
func TestValidateSafePath_Root(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 根目录测试在 Unix 环境跳过")
	}
	if err := ValidateSafePath("/"); err == nil {
		t.Fatal("ValidateSafePath 应拦截根目录 /，但返回了 nil")
	}
}
