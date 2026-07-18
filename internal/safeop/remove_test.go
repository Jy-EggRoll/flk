package safeop

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveWithConfirm_BlocksHomeDirectChild 验证删除家目录顶层子项时被安全校验拦截，不会真正删除
// 对应修复点：ValidateSafePath 现在禁止删除 ~/.ssh 等家目录顶层子项
func TestRemoveWithConfirm_BlocksHomeDirectChild(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("无法获取家目录，跳过")
	}
	// 仅校验逻辑，不需要真实存在该路径
	target := filepath.Join(home, ".ssh")
	_, err = RemoveWithConfirm(target, RemoveOptions{Force: true})
	if err == nil {
		t.Fatalf("RemoveWithConfirm 应拦截家目录顶层子项 %s", target)
	}
}

// TestRemoveWithConfirm_DeletesAllowedPath 验证允许删除的路径在 --force 下可被删除
func TestRemoveWithConfirm_DeletesAllowedPath(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "to-remove.txt")
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatalf("创建测试文件失败: %v", err)
	}
	_, err := RemoveWithConfirm(target, RemoveOptions{Force: true})
	if err != nil {
		t.Fatalf("RemoveWithConfirm 应成功删除允许路径: %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatal("目标文件应已被删除")
	}
}

// TestRemoveWithConfirm_Cancel 验证非 force 模式下用户取消会返回 ErrOperationCancelled
func TestRemoveWithConfirm_Cancel(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "keep.txt")
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatalf("创建测试文件失败: %v", err)
	}
	_, err := RemoveWithConfirm(target, RemoveOptions{
		Confirm: func() (bool, error) { return false, nil },
	})
	if !errors.Is(err, ErrOperationCancelled) {
		t.Fatalf("应返回 ErrOperationCancelled，实际: %v", err)
	}
	if _, statErr := os.Stat(target); os.IsNotExist(statErr) {
		t.Fatal("取消操作后文件不应被删除")
	}
}
