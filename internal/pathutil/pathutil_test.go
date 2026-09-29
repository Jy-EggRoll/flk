package pathutil

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestFoldHome_Boundary 验证 FoldHome 按路径段边界折叠，
// 修复前 /rootother 这类与家目录同名前缀的路径会被误折叠成 ~other
func TestFoldHome_Boundary(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法获取家目录: %v", err)
	}

	sep := string(os.PathSeparator)

	// 家目录本身应折叠为 ~
	if got, _ := FoldHome(home); got != "~" {
		t.Fatalf("家目录折叠错误: got %s, want ~", got)
	}

	// 家目录真正的子项应折叠为 ~/子路径
	child := home + sep + ".config"
	if got, _ := FoldHome(child); got != "~"+sep+".config" {
		t.Fatalf("子项折叠错误: got %s, want %s", got, "~"+sep+".config")
	}

	// 与家目录同名前缀但不是子项的路径，绝不能被折叠
	sibling := home + "other" + sep + "config"
	got, _ := FoldHome(sibling)
	if strings.HasPrefix(got, "~") {
		t.Fatalf("同名前缀路径被错误折叠: %s => %s", sibling, got)
	}
}

// TestIsProtectedPath 固化受保护路径的判断口径：只有根目录本身与用户家目录本身命中，
// 家目录的子项、家目录的父目录以及普通临时目录一律放行
// 全部用例只调用纯判断函数，不涉及任何文件系统写入、删除或复制
func TestIsProtectedPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法获取家目录: %v", err)
	}
	home = filepath.Clean(home)
	sep := string(os.PathSeparator)

	// 根目录用 VolumeName 现场推导而不是硬编码：Unix 得到 "/"，Windows 得到 "C:\" 这类盘符根，
	// 家目录必然与根同盘，因此这样推导出的就是当前平台的根目录本身
	root := filepath.VolumeName(home) + sep

	// home+"/.." 归一化后是家目录的父目录（如 /home）而不是家目录本身，按「只保护根与家目录本身」的
	// 约定应当放行；仅当家目录本身挂在根目录下（如 root 用户的 /root）时它才恰好等于根目录而被根规则
	// 拦下，所以期望值必须按实际归一化结果推导，不能写死，否则用例在 root 环境下会误报
	homeParent := filepath.Clean(filepath.Join(home, ".."))
	homeParentProtected := homeParent == home || filepath.Dir(homeParent) == homeParent

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"根目录本身", root, true},
		{"根目录的 ./ 归一化形式", filepath.Join(root, "."), true},
		{"根目录的 .. 归一化形式", filepath.Join(root, ".."), true},
		{"家目录本身", home, true},
		{"家目录带尾部斜杠", home + sep, true},
		{"家目录的 ./ 归一化形式", filepath.Join(home, "."), true},
		{"家目录中夹 .. 的归一化等价形式", filepath.Join(home, "..", filepath.Base(home)), true},
		{"家目录的父目录遍历", filepath.Join(home, ".."), homeParentProtected},
		{"家目录的子目录", filepath.Join(home, ".ssh"), false},
		{"家目录的子文件", filepath.Join(home, ".bashrc"), false},
		{"普通临时目录", t.TempDir(), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsProtectedPath(tc.path); got != tc.want {
				t.Fatalf("IsProtectedPath(%q) = %v，期望 %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestIsProtectedPathDoesNotResolveSymlinks 固化「不解析符号链接」的口径：
// 一个位于临时目录、指向家目录的链接必须被放行——os.RemoveAll 只删除链接自身而不跟随到目标，
// 展开链接反而会把这种无害场景误判成危险目标
// 本用例只创建链接，绝不对家目录做任何写入、删除或复制
func TestIsProtectedPathDoesNotResolveSymlinks(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法获取家目录: %v", err)
	}

	link := filepath.Join(t.TempDir(), "link-to-home")
	if err := os.Symlink(home, link); err != nil {
		t.Skipf("当前环境无法创建符号链接: %v", err)
	}

	if IsProtectedPath(link) {
		t.Fatalf("指向家目录的链接被误判为受保护路径: %s", link)
	}
}

// TestIsProtectedPathWindowsCaseInsensitive 只在 Windows 上固化大小写不敏感口径，
// 保证 C:\Users\Foo 与 c:\users\foo 这类大小写变体同样被拦下；Unix 文件系统区分大小写，
// 该场景不适用于本用例
func TestIsProtectedPathWindowsCaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅 Windows 的路径比较忽略大小写")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法获取家目录: %v", err)
	}
	upper := strings.ToUpper(home)
	if upper == home {
		t.Skip("家目录不含可大写化的字符，无法构造大小写变体")
	}

	if !IsProtectedPath(upper) {
		t.Fatalf("Windows 下家目录的大小写变体应判定为受保护: %q", upper)
	}
}

