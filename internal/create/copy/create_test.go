package copy

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/prompt"
)

// 本文件为 internal/create/copy 的单元测试。Create 的签名与 symlink/hardlink 不同：
// 它不接收 safeop.RemoveOptions，而是接收 force/smart/noTrash 三个布尔量加一个可选的进度 writer，
// 内部的删除选项由实现自行拼装（Confirm 恒为 nil），因此「用户拒绝确认」这条用例无法走注入路径，
// 只能由全局的非交互判定来触发，详见 TestCreateKeepsDestinationWhenConfirmationUnavailable 的注释
//
// 隔离红线：所有用例一律在 t.TempDir() 内构造，绝不对真实家目录、根目录、~/.config/flk 或任何真实用户数据
// 做创建、删除、移动。所有用例的 noTrash 一律传 true：noTrash=false 会把被覆盖的目标移入真实回收站
// （~/.local/share/flk/trash），那属于测试红线之外的副作用，会让反复跑测试残留垃圾

// mustWriteFile 写入测试文件，失败直接终止用例
func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("创建测试文件 %q 失败: %v", path, err)
	}
}

// mustAbs 取绝对路径，失败直接终止用例，断言与实现的路径口径保持一致
func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("解析 %q 的绝对路径失败: %v", path, err)
	}
	return abs
}

// assertSameContent 用 pathutil.FileHash 比对源与目标的内容，失败信息带期望值与实际值
// 之所以不直接比字符串：哈希是项目既有的内容一致性口径（cmd/check 也用它），
// 且对多字节内容、长内容的比对结论更明确
func assertSameContent(t *testing.T, src, dst string) {
	t.Helper()

	wantHash, err := pathutil.FileHash(src)
	if err != nil {
		t.Fatalf("计算源文件 %q 哈希失败: %v", src, err)
	}
	gotHash, err := pathutil.FileHash(dst)
	if err != nil {
		t.Fatalf("计算目标文件 %q 哈希失败: %v", dst, err)
	}
	if gotHash != wantHash {
		t.Fatalf("内容不一致：目标 %q 哈希 = %s，期望与源 %q 相同的 %s", dst, gotHash, src, wantHash)
	}
}

// forceNonInteractiveStdin 把 os.Stdin 指向 os.DevNull，让 prompt.Confirm 稳定走「非终端」分支
//
// 必要性：prompt.Confirm 的判据是 term.IsTerminal(os.Stdin)，而 go test 进程会继承调用方的 stdin；
// 一旦它恰好继承到交互式终端，未注入确认函数的删除流程会真的进入 pterm 交互并永久阻塞
// 这里只替换测试进程自己的文件描述符变量，不触碰任何真实文件，t.Cleanup 会恢复原值
func forceNonInteractiveStdin(t *testing.T) {
	t.Helper()

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("无法打开 %s 构造非终端 stdin: %v", os.DevNull, err)
	}

	original := os.Stdin
	os.Stdin = devNull
	t.Cleanup(func() {
		os.Stdin = original
		_ = devNull.Close()
	})
}

// TestCreateReturnsErrorWhenSourceMissing 固化「源不存在时返回带路径文案的错误」，且不创建目标
// 防的回归：源不存在却先把目标删掉——用户的既有目标凭空消失，副本又没建成
func TestCreateReturnsErrorWhenSourceMissing(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "missing-src.txt")
	dst := filepath.Join(base, "dst.txt")

	var buffer bytes.Buffer
	err := Create(src, dst, false, false, true, &buffer)
	if err == nil {
		t.Fatal("源文件不存在时应返回错误，实际返回 nil")
	}
	// l10n.Init 未被调用时 T 按设计返回源串，模板占位符 {{.Path}} 不会渲染（见 pkg/l10n/l10n.go:82-86），
	// 因此这里接受「已渲染的源路径」或「未渲染的占位符」两种形态：
	// 既守住「错误必须指名道姓地说明哪个源不存在」的意图，又不会把「单测没有初始化 i18n」误判成实现缺陷
	if msg := err.Error(); msg == "" {
		t.Fatal("源不存在时必须返回带有可展示文案的错误，实际为空串")
	} else if !strings.Contains(msg, src) && !strings.Contains(msg, "{{.Path}}") {
		t.Fatalf("错误信息应指出不存在的源路径 %q（或未渲染的 {{.Path}} 占位符），实际 = %q", src, msg)
	}

	if _, statErr := os.Lstat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("源不存在时不应创建目标，Lstat(%q) 错误 = %v，期望 os.ErrNotExist", dst, statErr)
	}
	if buffer.Len() != 0 {
		t.Fatalf("源不存在时不应产生删除计划，实际输出: %q", buffer.String())
	}
}

