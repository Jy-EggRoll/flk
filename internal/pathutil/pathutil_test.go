package pathutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFile(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "src.txt")
	dst := filepath.Join(tmpDir, "dst.txt")

	if err := os.WriteFile(src, []byte("test content"), 0644); err != nil {
		t.Fatalf("create src file failed: %v", err)
	}

	if err := CopyFile(dst, src); err != nil {
		t.Fatalf("CopyFile failed: %v", err)
	}

	content, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst file failed: %v", err)
	}
	if string(content) != "test content" {
		t.Fatalf("content mismatch: got %s, want %s", string(content), "test content")
	}
}

func TestCopyFile_Permission(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "src.txt")
	dst := filepath.Join(tmpDir, "dst.txt")

	if err := os.WriteFile(src, []byte("test"), 0755); err != nil {
		t.Fatalf("create src file failed: %v", err)
	}

	if err := CopyFile(dst, src); err != nil {
		t.Fatalf("CopyFile failed: %v", err)
	}

	srcInfo, _ := os.Stat(src)
	dstInfo, _ := os.Stat(dst)
	if srcInfo.Mode() != dstInfo.Mode() {
		t.Fatalf("permission mismatch: got %v, want %v", dstInfo.Mode(), srcInfo.Mode())
	}
}

func TestCopyFile_SourceNotExist(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "notexist.txt")
	dst := filepath.Join(tmpDir, "dst.txt")

	if err := CopyFile(dst, src); err == nil {
		t.Fatal("CopyFile should fail for non-existent source")
	}
}

func TestCopyDir(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "src")
	dst := filepath.Join(tmpDir, "dst")

	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatalf("create src dir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "file1.txt"), []byte("content1"), 0644); err != nil {
		t.Fatalf("create file1 failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "file2.txt"), []byte("content2"), 0644); err != nil {
		t.Fatalf("create file2 failed: %v", err)
	}

	subdir := filepath.Join(src, "subdir")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("create subdir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "file3.txt"), []byte("content3"), 0644); err != nil {
		t.Fatalf("create file3 failed: %v", err)
	}

	if err := CopyDir(src, dst); err != nil {
		t.Fatalf("CopyDir failed: %v", err)
	}

	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("dst dir not exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "file1.txt")); err != nil {
		t.Fatalf("file1 not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "file2.txt")); err != nil {
		t.Fatalf("file2 not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "subdir", "file3.txt")); err != nil {
		t.Fatalf("file3 not copied: %v", err)
	}
}

func TestCopyDir_Symlink(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "src")
	dst := filepath.Join(tmpDir, "dst")

	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatalf("create src dir failed: %v", err)
	}

	targetFile := filepath.Join(tmpDir, "target.txt")
	if err := os.WriteFile(targetFile, []byte("target"), 0644); err != nil {
		t.Fatalf("create target failed: %v", err)
	}

	if err := os.Symlink(targetFile, filepath.Join(src, "link")); err != nil {
		t.Fatalf("create symlink failed: %v", err)
	}

	if err := CopyDir(src, dst); err != nil {
		t.Fatalf("CopyDir failed: %v", err)
	}

	link, err := os.Readlink(filepath.Join(dst, "link"))
	if err != nil {
		t.Fatalf("readlink failed: %v", err)
	}
	if link != targetFile {
		t.Fatalf("symlink target mismatch: got %s, want %s", link, targetFile)
	}
}

func TestCopy_SymlinkToDir(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "src")
	dst := filepath.Join(tmpDir, "dst")

	targetDir := filepath.Join(tmpDir, "targetdir")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatalf("create target dir failed: %v", err)
	}

	if err := os.Symlink(targetDir, src); err != nil {
		t.Fatalf("create symlink failed: %v", err)
	}

	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy failed: %v", err)
	}

	link, err := os.Readlink(dst)
	if err != nil {
		t.Fatalf("readlink failed: %v", err)
	}
	if link != targetDir {
		t.Fatalf("symlink target mismatch: got %s, want %s", link, targetDir)
	}
}

func TestCopy_SymlinkToFile(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "src")
	dst := filepath.Join(tmpDir, "dst")

	target := filepath.Join(tmpDir, "target")
	if err := os.WriteFile(target, []byte("target"), 0644); err != nil {
		t.Fatalf("create target failed: %v", err)
	}

	if err := os.Symlink(target, src); err != nil {
		t.Fatalf("create symlink failed: %v", err)
	}

	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy failed: %v", err)
	}

	link, err := os.Readlink(dst)
	if err != nil {
		t.Fatalf("readlink failed: %v", err)
	}
	if link != target {
		t.Fatalf("symlink target mismatch: got %s, want %s", link, target)
	}
}

func TestCopy_File(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "src.txt")
	dst := filepath.Join(tmpDir, "dst.txt")

	if err := os.WriteFile(src, []byte("test"), 0644); err != nil {
		t.Fatalf("create src file failed: %v", err)
	}

	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy failed: %v", err)
	}

	content, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst file failed: %v", err)
	}
	if string(content) != "test" {
		t.Fatalf("content mismatch")
	}
}

func TestCopy_Dir(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "src")
	dst := filepath.Join(tmpDir, "dst")

	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatalf("create src dir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatalf("create file failed: %v", err)
	}

	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dst, "a.txt")); err != nil {
		t.Fatalf("file not copied: %v", err)
	}
}

func TestCopy_NotExist(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "notexist")
	dst := filepath.Join(tmpDir, "dst")

	if err := Copy(src, dst); err == nil {
		t.Fatal("Copy should fail for non-existent source")
	}
}