// TestIsSubPath 固化「严格后代」的判断口径，全部用例只调用纯判断函数，
// 不做任何文件系统写入、删除或复制（t.TempDir 仅提供一个隔离的绝对路径字符串）
//
// 重点覆盖三类容易写错的情形：
//   - 前缀相似的兄弟目录（/a/b 与 /a/bc）必须返回 false——用字符串前缀比较实现本函数时会在这里翻车，
//     这是本用例集最关键的一条
//   - parent 与 child 相同时必须返回 false（严格后代不含自身），否则调用方无法把它与「包含」区分开
//   - 相对结果以 ".." 开头的合法目录名（如 "..foo"）不能被当成上级目录——用 HasPrefix(rel, "..")
//     实现同样会在这里翻车
func TestIsSubPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法获取家目录: %v", err)
	}
	home = filepath.Clean(home)
	sep := string(os.PathSeparator)

	// 根目录现场推导而不是硬编码：Unix 得到 "/"，Windows 得到 "C:\" 这类盘符根
	// 本用例只把根目录当作纯字符串做判断，绝不触碰它
	root := filepath.VolumeName(home) + sep

	base := t.TempDir()
	// parent 与 sibling 刻意构造成「一个是另一个的前缀」：/a/b 与 /a/bc 的字符串前缀关系成立，
	// 但路径语义上互不包含，这正是前缀比较实现会误判的关键反例
	parent := filepath.Join(base, "a", "b")
	sibling := filepath.Join(base, "a", "bc")
	// "..foo" 是合法目录名：filepath.Rel 相对 parent 的结果就是一个以 ".." 开头却不是上级的字符串
	dotDotNamed := filepath.Join(parent, "..foo")

	cases := []struct {
		name   string
		parent string
		child  string
		want   bool
	}{
		{"真后代一层", parent, filepath.Join(parent, "child"), true},
		{"真后代多层", parent, filepath.Join(parent, "child", "grand", "leaf"), true},
		{"真后代带 . 等价形式", parent, filepath.Join(parent, ".", "child"), true},
		{"真后代带 .. 归一化等价形式", parent, filepath.Join(parent, "child", "..", "child", "leaf"), true},
		{"真后代名为 ..foo 的目录", parent, dotDotNamed, true},
		{"parent 与 child 相同", parent, parent, false},
		{"parent 与 child 相同（尾部分隔符）", parent, parent + sep, false},
		{"parent 与 child 相同（./ 与 .. 归一化）", parent, filepath.Join(parent, "child", ".."), false},
		{"兄弟目录", parent, filepath.Join(base, "a", "c"), false},
		{"前缀相似的兄弟目录", parent, sibling, false},
		{"前缀相似的兄弟目录（反向）", sibling, parent, false},
		{"父级", parent, filepath.Join(base, "a"), false},
		{"祖先", parent, base, false},
		{"根目录之下的临时目录", root, base, true},
		{"根目录与自身", root, root, false},
		{"非绝对路径的真后代", "flk-rel-parent", filepath.Join("flk-rel-parent", "child"), true},
		{"非绝对路径的相同路径", "flk-rel-parent", "flk-rel-parent", false},
		{"非绝对路径前缀相似的兄弟目录", "flk-rel-parent", "flk-rel-parent-x", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSubPath(tc.parent, tc.child); got != tc.want {
				t.Fatalf("IsSubPath(%q, %q) = %v，期望 %v", tc.parent, tc.child, got, tc.want)
			}
		})
	}
}