// TestCreateRejectsDirectorySource 固化「源是目录时不支持复制」的显式错误
// 防的回归：把目录当成普通文件去 os.Open + io.Copy，得到一个含义不明的 I/O 错误
// （历史上目录复制由 pathutil.CopyDir 承担，本函数只负责单个普通文件）
func TestCreateRejectsDirectorySource(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src-dir")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("创建源目录失败: %v", err)
	}
	dst := filepath.Join(base, "dst.txt")

	var buffer bytes.Buffer
	err := Create(src, dst, false, false, true, &buffer)
	if err == nil {
		t.Fatal("源是目录时应返回错误，实际返回 nil")
	}
	if _, statErr := os.Lstat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("源是目录时不应创建目标，Lstat(%q) 错误 = %v，期望 os.ErrNotExist", dst, statErr)
	}
}

// TestCreateCopiesFilePreservingContentAndMetadata 固化最基本的成功路径：内容、权限、修改时间
// 目标刻意放在还不存在的多级子目录下，顺带覆盖 pathutil.EnsureDirExists 的 MkdirAll 分支
// 防的回归：内容拷贝丢失、权限被 umask 改掉、以及修改时间没跟着源走
func TestCreateCopiesFilePreservingContentAndMetadata(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src.txt")
	if err := os.WriteFile(src, []byte("待复制的内容"), 0o600); err != nil {
		t.Fatalf("创建源文件失败: %v", err)
	}

	dst := filepath.Join(base, "nested", "deep", "dst.txt")

	var buffer bytes.Buffer
	if err := Create(src, dst, false, false, true, &buffer); err != nil {
		t.Fatalf("复制普通文件失败: %v", err)
	}

	assertSameContent(t, src, dst)

	srcInfo, err := os.Stat(src)
	if err != nil {
		t.Fatalf("读取源文件信息失败: %v", err)
	}
	dstInfo, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("读取目标文件信息失败: %v", err)
	}

	if dstInfo.Mode().Perm() != srcInfo.Mode().Perm() {
		t.Fatalf("目标权限 = %v，期望与源一致的 %v", dstInfo.Mode().Perm(), srcInfo.Mode().Perm())
	}

	// 时间戳比对截断到秒：多数文件系统能保存纳秒，但不同文件系统的精度并不一致，
	// 截断到秒既能证明 Chtimes 生效，又不会把文件系统精度差异误报成实现缺陷
	if !dstInfo.ModTime().Truncate(time.Second).Equal(srcInfo.ModTime().Truncate(time.Second)) {
		t.Fatalf("目标修改时间 = %v，期望与源一致的 %v", dstInfo.ModTime(), srcInfo.ModTime())
	}

	if buffer.Len() != 0 {
		t.Fatalf("目标不存在时不应产生删除计划，实际输出: %q", buffer.String())
	}
}

// TestCreateReplacesExistingDestinationWithForce 固化 force 分支：旧目标被删除、新副本建立
// 同时断言删除计划写入了调用方注入的进度流——它属于交互过程，绝不能混进最终结果的 stdout
// 防的回归：旧目标没被删掉，os.Create 直接截断旧文件（--force 的「先删后写」语义被偷偷改成原地截断，
// 半途失败时用户拿到的是一份被破坏的旧文件）
func TestCreateReplacesExistingDestinationWithForce(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src.txt")
	mustWriteFile(t, src, "新的内容")

	dst := filepath.Join(base, "dst.txt")
	mustWriteFile(t, dst, "旧的、应当被删除的内容")

	var buffer bytes.Buffer
	if err := Create(src, dst, true, false, true, &buffer); err != nil {
		t.Fatalf("force 覆盖已存在的目标失败: %v", err)
	}

	assertSameContent(t, src, dst)

	content, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("读取覆盖后的目标失败: %v", err)
	}
	if string(content) != "新的内容" {
		t.Fatalf("覆盖后目标内容 = %q，期望 %q", string(content), "新的内容")
	}

	absDst := mustAbs(t, dst)
	if !strings.Contains(buffer.String(), absDst) {
		t.Fatalf("删除计划未写入注入的进度流，实际输出: %q，期望包含 %q", buffer.String(), absDst)
	}
}