func TestCopyFile_TargetExist(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "src.txt")
	dst := filepath.Join(tmpDir, "dst.txt")

	if err := os.WriteFile(src, []byte("src"), 0644); err != nil {
		t.Fatalf("create src failed: %v", err)
	}
	if err := os.WriteFile(dst, []byte("dst"), 0644); err != nil {
		t.Fatalf("create dst failed: %v", err)
	}

	if err := CopyFile(dst, src); err != nil {
		t.Fatalf("CopyFile should overwrite existing: %v", err)
	}

	content, _ := os.ReadFile(dst)
	if string(content) != "src" {
		t.Fatalf("content not overwritten: %s", string(content))
	}
}

func TestCopyDir_TargetExist(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "src")
	dst := filepath.Join(tmpDir, "dst")

	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatalf("create src failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatalf("create file failed: %v", err)
	}

	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatalf("create dst failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dst, "b.txt"), []byte("b"), 0644); err != nil {
		t.Fatalf("create dst file failed: %v", err)
	}

	if err := CopyDir(src, dst); err != nil {
		t.Fatalf("CopyDir should overwrite: %v", err)
	}

	content, _ := os.ReadFile(filepath.Join(dst, "a.txt"))
	if string(content) != "a" {
		t.Fatalf("file not copied: %s", string(content))
	}
}

func TestCopyDir_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "src")
	dst := filepath.Join(tmpDir, "dst")

	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatalf("create src failed: %v", err)
	}

	if err := CopyDir(src, dst); err != nil {
		t.Fatalf("CopyDir empty dir failed: %v", err)
	}

	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("dst not exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("dst is not dir")
	}
}

func TestCopyDir_SymlinkToParent(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "src")
	link := filepath.Join(src, "link")

	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatalf("create src failed: %v", err)
	}

	if err := os.Symlink(src, link); err != nil {
		t.Fatalf("create symlink failed: %v", err)
	}

	dst := filepath.Join(tmpDir, "dst")
	if err := CopyDir(src, dst); err != nil {
		t.Fatalf("CopyDir failed: %v", err)
	}

	linkDst := filepath.Join(dst, "link")
	target, err := os.Readlink(linkDst)
	if err != nil {
		t.Fatalf("readlink failed: %v", err)
	}
	if target != src {
		t.Fatalf("symlink target mismatch: got %s, want %s", target, src)
	}
}

func TestCopyDir_SymlinkInside_SymlinkLoop(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "src")
	subdir := filepath.Join(src, "subdir")
	link := filepath.Join(subdir, "back")

	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("create dirs failed: %v", err)
	}

	if err := os.Symlink(src, link); err != nil {
		t.Fatalf("create loop symlink failed: %v", err)
	}

	dst := filepath.Join(tmpDir, "dst")
	if err := CopyDir(src, dst); err != nil {
		t.Fatalf("CopyDir with loop symlink failed: %v", err)
	}

	linkDst := filepath.Join(dst, "subdir", "back")
	target, err := os.Readlink(linkDst)
	if err != nil {
		t.Fatalf("readlink failed: %v", err)
	}
	if target != src {
		t.Fatalf("symlink target mismatch: got %s, want %s", target, src)
	}
}

func TestCopyFile_Unicode(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "中文文件.txt")
	dst := filepath.Join(tmpDir, "复制文件.txt")

	if err := os.WriteFile(src, []byte("内容"), 0644); err != nil {
		t.Fatalf("create src failed: %v", err)
	}

	if err := CopyFile(dst, src); err != nil {
		t.Fatalf("CopyFile unicode failed: %v", err)
	}

	content, _ := os.ReadFile(dst)
	if string(content) != "内容" {
		t.Fatalf("content mismatch: %s", string(content))
	}
}

func TestCopyDir_NestedSymlink(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "src")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatalf("create src failed: %v", err)
	}

	external := filepath.Join(tmpDir, "external")
	if err := os.MkdirAll(external, 0755); err != nil {
		t.Fatalf("create external failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(external, "file.txt"), []byte("external"), 0644); err != nil {
		t.Fatalf("create external file failed: %v", err)
	}

	if err := os.Symlink(external, filepath.Join(src, "extlink")); err != nil {
		t.Fatalf("create symlink failed: %v", err)
	}

	dst := filepath.Join(tmpDir, "dst")
	if err := CopyDir(src, dst); err != nil {
		t.Fatalf("CopyDir nested symlink failed: %v", err)
	}

	linkDst := filepath.Join(dst, "extlink")
	target, err := os.Readlink(linkDst)
	if err != nil {
		t.Fatalf("readlink failed: %v", err)
	}
	if target != external {
		t.Fatalf("symlink target mismatch: got %s, want %s", target, external)
	}
}

func TestCopyDir_SubdirPermission(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "src")
	subdir := filepath.Join(src, "subdir")

	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("create dirs failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatalf("create file failed: %v", err)
	}

	dst := filepath.Join(tmpDir, "dst")
	if err := CopyDir(src, dst); err != nil {
		t.Fatalf("CopyDir permission failed: %v", err)
	}

	srcInfo, _ := os.Stat(subdir)
	dstInfo, _ := os.Stat(filepath.Join(dst, "subdir"))
	if srcInfo.Mode() != dstInfo.Mode() {
		t.Fatalf("dir permission mismatch: got %v, want %v", dstInfo.Mode(), srcInfo.Mode())
	}
}