// TestSamePath 固化同一路径的判断口径：归一化写法（尾部斜杠、./、..绕行）必须判为相同，
// 前缀相似的无关路径必须判为不同
func TestSamePath(t *testing.T) {
	sep := string(os.PathSeparator)
	base := t.TempDir()
	dir := filepath.Join(base, "same")

	cases := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{"字面相同", dir, dir, true},
		{"尾部斜杠", dir, dir + sep, true},
		{"./ 形式", dir, filepath.Join(dir, "."), true},
		{".. 绕行形式", dir, filepath.Join(dir, "sub", ".."), true},
		{"前缀相似的兄弟路径", dir, dir + "-x", false},
		{"父子路径", dir, filepath.Join(dir, "sub"), false},
		{"非绝对路径的自身", "flk-rel-same", "flk-rel-same", true},
		{"非绝对路径的等价形式", "flk-rel-same", filepath.Join("flk-rel-same", ".", "./"), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SamePath(tc.a, tc.b); got != tc.want {
				t.Fatalf("SamePath(%q, %q) = %v，期望 %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestIsSubPathWindowsCaseInsensitive 只在 Windows 上固化大小写不敏感口径：
// C:\Foo\Bar 与 c:\foo\Bar 是同一棵子树，大小写变体必须同样被判定为子路径；
// Unix 文件系统区分大小写，该场景不适用于本用例
// 本用例只对临时目录的路径字符串做判断，不触碰任何真实目录
func TestIsSubPathWindowsCaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅 Windows 的路径比较忽略大小写")
	}

	base := t.TempDir()
	parent := filepath.Join(base, "CaseParent")
	child := filepath.Join(parent, "casechild")
	upperParent := strings.ToUpper(parent)
	if upperParent == parent {
		t.Skip("临时目录路径不含可大写化的字符，无法构造大小写变体")
	}

	if !IsSubPath(upperParent, strings.ToUpper(child)) {
		t.Fatalf("Windows 下大小写变体的子路径应判定为子路径: %q", upperParent)
	}
	if !SamePath(strings.ToUpper(parent), parent) {
		t.Fatalf("Windows 下大小写变体应判定为同一路径: %q", upperParent)
	}
}

// TestFileHash 验证内容相同哈希相同、内容不同哈希不同
func TestFileHash(t *testing.T) {
	tmpDir := t.TempDir()

	a := filepath.Join(tmpDir, "a.txt")
	b := filepath.Join(tmpDir, "b.txt")
	c := filepath.Join(tmpDir, "c.txt")

	if err := os.WriteFile(a, []byte("same content"), 0644); err != nil {
		t.Fatalf("写 a 失败: %v", err)
	}
	if err := os.WriteFile(b, []byte("same content"), 0644); err != nil {
		t.Fatalf("写 b 失败: %v", err)
	}
	if err := os.WriteFile(c, []byte("different"), 0644); err != nil {
		t.Fatalf("写 c 失败: %v", err)
	}

	ha, err := FileHash(a)
	if err != nil {
		t.Fatalf("FileHash a 失败: %v", err)
	}
	hb, err := FileHash(b)
	if err != nil {
		t.Fatalf("FileHash b 失败: %v", err)
	}
	hc, err := FileHash(c)
	if err != nil {
		t.Fatalf("FileHash c 失败: %v", err)
	}

	if ha != hb {
		t.Fatalf("内容相同哈希应相同: %s vs %s", ha, hb)
	}
	if ha == hc {
		t.Fatalf("内容不同哈希应不同")
	}
}

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

// TestCheckCopyPaths 固化 CheckCopyPaths 的判定口径：只有「源与目标相同」与「目标位于源内部」
// 两种形态命中，前缀相似的兄弟目录、目标为源的父目录、彼此无关的路径都必须放行
//
// 全部路径都在 t.TempDir() 内构造；并且刻意包含「路径根本不存在」的用例，
// 证明本函数是纯字符串判断，不依赖任何文件系统状态（这正是调用方能在动手之前安全调用的前提）
func TestCheckCopyPaths(t *testing.T) {
	base := t.TempDir()
	sep := string(os.PathSeparator)

	dir := filepath.Join(base, "repo")
	// 与 dir 前缀相似但不是其后代的兄弟目录：字符串前缀比较会把 /a/bc 误判为在 /a/b 内部，
	// 这里就是专门用来钉死那个错误口径的
	sibling := filepath.Join(base, "repo2")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.MkdirAll(sibling, 0755); err != nil {
		t.Fatalf("创建兄弟目录失败: %v", err)
	}

	cases := []struct {
		name    string
		src     string
		dst     string
		wantHit bool
	}{
		{"源与目标相同", dir, dir, true},
		{"源与目标相同（尾部分隔符）", dir + sep, dir, true},
		{"源与目标相同（./ 形式）", filepath.Join(dir, "."), dir, true},
		{"源与目标相同（.. 绕行）", dir + sep + "sub" + sep + "..", dir, true},
		{"目标为源的直接子目录", dir, filepath.Join(dir, "sub"), true},
		{"目标为源的深层子目录", dir, filepath.Join(dir, "a", "b", "c"), true},
		{"目标为源内部（.. 绕行到别处再回来）", dir, dir + sep + "sub" + sep + ".." + sep + "sub2", true},
		// 两个路径都不存在也必须命中：判定不依赖 Stat，调用方才能在删改之前用
		{"目标为源内部（两者均不存在）", filepath.Join(base, "ghost"), filepath.Join(base, "ghost", "inner"), true},
		{"前缀相似的兄弟目录必须放行", dir, sibling, false},
		{"目标为源的父目录必须放行", dir, base, false},
		{"彼此无关的路径必须放行", dir, filepath.Join(base, "other"), false},
		{"目标为源的兄弟目录（反向前缀相似）", sibling, dir, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckCopyPaths(tc.src, tc.dst)
			if !tc.wantHit {
				if err != nil {
					t.Fatalf("CheckCopyPaths(%q, %q) = %v，期望放行", tc.src, tc.dst, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("CheckCopyPaths(%q, %q) = nil，期望被拒绝", tc.src, tc.dst)
			}
			if !errors.Is(err, ErrCopyPathOverlap) {
				t.Fatalf("错误 = %v，期望可被 errors.Is 判定为 %v", err, ErrCopyPathOverlap)
			}
			// 文案必须自带两个路径与方向：命令层会把它原样呈现给用户，只写「路径非法」用户无法改正
			if err.Error() == "" {
				t.Fatal("拒绝错误必须带有可展示的文案")
			}
		})
	}
}

// TestCopyRejectsSelfReference 验证 Copy 在触碰文件系统之前就拒绝自引用复制，
// 且拒绝过程没有任何副作用——这是本组缺陷修复的核心承诺
//
// 修复前两种损害都是不可逆的：
//   - 目录：CopyDir 先 MkdirAll(dst) 建出目标，紧接着 ReadDir(src) 读到自己刚建的目标并递归下钻，
//     每层多一级同名子目录，直到路径超长报错，期间在用户的源目录里留下大量垃圾目录
//   - 相同路径的文件：CopyFile 用 os.Create(dst) 先把文件截断，再从已被截断的同一文件读取，
//     结果是文件被清零
//
// 因此断言分两步：错误可被 errors.Is 识别，且源目录/源文件的内容分毫未动（用探针核对）
func TestCopyRejectsSelfReference(t *testing.T) {
	t.Run("目标位于源内部时不产生任何垃圾目录", func(t *testing.T) {
		base := t.TempDir()
		src := filepath.Join(base, "repo")
		if err := os.MkdirAll(src, 0755); err != nil {
			t.Fatalf("创建源目录失败: %v", err)
		}
		if err := os.WriteFile(filepath.Join(src, "probe.txt"), []byte("keep-me"), 0644); err != nil {
			t.Fatalf("创建探针失败: %v", err)
		}

		dst := filepath.Join(src, "self")
		err := Copy(src, dst)
		if err == nil {
			t.Fatal("目标位于源内部时必须拒绝")
		}
		if !errors.Is(err, ErrCopyPathOverlap) {
			t.Fatalf("错误 = %v，期望 %v", err, ErrCopyPathOverlap)
		}

		// 目标目录绝不能被建出来（修复前它会被 MkdirAll 创建，随后被读回源里形成递归）
		if _, statErr := os.Lstat(dst); statErr == nil {
			t.Fatalf("拒绝时不应创建目标目录: %s", dst)
		}
		// 源目录必须仍是原来的样子：只有探针一个条目，没有被复制进来的垃圾
		entries, readErr := os.ReadDir(src)
		if readErr != nil {
			t.Fatalf("读取源目录失败: %v", readErr)
		}
		if len(entries) != 1 || entries[0].Name() != "probe.txt" {
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			t.Fatalf("源目录被污染，条目 = %v，期望只有 probe.txt", names)
		}
		if content, err := os.ReadFile(filepath.Join(src, "probe.txt")); err != nil || string(content) != "keep-me" {
			t.Fatalf("源探针被改写: content = %q, err = %v", string(content), err)
		}
	})

	t.Run("源与目标相同（目录）时拒绝", func(t *testing.T) {
		base := t.TempDir()
		src := filepath.Join(base, "same")
		if err := os.MkdirAll(src, 0755); err != nil {
			t.Fatalf("创建目录失败: %v", err)
		}
		if err := os.WriteFile(filepath.Join(src, "probe.txt"), []byte("keep-me"), 0644); err != nil {
			t.Fatalf("创建探针失败: %v", err)
		}

		if err := Copy(src, src); !errors.Is(err, ErrCopyPathOverlap) {
			t.Fatalf("错误 = %v，期望 %v", err, ErrCopyPathOverlap)
		}
		if content, err := os.ReadFile(filepath.Join(src, "probe.txt")); err != nil || string(content) != "keep-me" {
			t.Fatalf("源探针被改写: content = %q, err = %v", string(content), err)
		}
	})

	t.Run("源与目标相同（文件）时内容不被清零", func(t *testing.T) {
		base := t.TempDir()
		file := filepath.Join(base, "same.txt")
		if err := os.WriteFile(file, []byte("precious"), 0644); err != nil {
			t.Fatalf("创建文件失败: %v", err)
		}

		err := Copy(file, file)
		if !errors.Is(err, ErrCopyPathOverlap) {
			t.Fatalf("错误 = %v，期望 %v", err, ErrCopyPathOverlap)
		}
		// 这条断言直接对应修复前的真实损害：CopyFile 会先截断再自读，文件变成空
		content, readErr := os.ReadFile(file)
		if readErr != nil {
			t.Fatalf("读取文件失败: %v", readErr)
		}
		if string(content) != "precious" {
			t.Fatalf("文件被清零或改写: content = %q，期望 %q", string(content), "precious")
		}
	})

	t.Run("深层子目录同样拒绝", func(t *testing.T) {
		base := t.TempDir()
		src := filepath.Join(base, "repo")
		if err := os.MkdirAll(src, 0755); err != nil {
			t.Fatalf("创建源目录失败: %v", err)
		}

		dst := filepath.Join(src, "a", "b", "c")
		if err := Copy(src, dst); !errors.Is(err, ErrCopyPathOverlap) {
			t.Fatalf("错误 = %v，期望 %v", err, ErrCopyPathOverlap)
		}
		// 连中间层目录也不能被创建：守卫必须在任何文件系统操作之前
		if _, statErr := os.Lstat(filepath.Join(src, "a")); statErr == nil {
			t.Fatal("拒绝时不应创建目标的任何中间目录")
		}
	})
}

// TestCopyAllowsLegitimateDirections 验证守卫不误伤合法用法：
// 前缀相似的兄弟目录（修复前最容易被字符串前缀比较误伤）、把源复制进它的父目录，都必须照常工作
func TestCopyAllowsLegitimateDirections(t *testing.T) {
	t.Run("前缀相似的兄弟目录", func(t *testing.T) {
		base := t.TempDir()
		src := filepath.Join(base, "b")
		dst := filepath.Join(base, "bc")
		if err := os.MkdirAll(src, 0755); err != nil {
			t.Fatalf("创建源目录失败: %v", err)
		}
		if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("A"), 0644); err != nil {
			t.Fatalf("创建文件失败: %v", err)
		}

		if err := Copy(src, dst); err != nil {
			t.Fatalf("前缀相似的兄弟目录必须放行，却返回错误: %v", err)
		}
		if content, err := os.ReadFile(filepath.Join(dst, "a.txt")); err != nil || string(content) != "A" {
			t.Fatalf("复制结果不正确: content = %q, err = %v", string(content), err)
		}
	})

	t.Run("把源复制进它的父目录", func(t *testing.T) {
		base := t.TempDir()
		parent := filepath.Join(base, "parent")
		src := filepath.Join(parent, "child")
		if err := os.MkdirAll(src, 0755); err != nil {
			t.Fatalf("创建源目录失败: %v", err)
		}
		if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("A"), 0644); err != nil {
			t.Fatalf("创建文件失败: %v", err)
		}

		// 目标恰为源的父目录：方向正常（把数据并入上层），必须放行
		if err := Copy(src, parent); err != nil {
			t.Fatalf("目标为源的父目录必须放行，却返回错误: %v", err)
		}
		if content, err := os.ReadFile(filepath.Join(parent, "a.txt")); err != nil || string(content) != "A" {
			t.Fatalf("复制结果不正确: content = %q, err = %v", string(content), err)
		}
	})
}