// TestCreateKeepsDestinationWhenConfirmationUnavailable 固化「未确认就不许动旧目标」
//
// 关于本用例为何不是 ErrOperationCancelled：Create 的签名没有 Confirm 参数，内部拼装的
// RemoveOptions.Confirm 恒为 nil，因此只会走 prompt.Confirm 的默认实现；在非终端环境下
// prompt 明确返回 ErrNonInteractive（而不是当作用户取消），本用例断言的正是这条既有语义
//
// 防的回归：把「环境不可交互」当成「同意」——脚本/CI 里跑一次 flk create，用户的既有文件被静默覆盖
func TestCreateKeepsDestinationWhenConfirmationUnavailable(t *testing.T) {
	forceNonInteractiveStdin(t)

	base := t.TempDir()
	src := filepath.Join(base, "src.txt")
	mustWriteFile(t, src, "新的内容")

	dst := filepath.Join(base, "dst.txt")
	mustWriteFile(t, dst, "旧的、必须被保留的内容")

	var buffer bytes.Buffer
	err := Create(src, dst, false, false, true, &buffer)
	if !errors.Is(err, prompt.ErrNonInteractive) {
		t.Fatalf("返回错误 = %v，期望 %v", err, prompt.ErrNonInteractive)
	}

	content, readErr := os.ReadFile(dst)
	if readErr != nil {
		t.Fatalf("无法确认时旧目标应原样保留，读取失败: %v", readErr)
	}
	if string(content) != "旧的、必须被保留的内容" {
		t.Fatalf("无法确认时旧目标内容 = %q，期望 %q", string(content), "旧的、必须被保留的内容")
	}

	// 删除计划已经写出（用户有权看到将要发生什么），但旧目标必须还在
	if !strings.Contains(buffer.String(), mustAbs(t, dst)) {
		t.Fatalf("确认前应先把删除计划写入注入的进度流，实际输出: %q", buffer.String())
	}
}

// TestCreateRebuildsWhenDestinationParentIsFile 固化 pathutil.ExistsButNotDirectoryError 分支：
// 目标的父路径是一个普通文件时，先删除该文件、重建中间目录，再复制
// 防的回归：父路径是文件时直接把 ExistsButNotDirectoryError 上抛——用户明明给了 force，
// 却得到一个无法自助解决的错误，还得手动去删那个挡路的文件
func TestCreateRebuildsWhenDestinationParentIsFile(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src.txt")
	mustWriteFile(t, src, "待复制的内容")

	// 挡路的普通文件：它同时是目标的父路径
	blocker := filepath.Join(base, "blocker")
	mustWriteFile(t, blocker, "挡路的普通文件")

	dst := filepath.Join(blocker, "child.txt")

	var buffer bytes.Buffer
	if err := Create(src, dst, true, false, true, &buffer); err != nil {
		t.Fatalf("父路径为普通文件时应删除并重建目录，实际返回错误: %v", err)
	}

	// 断言口径：实现删除挡路文件后会执行 EnsureDirExists 的重试，由 MkdirAll 在同一个路径上重建目录，
	// 因此这里不能断言「路径不存在」，而要断言「原来的普通文件已被目录取代」——这才是可观测的删除证据
	blockerInfo, err := os.Lstat(blocker)
	if err != nil {
		t.Fatalf("挡路文件被删除后应在同一路径重建出目录，Lstat(%q) 错误 = %v，期望 nil", blocker, err)
	}
	if !blockerInfo.IsDir() {
		t.Fatalf("挡路路径 %q 应为重建出的目录，实际模式 = %v", blocker, blockerInfo.Mode())
	}

	parentInfo, err := os.Stat(filepath.Dir(dst))
	if err != nil {
		t.Fatalf("重建后的中间目录应存在: %v", err)
	}
	if !parentInfo.IsDir() {
		t.Fatalf("中间路径 %q 应为目录，实际模式 = %v", filepath.Dir(dst), parentInfo.Mode())
	}

	assertSameContent(t, src, dst)

	// 父路径删除同样属于交互过程，其删除计划也必须落在注入的进度流里
	if !strings.Contains(buffer.String(), mustAbs(t, blocker)) {
		t.Fatalf("父路径的删除计划未写入注入的进度流，实际输出: %q，期望包含 %q", buffer.String(), mustAbs(t, blocker))
	}
}

