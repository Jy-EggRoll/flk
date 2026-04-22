package hardlink

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
	primPath := filepath.Join(tmpDir, "prim")
	secoPath := filepath.Join(tmpDir, "seco")

	if err := os.WriteFile(primPath, []byte("test"), 0644); err != nil {
		t.Fatalf("create prim file failed: %v", err)
	}

	err := Create(primPath, secoPath, false)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	primInfo, _ := os.Stat(primPath)
	secoInfo, _ := os.Stat(secoPath)
	if primInfo.Mode() != secoInfo.Mode() || primInfo.Size() != secoInfo.Size() {
		t.Fatal("hardlink not created correctly")
	}
}

func TestCreate_PrimNotExist(t *testing.T) {
	tmpDir := t.TempDir()
	primPath := filepath.Join(tmpDir, "notexist")
	secoPath := filepath.Join(tmpDir, "seco")

	err := Create(primPath, secoPath, false)
	if err == nil {
		t.Fatal("Create should fail when prim does not exist")
	}
}

func TestCreate_ForceOverwrite(t *testing.T) {
	tmpDir := t.TempDir()
	primPath := filepath.Join(tmpDir, "prim")
	secoPath := filepath.Join(tmpDir, "seco")

	if err := os.WriteFile(primPath, []byte("test"), 0644); err != nil {
		t.Fatalf("create prim file failed: %v", err)
	}
	if err := os.WriteFile(secoPath, []byte("existing"), 0644); err != nil {
		t.Fatalf("create seco file failed: %v", err)
	}

	if err := Create(primPath, secoPath, true); err != nil {
		t.Fatalf("Create with force failed: %v", err)
	}

	content, _ := os.ReadFile(secoPath)
	if string(content) != "test" {
		t.Fatalf("seco not overwritten: %s", string(content))
	}
}

func TestCreate_CreatesParentDir(t *testing.T) {
	tmpDir := t.TempDir()
	primPath := filepath.Join(tmpDir, "prim")
	secoPath := filepath.Join(tmpDir, "subdir", "seco")

	if err := os.WriteFile(primPath, []byte("test"), 0644); err != nil {
		t.Fatalf("create prim file failed: %v", err)
	}

	err := Create(primPath, secoPath, false)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmpDir, "subdir")); err != nil {
		t.Fatalf("parent dir not created: %v", err)
	}
}

func TestCreate_UserCancel(t *testing.T) {
	tmpDir := t.TempDir()
	primPath := filepath.Join(tmpDir, "prim")
	secoPath := filepath.Join(tmpDir, "seco")

	if err := os.WriteFile(primPath, []byte("test"), 0644); err != nil {
		t.Fatalf("create prim file failed: %v", err)
	}
	if err := os.WriteFile(secoPath, []byte("existing"), 0644); err != nil {
		t.Fatalf("create seco file failed: %v", err)
	}

	_, err := safeop.RemoveWithConfirm(secoPath, safeop.RemoveOptions{
		Force:   false,
		Confirm: func() (bool, error) { return false, nil },
	})
	if err != safeop.ErrOperationCancelled {
		t.Fatalf("expected cancelled, got %v", err)
	}
}

func TestCreate_ParentIsFile(t *testing.T) {
	tmpDir := t.TempDir()
	primPath := filepath.Join(tmpDir, "prim")
	parentAsFile := filepath.Join(tmpDir, "parent")
	secoPath := filepath.Join(parentAsFile, "seco")

	if err := os.WriteFile(primPath, []byte("test"), 0644); err != nil {
		t.Fatalf("create prim file failed: %v", err)
	}
	if err := os.WriteFile(parentAsFile, []byte("file"), 0644); err != nil {
		t.Fatalf("create parent file failed: %v", err)
	}

	err := Create(primPath, secoPath, true)
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

	content, _ := os.ReadFile(secoPath)
	if string(content) != "test" {
		t.Fatalf("seco content mismatch: %s", string(content))
	}
}
