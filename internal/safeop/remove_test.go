package safeop

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveWithConfirm_NonForce_ConfirmYes(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "to-delete")
	if err := os.MkdirAll(filepath.Join(target, "sub"), 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "sub", "b.txt"), []byte("b"), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	var buf bytes.Buffer
	confirmCalled := false
	deleted, err := RemoveWithConfirm(target, RemoveOptions{
		Force:  false,
		Output: &buf,
		Confirm: func() (bool, error) {
			confirmCalled = true
			return true, nil
		},
	})
	if err != nil {
		t.Fatalf("RemoveWithConfirm failed: %v", err)
	}
	if !confirmCalled {
		t.Fatal("expected confirm to be called in non-force mode")
	}

	if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target should be removed, got err: %v", statErr)
	}

	absTarget, _ := filepath.Abs(target)
	absA, _ := filepath.Abs(filepath.Join(target, "a.txt"))
	absSub, _ := filepath.Abs(filepath.Join(target, "sub"))
	absB, _ := filepath.Abs(filepath.Join(target, "sub", "b.txt"))

	out := buf.String()
	if !strings.Contains(out, "有以下文件会在恢复过程中被删除:") {
		t.Fatalf("missing delete header, output=%q", out)
	}
	for _, p := range []string{absTarget, absA, absSub, absB} {
		if !strings.Contains(out, p) {
			t.Fatalf("missing deleted path %q in output=%q", p, out)
		}
	}
	if !strings.Contains(out, "您是否确认[y/N]") {
		t.Fatalf("missing confirm prompt, output=%q", out)
	}
	if len(deleted) != 4 {
		t.Fatalf("unexpected deleted count: got %d want 4", len(deleted))
	}
}

func TestRemoveWithConfirm_NonForce_ConfirmNo(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "to-keep")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	var buf bytes.Buffer
	_, err := RemoveWithConfirm(target, RemoveOptions{
		Force:  false,
		Output: &buf,
		Confirm: func() (bool, error) {
			return false, nil
		},
	})
	if !errors.Is(err, ErrOperationCancelled) {
		t.Fatalf("expected ErrOperationCancelled, got: %v", err)
	}

	if _, statErr := os.Lstat(target); statErr != nil {
		t.Fatalf("target should still exist, got err: %v", statErr)
	}

	out := buf.String()
	if !strings.Contains(out, "有以下文件会在恢复过程中被删除:") {
		t.Fatalf("missing delete header, output=%q", out)
	}
	if !strings.Contains(out, "您是否确认[y/N]") {
		t.Fatalf("missing confirm prompt, output=%q", out)
	}
}

func TestRemoveWithConfirm_Force_SkipsConfirmAndDeletes(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "force-delete")
	if err := os.MkdirAll(filepath.Join(target, "sub"), 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "sub", "x.txt"), []byte("x"), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	var buf bytes.Buffer
	confirmCalled := false
	deleted, err := RemoveWithConfirm(target, RemoveOptions{
		Force:  true,
		Output: &buf,
		Confirm: func() (bool, error) {
			confirmCalled = true
			return false, nil
		},
	})
	if err != nil {
		t.Fatalf("RemoveWithConfirm failed: %v", err)
	}
	if confirmCalled {
		t.Fatal("confirm should not be called in force mode")
	}

	if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target should be removed, got err: %v", statErr)
	}

	out := buf.String()
	if !strings.Contains(out, "有以下文件会在恢复过程中被删除:") {
		t.Fatalf("missing delete header, output=%q", out)
	}
	if !strings.Contains(out, "您是否确认[y/N]") {
		t.Fatalf("missing confirm prompt in force mode output=%q", out)
	}
	if len(deleted) != 3 {
		t.Fatalf("unexpected deleted count: got %d want 3", len(deleted))
	}
}