// TestCreateSmartModeOverwritesWithoutDeletePlan 固化 smart 模式的语义：
// 目标已存在时跳过删除与确认，直接覆盖写入，且不产生任何删除计划
// 内容刻意让旧值比新值长，用来证明是「截断后重写」而不是「原地覆盖前若干字节」——
// 若不截断，旧内容的尾部会残留在目标文件里
// 防的回归：smart 模式误走删除分支，把目标移入回收站（用户以为只是覆盖，结果回收站里多一份残留）；
// 或者写入未截断导致目标内容变成新旧混合
func TestCreateSmartModeOverwritesWithoutDeletePlan(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src.txt")
	mustWriteFile(t, src, "新")

	dst := filepath.Join(base, "dst.txt")
	mustWriteFile(t, dst, "旧的、比新内容长得多的内容")

	var buffer bytes.Buffer
	if err := Create(src, dst, false, true, true, &buffer); err != nil {
		t.Fatalf("smart 模式覆盖已存在的目标失败: %v", err)
	}

	content, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("读取覆盖后的目标失败: %v", err)
	}
	if string(content) != "新" {
		t.Fatalf("smart 模式覆盖后目标内容 = %q，期望 %q（尾部残留旧内容说明写入未截断）", string(content), "新")
	}

	if buffer.Len() != 0 {
		t.Fatalf("smart 模式不应产生删除计划，实际输出: %q", buffer.String())
	}
}

// TestCreateRefusesDirectoryDestination 固化「目标是目录时拒绝覆盖」，并顺带守住家目录这条红线
//
// 关于家目录这个子用例：Create 在进入删除流程之前就会拦截「目标是目录」，
// 因此实现里的受保护路径围栏在本函数的目标侧其实不可达（home 与 / 必然是目录），
// 本用例断言的正是这条更靠前的目录检查——它必须保证对家目录既无删除计划、也无任何写入
// 用例刻意传 smart=true：即使目录检查日后被删掉，smart 分支也不会去删目标，
// 而 os.Create 对一个目录只会返回 EISDIR，绝不会截断家目录里的任何数据
func TestCreateRefusesDirectoryDestination(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src.txt")
	mustWriteFile(t, src, "待复制的内容")

	existingDir := filepath.Join(base, "dst-dir")
	if err := os.MkdirAll(existingDir, 0o755); err != nil {
		t.Fatalf("创建目标目录失败: %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法获取家目录: %v", err)
	}
	home = filepath.Clean(home)

	cases := []struct {
		name string
		dst  string
	}{
		{"临时目录中的已有目录", existingDir},
		{"当前用户家目录本身", home},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buffer bytes.Buffer
			err := Create(src, tc.dst, false, true, true, &buffer)
			if err == nil {
				t.Fatalf("目标是目录 %q 时应返回错误，实际返回 nil", tc.dst)
			}
			if buffer.Len() != 0 {
				t.Fatalf("拒绝目录目标时不应产生删除计划，实际输出: %q", buffer.String())
			}

			info, statErr := os.Lstat(tc.dst)
			if statErr != nil {
				t.Fatalf("目标目录 %q 被删除或移动: %v", tc.dst, statErr)
			}
			if !info.IsDir() {
				t.Fatalf("目标路径 %q 应仍是目录，实际模式 = %v", tc.dst, info.Mode())
			}
		})
	}
}
