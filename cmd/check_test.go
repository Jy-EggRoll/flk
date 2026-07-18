package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCheckCopyValid_SameContent 内容相同的两个文件应判定为有效（不再依赖 mtime）
func TestCheckCopyValid_SameContent(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.txt")
	dst := filepath.Join(tmp, "dst.txt")
	if err := os.WriteFile(src, []byte("hello flk"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("hello flk"), 0644); err != nil {
		t.Fatal(err)
	}
	// 故意写入不同的修改时间（复制后保留原 mtime 不易控制，这里仅验证内容一致即通过）
	valid, _, etype := checkCopyValid(src, dst)
	if !valid {
		t.Fatalf("内容相同的文件应判定有效，实际错误类型: %s", etype)
	}
}

// TestCheckCopyValid_DiffContent 内容不同应判定为 CONTENT_MISMATCH
func TestCheckCopyValid_DiffContent(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.txt")
	dst := filepath.Join(tmp, "dst.txt")
	if err := os.WriteFile(src, []byte("aaa"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("bbb"), 0644); err != nil {
		t.Fatal(err)
	}
	valid, _, etype := checkCopyValid(src, dst)
	if valid {
		t.Fatal("内容不同的文件应判定无效")
	}
	if etype != "CONTENT_MISMATCH" {
		t.Fatalf("错误类型应为 CONTENT_MISMATCH，实际: %s", etype)
	}
}

// TestCheckCopyValid_SizeDiff 大小不同即可快速判定需要同步（无需计算哈希）
func TestCheckCopyValid_SizeDiff(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.txt")
	dst := filepath.Join(tmp, "dst.txt")
	if err := os.WriteFile(src, []byte("longer content here"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("short"), 0644); err != nil {
		t.Fatal(err)
	}
	valid, _, etype := checkCopyValid(src, dst)
	if valid {
		t.Fatal("大小不同的文件应判定无效")
	}
	if etype != "SIZE_MISMATCH" {
		t.Fatalf("错误类型应为 SIZE_MISMATCH，实际: %s", etype)
	}
}

// TestFilesEqualByHash 验证哈希比较正确性
func TestFilesEqualByHash(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "a")
	b := filepath.Join(tmp, "b")
	if err := os.WriteFile(a, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if !filesEqualByHash(a, b) {
		t.Fatal("相同内容应返回 true")
	}
	if err := os.WriteFile(b, []byte("y"), 0644); err != nil {
		t.Fatal(err)
	}
	if filesEqualByHash(a, b) {
		t.Fatal("不同内容应返回 false")
	}
}
