package trash

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMoveToTrash_File(t *testing.T) {
	origRoot := trashRoot
	t.Cleanup(func() { trashRoot = origRoot })

	tmpDir := t.TempDir()
	trashRoot = filepath.Join(tmpDir, "trash")

	testFile := filepath.Join(tmpDir, ".zshrc")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := MoveToTrash(testFile); err != nil {
		t.Fatalf("MoveToTrash 失败: %v", err)
	}

	if _, err := os.Stat(testFile); !os.IsNotExist(err) {
		t.Fatal("原文件应该已被移除")
	}

	found := false
	filepath.WalkDir(trashRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			found = true
		}
		return nil
	})
	if !found {
		t.Fatal("回收站中应该至少有一个文件")
	}
}

func TestMoveToTrash_Directory(t *testing.T) {
	origRoot := trashRoot
	t.Cleanup(func() { trashRoot = origRoot })

	tmpDir := t.TempDir()
	trashRoot = filepath.Join(tmpDir, "trash")

	testDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(testDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "b.txt"), []byte("b"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := MoveToTrash(testDir); err != nil {
		t.Fatalf("MoveToTrash 目录失败: %v", err)
	}

	if _, err := os.Stat(testDir); !os.IsNotExist(err) {
		t.Fatal("原目录应该已被移除")
	}

	foundA := false
	foundB := false
	filepath.WalkDir(trashRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			if d.Name() == "a.txt" {
				foundA = true
			}
			if d.Name() == "b.txt" {
				foundB = true
			}
		}
		return nil
	})
	if !foundA || !foundB {
		t.Fatal("回收站中应该保留目录内的所有文件")
	}
}

func TestMoveToTrash_NonExistent(t *testing.T) {
	origRoot := trashRoot
	t.Cleanup(func() { trashRoot = origRoot })

	tmpDir := t.TempDir()
	trashRoot = filepath.Join(tmpDir, "trash")

	err := MoveToTrash(filepath.Join(tmpDir, "nonexistent"))
	if err == nil {
		t.Fatal("不存在路径应该返回错误")
	}
}

func TestMoveToTrash_TrashPathStructure(t *testing.T) {
	origRoot := trashRoot
	t.Cleanup(func() { trashRoot = origRoot })

	tmpDir := t.TempDir()
	trashRoot = filepath.Join(tmpDir, "trash")

	homeRel := filepath.Join("home", "user")
	fullDir := filepath.Join(tmpDir, homeRel)
	if err := os.MkdirAll(fullDir, 0755); err != nil {
		t.Fatal(err)
	}
	testFile := filepath.Join(fullDir, ".zshrc")
	if err := os.WriteFile(testFile, []byte("config"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := MoveToTrash(testFile); err != nil {
		t.Fatalf("MoveToTrash 失败: %v", err)
	}

	found := false
	filepath.WalkDir(trashRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == ".zshrc" {
			found = true
		}
		return nil
	})
	if !found {
		t.Fatal("回收站应该保留完整的路径树结构")
	}
}
