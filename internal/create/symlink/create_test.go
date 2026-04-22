package symlink

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/safeop"
)

func TestMain(m *testing.M) {
	logger.Init(nil)
	m.Run()
}

func TestCreate_Basic(t *testing.T) {
	tmpDir := t.TempDir()
	realPath := filepath.Join(tmpDir, "real")
	fakePath := filepath.Join(tmpDir, "link")

	if err := os.WriteFile(realPath, []byte("test"), 0644); err != nil {
		t.Fatalf("create real file failed: %v", err)
	}

	err := Create(realPath, fakePath, false)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	link, err := os.Readlink(fakePath)
	if err != nil {
		t.Fatalf("readlink failed: %v", err)
	}
	if link != realPath {
		t.Fatalf("symlink target mismatch: got %s, want %s", link, realPath)
	}
}

func TestCreate_RealNotExist(t *testing.T) {
	tmpDir := t.TempDir()
	realPath := filepath.Join(tmpDir, "notexist")
	fakePath := filepath.Join(tmpDir, "link")

	err := Create(realPath, fakePath, false)
	if err == nil {
		t.Fatal("Create should fail when real does not exist")
	}
}

func TestCreate_ForceOverwrite(t *testing.T) {
	tmpDir := t.TempDir()
	realPath := filepath.Join(tmpDir, "real")
	fakePath := filepath.Join(tmpDir, "link")

	if err := os.WriteFile(realPath, []byte("test"), 0644); err != nil {
		t.Fatalf("create real file failed: %v", err)
	}
	if err := os.WriteFile(fakePath, []byte("existing"), 0644); err != nil {
		t.Fatalf("create fake file failed: %v", err)
	}

	err := Create(realPath, fakePath, true)
	if err != nil {
		t.Fatalf("Create with force failed: %v", err)
	}

	link, err := os.Readlink(fakePath)
	if err != nil {
		t.Fatalf("readlink failed: %v", err)
	}
	if link != realPath {
		t.Fatalf("symlink not created: got %s", link)
	}
}

func TestCreate_CreatesParentDir(t *testing.T) {
	tmpDir := t.TempDir()
	realPath := filepath.Join(tmpDir, "real")
	fakePath := filepath.Join(tmpDir, "subdir", "link")

	if err := os.WriteFile(realPath, []byte("test"), 0644); err != nil {
		t.Fatalf("create real file failed: %v", err)
	}

	err := Create(realPath, fakePath, false)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmpDir, "subdir")); err != nil {
		t.Fatalf("parent dir not created: %v", err)
	}
}

func TestCreate_DirSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	realPath := filepath.Join(tmpDir, "real")
	fakePath := filepath.Join(tmpDir, "link")

	if err := os.MkdirAll(realPath, 0755); err != nil {
		t.Fatalf("create real dir failed: %v", err)
	}

	err := Create(realPath, fakePath, false)
	if err != nil {
		t.Fatalf("Create failed for dir: %v", err)
	}

	link, err := os.Readlink(fakePath)
	if err != nil {
		t.Fatalf("readlink failed: %v", err)
	}
	if link != realPath {
		t.Fatalf("symlink target mismatch: got %s, want %s", link, realPath)
	}
}

func TestCreate_UserCancelForceFalse(t *testing.T) {
	tmpDir := t.TempDir()
	realPath := filepath.Join(tmpDir, "real")
	fakePath := filepath.Join(tmpDir, "link")

	if err := os.WriteFile(realPath, []byte("test"), 0644); err != nil {
		t.Fatalf("create real file failed: %v", err)
	}
	if err := os.WriteFile(fakePath, []byte("existing"), 0644); err != nil {
		t.Fatalf("create fake file failed: %v", err)
	}

	removed, err := safeop.RemoveWithConfirm(fakePath, safeop.RemoveOptions{
		Force:   false,
		Confirm: func() (bool, error) { return false, nil },
	})
	if err != safeop.ErrOperationCancelled {
		t.Fatalf("expected cancelled, got %v", err)
	}
	if removed != nil {
		t.Fatal("nothing should be removed on cancel")
	}
}

func TestCreate_ParentIsFile(t *testing.T) {
	tmpDir := t.TempDir()
	realPath := filepath.Join(tmpDir, "real")
	parentAsFile := filepath.Join(tmpDir, "parent")
	fakePath := filepath.Join(parentAsFile, "link")

	if err := os.WriteFile(realPath, []byte("test"), 0644); err != nil {
		t.Fatalf("create real file failed: %v", err)
	}
	if err := os.WriteFile(parentAsFile, []byte("file"), 0644); err != nil {
		t.Fatalf("create parent file failed: %v", err)
	}

	err := Create(realPath, fakePath, true)
	if err != nil {
		t.Fatalf("Create should handle parent being file: %v", err)
	}

	info, err := os.Stat(parentAsFile)
	if err != nil {
		t.Fatalf("parent dir should exist after create: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("parent should be a directory after create")
	}

	link, err := os.Readlink(fakePath)
	if err != nil {
		t.Fatalf("readlink failed: %v", err)
	}
	if link != realPath {
		t.Fatalf("symlink target mismatch: got %s, want %s", link, realPath)
	}
}
