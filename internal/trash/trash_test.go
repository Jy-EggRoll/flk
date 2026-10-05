package trash

import (
	"errors"
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

// TestMoveToTrash_CrossDeviceFallback 守住「rename 失败时降级为复制再删除」这条回退路径
//
// 前因（用户实际报来的故障）：回收站根目录固定在用户家目录所在的卷（Windows 上即 C:），
// 而被删文件可能在另一个卷（D:）或另一个挂载点，此时 os.Rename 必然失败。
// 这条路径此前直接把错误抛给用户，表现为「网页上修复/解除失败，且页面上没有任何开关能绕过」
// （WebUI 的修复端点没有 noTrash 通路，用户只能改成真实删除来回避，那是数据不可恢复的做法）
//
// 手段是注入一个必定失败的 renameFunc，而不是真去找两块文件系统：
// CI 容器里不一定有第二块可写文件系统，而这条逻辑必须在任何环境下都被覆盖。
// 刻意用一个普通错误而不是 syscall.EXDEV：实现不按 errno 区分失败原因，任何 rename 失败都走同一条回退路径
//
// 用目录（内含普通文件与符号链接）作为被测对象，覆盖递归复制这条最容易出错的分支；
// 符号链接必须原样保留，一旦被跟随复制，删一个链接就会把指向的真实数据也搬进回收站
func TestMoveToTrash_CrossDeviceFallback(t *testing.T) {
	origRoot := trashRoot
	origRename := renameFunc
	t.Cleanup(func() {
		trashRoot = origRoot
		renameFunc = origRename
	})

	tmpDir := t.TempDir()
	trashRoot = filepath.Join(tmpDir, "trash")
	renameFunc = func(_, _ string) error { return errors.New("cross-device link") }

	realTarget := filepath.Join(tmpDir, "real-target.txt")
	if err := os.WriteFile(realTarget, []byte("权威数据"), 0644); err != nil {
		t.Fatal(err)
	}

	srcDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realTarget, filepath.Join(srcDir, "link.txt")); err != nil {
		t.Fatal(err)
	}

	if err := MoveToTrash(srcDir); err != nil {
		t.Fatalf("跨设备回退失败: %v", err)
	}

	if _, err := os.Lstat(srcDir); !os.IsNotExist(err) {
		t.Fatal("回退成功后源目录应该已被移除")
	}

	var trashDir string
	filepath.WalkDir(trashRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "subdir" {
			trashDir = path
		}
		return nil
	})
	if trashDir == "" {
		t.Fatal("回收站中应该保留原路径树（subdir）")
	}

	content, err := os.ReadFile(filepath.Join(trashDir, "a.txt"))
	if err != nil {
		t.Fatalf("读取回收站中的文件失败: %v", err)
	}
	if string(content) != "a" {
		t.Errorf("回收站中的文件内容 = %q，期望 %q", content, "a")
	}

	linkInfo, err := os.Lstat(filepath.Join(trashDir, "link.txt"))
	if err != nil {
		t.Fatalf("回收站中的符号链接丢失: %v", err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Error("符号链接被跟随复制成了普通文件：删链接不应把指向的真实数据搬进回收站")
	}

	// 符号链接指向的真实数据必须原地不动
	if _, err := os.Stat(realTarget); err != nil {
		t.Fatalf("符号链接指向的真实文件不应被移动或删除: %v", err)
	}
}

// TestMoveToTrash_RealCrossDevice 用真实的两块文件系统验证同一条回退路径
//
// 与上一条的区别：那条证明逻辑分支正确，这条证明在真实 EXDEV 下真的能跑通。
// 探测方式是对两块目录之间做一次真实的 rename 探针——比比较设备号更可移植，
// 也避免引入 syscall.Stat_t 这类在 Windows 上不存在的类型（本文件必须能在所有平台编译）
func TestMoveToTrash_RealCrossDevice(t *testing.T) {
	origRoot := trashRoot
	t.Cleanup(func() { trashRoot = origRoot })

	base := t.TempDir()

	for _, candidate := range []string{"/dev/shm", "/run/shm"} {
		if fi, err := os.Stat(candidate); err != nil || !fi.IsDir() {
			continue
		}
		other, err := os.MkdirTemp(candidate, "flk-trash-probe-")
		if err != nil {
			continue
		}

		// 探针：同设备时 rename 会成功，说明这条用例没有验证价值
		probe := filepath.Join(base, "probe")
		if err := os.WriteFile(probe, nil, 0644); err != nil {
			_ = os.RemoveAll(other)
			continue
		}
		probeDest := filepath.Join(other, "probe")
		if err := os.Rename(probe, probeDest); err == nil {
			_ = os.RemoveAll(other)
			continue
		}
		_ = os.Remove(probe)
		t.Cleanup(func() { _ = os.RemoveAll(other) })

		trashRoot = filepath.Join(other, "trash")
		testFile := filepath.Join(base, "config.txt")
		if err := os.WriteFile(testFile, []byte("config"), 0644); err != nil {
			t.Fatal(err)
		}

		if err := MoveToTrash(testFile); err != nil {
			t.Fatalf("真实跨设备场景下 MoveToTrash 失败: %v", err)
		}
		if _, err := os.Stat(testFile); !os.IsNotExist(err) {
			t.Fatal("源文件应该已被移除")
		}

		found := false
		filepath.WalkDir(trashRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && d.Name() == "config.txt" {
				found = true
			}
			return nil
		})
		if !found {
			t.Fatal("跨设备回退后回收站里应该有该文件")
		}
		return
	}

	t.Skip("本机没有与临时目录不同设备的可写目录（如 /dev/shm），跳过真实跨设备验证")
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
