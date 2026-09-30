package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// 本文件是 internal/store 的首个测试文件，负责钉住「链接清单持久化」这一核心数据层的契约
// 所有用例都遵守两条隔离红线：
//  1. 落盘一律走 t.TempDir，绝不读写真实 ~/.config/flk/flk-store.json 或家目录下的任何文件
//  2. 需要触碰全局实例的用例（InitStore）必须先用 preserveGlobal 记录原值并 t.Cleanup 还原
//
// 读取约定：清单字段不再导出，测试一律经 Snapshot() 取深拷贝来断言，
// 这样测试读到的形态与生产代码（check / serve）完全一致，顺带把深拷贝本身也覆盖了
//
// 路径折叠用例只做纯字符串运算（pathutil.NormalizePath / pathutil.FoldHome 不访问文件系统），
// 因此「调用家目录下的绝对路径」不会产生任何家目录读写，属于安全断言

// storeTestPlatform 返回写记录时使用的平台键
// store.go 的 AddRecord 内部固定用 runtime.GOOS 作为平台键，测试若硬编码 "linux" 会在其它平台整体失败
func storeTestPlatform() string {
	return runtime.GOOS
}

// newTestManager 构造一个空 Manager，等价于 InitStore 在存储文件不存在时的产物
func newTestManager() *Manager {
	return New()
}

// writeStoreFile 在临时目录写入一份存储文件并返回其路径
func writeStoreFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flk-store.json")
	writeStoreFileAt(t, path, content)
	return path
}

// writeStoreFileAt 在指定路径写入存储文件内容，父目录必须已存在
func writeStoreFileAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入存储文件 %s 失败: %v", path, err)
	}
}

// foldedJoin 拼出与 store.go 折叠口径一致的「~ + 路径」字面量
// 刻意用 os.PathSeparator 而不是写死 "/"，让期望值在 Windows 上同样成立
func foldedJoin(parts ...string) string {
	return "~" + string(os.PathSeparator) + filepath.Join(parts...)
}

// entriesOf 取出当前平台下指定设备/类型的记录切片
// 经 Snapshot 深拷贝读取：与生产调用方（cmd/check.go 的 performCheck）走同一条出口
func entriesOf(m *Manager, device, linkType string) []Entry {
	return m.Snapshot()[storeTestPlatform()][device][linkType]
}

// foldRealPath 借 AddRecord 的 real 字段观察路径折叠结果
// 只做纯字符串运算，不读写家目录下的任何文件
func foldRealPath(t *testing.T, raw string) string {
	t.Helper()
	m := newTestManager()
	m.AddRecord("device-fold", "symlink", map[string]string{"real": raw, "fake": foldedJoin("flk", "fake")})
	entries := entriesOf(m, "device-fold", "symlink")
	if len(entries) != 1 {
		t.Fatalf("折叠探针应写入 1 条记录，实际 %d 条: %#v", len(entries), entries)
	}
	return entries[0]["real"]
}

// sameEntries 比较两组记录，nil 与空切片视为等价（reflect.DeepEqual 会区分二者，这里必须抹平）
func sameEntries(a, b []Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

// findEntry 在记录切片中按字段值查找第一条匹配记录，找不到返回 nil
func findEntry(entries []Entry, key, value string) Entry {
	for _, e := range entries {
		if e[key] == value {
			return e
		}
	}
	return nil
}

// preserveGlobal 记录包级全局实例的原值并在用例结束时还原
// InitStore 会替换全局实例，若不还原会污染同包其它用例（Go 默认串行执行同一包的测试）；
// 赋值与读取都走带锁的访问器，避免测试自己成为裸变量竞态的制造者
func preserveGlobal(t *testing.T) {
	t.Helper()
	original := Global()
	t.Cleanup(func() {
		SetGlobal(original)
	})
}

// TestAddRecordWritesAtCorrectHierarchy 验证 AddRecord 落在 platform → device → linkType 三层正确位置
// 防的回归：层级错位（例如把设备当平台、把不同类型混进同一分组），会让 check/fix/unlink 按设备过滤时读错数据
func TestAddRecordWritesAtCorrectHierarchy(t *testing.T) {
	const (
		deviceA = "device-a"
		deviceB = "device-b"
	)

	m := newTestManager()
	m.AddRecord(deviceA, "symlink", map[string]string{
		"real": foldedJoin("flk", "real-a"),
		"fake": foldedJoin("flk", "fake-a"),
	})
	m.AddRecord(deviceA, "hardlink", map[string]string{
		"prim": foldedJoin("flk", "prim-a"),
		"seco": foldedJoin("flk", "seco-a"),
	})
	m.AddRecord(deviceB, "symlink", map[string]string{
		"real": foldedJoin("flk", "real-b"),
		"fake": foldedJoin("flk", "fake-b"),
	})

	platform := storeTestPlatform()
	snapshot := m.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("平台键数量 = %d，期望 1（只应写入 runtime.GOOS = %q）: %#v", len(snapshot), platform, snapshot)
	}
	deviceGroup, ok := snapshot[platform]
	if !ok {
		t.Fatalf("未在平台 %q 下写入数据: %#v", platform, snapshot)
	}
	if len(deviceGroup) != 2 {
		t.Fatalf("设备分组数量 = %d，期望 2: %#v", len(deviceGroup), deviceGroup)
	}

	symlinkA := entriesOf(m, deviceA, "symlink")
	if len(symlinkA) != 1 {
		t.Fatalf("%s/symlink 记录数 = %d，期望 1: %#v", deviceA, len(symlinkA), symlinkA)
	}
	if got := symlinkA[0]["real"]; got != foldedJoin("flk", "real-a") {
		t.Fatalf("real = %q，期望 %q", got, foldedJoin("flk", "real-a"))
	}
	if got := symlinkA[0]["fake"]; got != foldedJoin("flk", "fake-a") {
		t.Fatalf("fake = %q，期望 %q", got, foldedJoin("flk", "fake-a"))
	}

	// 类型之间不能互相串位：hardlink 分组里不该出现 symlink 的字段
	hardlinkA := entriesOf(m, deviceA, "hardlink")
	if len(hardlinkA) != 1 {
		t.Fatalf("%s/hardlink 记录数 = %d，期望 1: %#v", deviceA, len(hardlinkA), hardlinkA)
	}
	if _, exists := hardlinkA[0]["fake"]; exists {
		t.Fatalf("hardlink 分组混入了 symlink 的 fake 字段: %#v", hardlinkA[0])
	}

	// 设备之间互相独立
	symlinkB := entriesOf(m, deviceB, "symlink")
	if len(symlinkB) != 1 || symlinkB[0]["fake"] != foldedJoin("flk", "fake-b") {
		t.Fatalf("%s/symlink = %#v，期望恰好含 fake-b 的一条", deviceB, symlinkB)
	}
}

// TestAddRecordFoldsHomePaths 验证路径字段写入前经 NormalizePath + FoldHome 折叠为 ~ 形式
// 防的回归：存储里混入真实家目录绝对路径，导致换用户/换机器后清单整体失效（对比变永远不相等）
// 本用例只做纯字符串运算，不触碰家目录下的任何文件
func TestAddRecordFoldsHomePaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法获取家目录，跳过折叠断言: %v", err)
	}
	// 家目录是文件系统根（filepath.Dir(home) == home）时所有绝对路径都是它的子项，折叠断言没有区分度
	if filepath.Dir(home) == home {
		t.Skipf("家目录 %q 是文件系统根，跳过折叠断言", home)
	}

	sep := string(os.PathSeparator)

	// 家目录本身上应折叠成 ~
	if got := foldRealPath(t, home); got != "~" {
		t.Fatalf("家目录折叠 = %q，期望 %q", got, "~")
	}

	// 家目录真正的子项应折叠成 ~/子路径
	underHome := filepath.Join(home, ".config", "flk", "links", "target")
	wantFolded := "~" + sep + filepath.Join(".config", "flk", "links", "target")
	if got := foldRealPath(t, underHome); got != wantFolded {
		t.Fatalf("家目录子项折叠 = %q，期望 %q（输入 %q）", got, wantFolded, underHome)
	}

	// 与家目录同名前缀、但并非其子项的路径绝不能被折叠
	// 这是 FoldHome 按路径段边界判断的边界防线（home=/root 时 /rootother/x 曾被误折叠成 ~other/x）
	sibling := filepath.Join(home+"-flk-sibling", "keep", "absolute")
	gotSibling := foldRealPath(t, sibling)
	if strings.HasPrefix(gotSibling, "~") {
		t.Fatalf("与家目录同名前缀的路径被误折叠: %q => %q", sibling, gotSibling)
	}
	if gotSibling != filepath.Clean(sibling) {
		t.Fatalf("家目录之外的绝对路径应保持原样: 实际 %q，期望 %q", gotSibling, filepath.Clean(sibling))
	}

	// 额外覆盖一个真实存在的绝对路径（临时目录）；若运行环境的 TMPDIR 恰好在家目录内则不满足前提，明确跳过而不是给出误导性结论
	tmpPath := filepath.Join(t.TempDir(), "outside-home.json")
	if strings.HasPrefix(tmpPath, home+sep) {
		t.Logf("临时目录 %q 位于家目录内，跳过「家目录之外保持绝对」断言", tmpPath)
	} else if got := foldRealPath(t, tmpPath); got != filepath.Clean(tmpPath) {
		t.Fatalf("临时目录绝对路径应保持原样: 实际 %q，期望 %q", got, filepath.Clean(tmpPath))
	}
}

// TestAddRecordFoldsPathsWithoutTouchingFilesystem 验证折叠是纯字符串变换：路径不需要真实存在
// 防的回归：有人把 NormalizePath 换成依赖文件系统（如 EvalSymlinks/Stat）的实现，导致记录写入依赖目标是否存在
func TestAddRecordFoldsPathsWithoutTouchingFilesystem(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法获取家目录，跳过: %v", err)
	}
	if filepath.Dir(home) == home {
		t.Skipf("家目录 %q 是文件系统根，跳过", home)
	}

	// 这些路径全部不存在，AddRecord 仍应稳定折叠出 ~/... 结果
	notExisting := filepath.Join(home, "flk-store-test-not-exist", "deep", "leaf")
	want := "~" + string(os.PathSeparator) + filepath.Join("flk-store-test-not-exist", "deep", "leaf")
	if got := foldRealPath(t, notExisting); got != want {
		t.Fatalf("不存在的家目录子路径折叠 = %q，期望 %q", got, want)
	}
}

// TestAddRecordDedupByLinkType 表驱动验证三种类型的去重键差异
// symlink 以 fake 去重、hardlink 以 seco 去重、copy 以 dst 去重
// 防的回归：去重键写错（例如 symlink 用 real 去重）会导致同一链接被登记多次，check 报告出现重复项
func TestAddRecordDedupByLinkType(t *testing.T) {
	const device = "device-dedup"

	cases := []struct {
		name     string
		linkType string
		dedupKey string
		first    map[string]string
		second   map[string]string // 与 first 共用去重键，其余字段不同
		distinct map[string]string // 去重键也不同，应追加而不是覆盖
	}{
		{
			name:     "symlink 按 fake 去重",
			linkType: "symlink",
			dedupKey: "fake",
			first:    map[string]string{"real": foldedJoin("flk", "real-1"), "fake": foldedJoin("flk", "fake-1")},
			second:   map[string]string{"real": foldedJoin("flk", "real-2"), "fake": foldedJoin("flk", "fake-1")},
			distinct: map[string]string{"real": foldedJoin("flk", "real-3"), "fake": foldedJoin("flk", "fake-3")},
		},
		{
			name:     "hardlink 按 seco 去重",
			linkType: "hardlink",
			dedupKey: "seco",
			first:    map[string]string{"prim": foldedJoin("flk", "prim-1"), "seco": foldedJoin("flk", "seco-1")},
			second:   map[string]string{"prim": foldedJoin("flk", "prim-2"), "seco": foldedJoin("flk", "seco-1")},
			distinct: map[string]string{"prim": foldedJoin("flk", "prim-3"), "seco": foldedJoin("flk", "seco-3")},
		},
		{
			name:     "copy 按 dst 去重",
			linkType: "copy",
			dedupKey: "dst",
			first:    map[string]string{"src": foldedJoin("flk", "src-1"), "dst": foldedJoin("flk", "dst-1")},
			second:   map[string]string{"src": foldedJoin("flk", "src-2"), "dst": foldedJoin("flk", "dst-1")},
			distinct: map[string]string{"src": foldedJoin("flk", "src-3"), "dst": foldedJoin("flk", "dst-3")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestManager()
			m.AddRecord(device, tc.linkType, tc.first)
			m.AddRecord(device, tc.linkType, tc.second)

			// 同键覆盖：长度仍为 1，且内容整体被第二次写入替换
			got := entriesOf(m, device, tc.linkType)
			if len(got) != 1 {
				t.Fatalf("%s 以 %s 去重后记录数 = %d，期望 1: %#v", tc.linkType, tc.dedupKey, len(got), got)
			}
			if !reflect.DeepEqual(got[0], Entry(tc.second)) {
				t.Fatalf("同键覆盖后记录\n实际: %#v\n期望: %#v", got[0], Entry(tc.second))
			}

			// 异键追加：长度变为 2，新记录排在末尾（实现是「过滤旧同键项后 append」）
			m.AddRecord(device, tc.linkType, tc.distinct)
			got = entriesOf(m, device, tc.linkType)
			if len(got) != 2 {
				t.Fatalf("%s 换 %s 后记录数 = %d，期望 2: %#v", tc.linkType, tc.dedupKey, len(got), got)
			}
			if !reflect.DeepEqual(got[1], Entry(tc.distinct)) {
				t.Fatalf("追加记录应位于末尾\n实际: %#v\n期望: %#v", got[1], Entry(tc.distinct))
			}
		})
	}
}

// TestAddRecordUnknownLinkTypeAppendsWithoutDedup 固化「只有三种已知类型才去重」的现状
// 防的回归：新增链接类型时忘记补 dedupField 分支，会使新类型被静默当作「永不去重」，同一目标反复堆叠
func TestAddRecordUnknownLinkTypeAppendsWithoutDedup(t *testing.T) {
	const device = "device-unknown"
	fields := map[string]string{
		"real": foldedJoin("flk", "real"),
		"fake": foldedJoin("flk", "fake"),
	}

	m := newTestManager()
	m.AddRecord(device, "unknown-type", fields)
	m.AddRecord(device, "unknown-type", fields)

	got := entriesOf(m, device, "unknown-type")
	if len(got) != 2 {
		t.Fatalf("未知类型不去重，记录数 = %d，期望 2: %#v", len(got), got)
	}
}

// TestRemoveMatchingEntryRemovesFirstMatch 验证按字段匹配删除第一条命中记录，且剩余顺序保持
// 防的回归：删除越界/删错条目/留下空洞，会让 check 报告与实际清单不一致
func TestRemoveMatchingEntryRemovesFirstMatch(t *testing.T) {
	const device = "device-remove"

	first := Entry{"real": foldedJoin("flk", "real-1"), "fake": foldedJoin("flk", "fake-1")}
	second := Entry{"real": foldedJoin("flk", "real-2"), "fake": foldedJoin("flk", "fake-2")}
	third := Entry{"real": foldedJoin("flk", "real-3"), "fake": foldedJoin("flk", "fake-3")}

	m := newTestManager()
	m.AddRecord(device, "symlink", first)
	m.AddRecord(device, "symlink", second)
	m.AddRecord(device, "symlink", third)

	// 删除中间那条
	m.RemoveMatchingEntry(storeTestPlatform(), device, "symlink", second)

	got := entriesOf(m, device, "symlink")
	if len(got) != 2 {
		t.Fatalf("删除后记录数 = %d，期望 2: %#v", len(got), got)
	}
	if !sameEntries(got, []Entry{first, third}) {
		t.Fatalf("删除后剩余记录\n实际: %#v\n期望: %#v", got, []Entry{first, third})
	}
}

// TestRemoveMatchingEntryMatchesSubset 验证传入 Entry 只需包含参与匹配的字段（子集匹配语义）
// 调用方（cmd/fix.go、cmd/unlink.go）按类型只传 2 个字段，这里钉住「未列出的字段不参与比较」
func TestRemoveMatchingEntryMatchesSubset(t *testing.T) {
	const device = "device-subset"

	first := Entry{"real": foldedJoin("flk", "real-1"), "fake": foldedJoin("flk", "fake-1")}
	second := Entry{"real": foldedJoin("flk", "real-2"), "fake": foldedJoin("flk", "fake-2")}

	m := newTestManager()
	m.AddRecord(device, "symlink", first)
	m.AddRecord(device, "symlink", second)

	// 只给 fake，real 不同也必须命中
	m.RemoveMatchingEntry(storeTestPlatform(), device, "symlink", Entry{"fake": foldedJoin("flk", "fake-1")})

	got := entriesOf(m, device, "symlink")
	if len(got) != 1 || !reflect.DeepEqual(got[0], second) {
		t.Fatalf("子集匹配删除后 = %#v，期望只剩 %#v", got, second)
	}
}

// TestRemoveMatchingEntryNoMatchKeepsData 验证没有任何记录匹配时数据完全不变
// 防的回归：匹配失败时误删（例如把所有 fake 视为匹配），属于静默数据丢失
func TestRemoveMatchingEntryNoMatchKeepsData(t *testing.T) {
	const device = "device-nomatch"

	m := newTestManager()
	m.AddRecord(device, "symlink", Entry{"real": foldedJoin("flk", "real-1"), "fake": foldedJoin("flk", "fake-1")})
	m.AddRecord(device, "symlink", Entry{"real": foldedJoin("flk", "real-2"), "fake": foldedJoin("flk", "fake-2")})

	// 浅拷贝到新切片：后续删除若发生，会在原底层数组上移位，但不会影响这份独立副本的元素
	before := append([]Entry(nil), entriesOf(m, device, "symlink")...)

	m.RemoveMatchingEntry(storeTestPlatform(), device, "symlink", Entry{"fake": foldedJoin("flk", "not-exist")})

	after := entriesOf(m, device, "symlink")
	if !sameEntries(before, after) {
		t.Fatalf("未命中时数据不应变化\n删除前: %#v\n删除后: %#v", before, after)
	}
}

// TestRemoveMatchingEntryRemovesOnlyOne 验证重复记录也只删一条（文档关键字是「第一个匹配」）
// 用不去重的 unknown-type 构造两条完全相同的记录，否则 AddRecord 会先把它们合并掉
func TestRemoveMatchingEntryRemovesOnlyOne(t *testing.T) {
	const device = "device-duplicate"
	entry := Entry{"real": foldedJoin("flk", "real"), "fake": foldedJoin("flk", "fake")}

	m := newTestManager()
	m.AddRecord(device, "unknown-type", entry)
	m.AddRecord(device, "unknown-type", entry)
	if n := len(entriesOf(m, device, "unknown-type")); n != 2 {
		t.Fatalf("前置条件不成立：期望 2 条重复记录，实际 %d 条", n)
	}

	m.RemoveMatchingEntry(storeTestPlatform(), device, "unknown-type", entry)

	got := entriesOf(m, device, "unknown-type")
	if len(got) != 1 {
		t.Fatalf("重复记录应只删一条，剩余 %d 条，期望 1 条: %#v", len(got), got)
	}
}

// TestRemoveMatchingEntryUnknownScopeIsNoop 验证平台/设备/类型不存在时不 panic、不改动数据
// 防的回归：对 nil map 或空切片做赋值/切片越界（cmd/serve_web.go 会在全局实例未初始化时走到这里）
func TestRemoveMatchingEntryUnknownScopeIsNoop(t *testing.T) {
	const device = "device-scope"
	entry := Entry{"real": foldedJoin("flk", "real"), "fake": foldedJoin("flk", "fake")}

	m := newTestManager()
	m.AddRecord(device, "symlink", entry)
	before := append([]Entry(nil), entriesOf(m, device, "symlink")...)

	// 三条路径分别打到不存在的平台、设备、类型上
	m.RemoveMatchingEntry("no-such-platform", device, "symlink", entry)
	m.RemoveMatchingEntry(storeTestPlatform(), "no-such-device", "symlink", entry)
	m.RemoveMatchingEntry(storeTestPlatform(), device, "no-such-type", entry)

	after := entriesOf(m, device, "symlink")
	if !sameEntries(before, after) {
		t.Fatalf("作用域不存在时不应改动数据\n删除前: %#v\n删除后: %#v", before, after)
	}
}

// TestRemoveMatchingEntryNeedsStoredFoldedForm 固化 AddRecord 与 RemoveMatchingEntry 的归一化不对称
// AddRecord 会把路径折叠成 ~/...，RemoveMatchingEntry 却直接用传入值比较（见 store.go 的 RemoveMatchingEntry），
// 因此调用方必须传「与存储形态一致」的折叠值，否则删除静默失败
// 本用例是这条隐含契约的回归防线：若日后给 RemoveMatchingEntry 补上归一化，本用例需要同步改写
func TestRemoveMatchingEntryNeedsStoredFoldedForm(t *testing.T) {
	const device = "device-form"

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法获取家目录，跳过: %v", err)
	}
	if filepath.Dir(home) == home {
		t.Skipf("家目录 %q 是文件系统根，跳过", home)
	}

	rawPath := filepath.Join(home, ".config", "flk", "links", "target")
	storedPath := "~" + string(os.PathSeparator) + filepath.Join(".config", "flk", "links", "target")

	m := newTestManager()
	m.AddRecord(device, "symlink", Entry{"real": rawPath, "fake": storedPath})

	// 前置条件：写入后被折叠成 ~/...，说明 rawPath 与存储形态确实不同
	stored := entriesOf(m, device, "symlink")
	if len(stored) != 1 || stored[0]["real"] != storedPath {
		t.Fatalf("前置条件不成立：real 写入后 = %#v，期望 %q", stored, storedPath)
	}

	// 用未折叠的原始绝对路径去删 → 匹配不上，记录仍在（现状，属于隐患而非期望行为）
	m.RemoveMatchingEntry(storeTestPlatform(), device, "symlink", Entry{"real": rawPath, "fake": storedPath})
	if n := len(entriesOf(m, device, "symlink")); n != 1 {
		t.Fatalf("现状下未折叠路径不应命中删除，记录数 = %d，期望仍为 1", n)
	}

	// 用与存储一致的折叠值去删 → 命中并删除
	m.RemoveMatchingEntry(storeTestPlatform(), device, "symlink", Entry{"real": storedPath, "fake": storedPath})
	if n := len(entriesOf(m, device, "symlink")); n != 0 {
		t.Fatalf("折叠形态一致时应删除，记录数 = %d，期望 0: %#v", n, entriesOf(m, device, "symlink"))
	}
}

// TestSaveAndLoadRoundTrip 验证 Save 落盘后 LoadFromFile 读回的数据与内存等价
// 覆盖：自动创建多级父目录、JSON 文件本身合法、往返后条目顺序稳定（Save 会先排序）
func TestSaveAndLoadRoundTrip(t *testing.T) {
	const device = "device-roundtrip"

	m := newTestManager()
	m.AddRecord(device, "symlink", Entry{"real": foldedJoin("flk", "real"), "fake": foldedJoin("flk", "fake")})
	m.AddRecord(device, "hardlink", Entry{"prim": foldedJoin("flk", "prim"), "seco": foldedJoin("flk", "seco")})
	m.AddRecord(device, "copy", Entry{"src": foldedJoin("flk", "src"), "dst": foldedJoin("flk", "dst")})

	// 父目录故意不存在，验证 Save 会 MkdirAll
	path := filepath.Join(t.TempDir(), "nested", "deeper", "flk-store.json")
	if err := m.Save(path); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	loaded, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile 失败: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadFromFile 返回 nil manager")
	}
	if !reflect.DeepEqual(loaded.Snapshot(), m.Snapshot()) {
		t.Fatalf("往返数据不一致\n实际: %#v\n期望: %#v", loaded.Snapshot(), m.Snapshot())
	}

	// 落盘内容必须是新格式（3 层）且能被直接反序列化成 RootConfig
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取落盘文件失败: %v", err)
	}
	var decoded RootConfig
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("落盘文件不是合法的新格式 JSON: %v，内容: %s", err, raw)
	}
	if !reflect.DeepEqual(decoded, m.Snapshot()) {
		t.Fatalf("落盘文件内容与内存不一致\n文件: %#v\n内存: %#v", decoded, m.Snapshot())
	}

	// 往返后 ToJSON 输出稳定（排序 + json map 键排序都应是确定性的）
	if got, want := loaded.ToJSON(), m.ToJSON(); got != want {
		t.Fatalf("往返后 ToJSON 输出不一致\n实际: %s\n期望: %s", got, want)
	}
}

// TestSaveReturnsErrorWhenParentIsFile 验证父路径被普通文件占用时 Save 如实报错
// 防的回归：错误被吞掉导致上层以为已持久化，而磁盘上其实没有这份清单
func TestSaveReturnsErrorWhenParentIsFile(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("占用"), 0o644); err != nil {
		t.Fatalf("创建占位文件失败: %v", err)
	}

	m := newTestManager()
	m.AddRecord("device-save-error", "symlink", Entry{"real": foldedJoin("flk", "real"), "fake": foldedJoin("flk", "fake")})

	if err := m.Save(filepath.Join(blocker, "child.json")); err == nil {
		t.Fatal("父路径为普通文件时 Save 应返回错误")
	}
}

// TestLoadFromFileMissingFile 验证文件不存在时返回 nil manager 与可被 os.IsNotExist 识别的错误
// 这层语义是 InitStore 区分「首次运行」和「真故障」的唯一依据
func TestLoadFromFileMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")

	m, err := LoadFromFile(missing)
	if m != nil {
		t.Fatalf("文件不存在时 manager = %#v，期望 nil", m)
	}
	if err == nil {
		t.Fatal("文件不存在时应返回错误")
	}
	if !os.IsNotExist(err) {
		t.Fatalf("错误 = %v，期望 os.IsNotExist(err) 为真", err)
	}
}

// TestLoadFromFileEmptyFile 验证 0 字节文件被当作「空清单」而不是解析错误
// 防的回归：把空文件报成损坏文件，导致首次运行被误判为故障
func TestLoadFromFileEmptyFile(t *testing.T) {
	path := writeStoreFile(t, "")

	m, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("空文件不应报错: %v", err)
	}
	if m == nil {
		t.Fatalf("空文件应返回空 manager，实际: %#v", m)
	}
	// 空清单语义：Snapshot 保证返回非 nil 的空表，调用方 len/range 都安全
	if snapshot := m.Snapshot(); len(snapshot) != 0 {
		t.Fatalf("空文件的清单长度 = %d，期望 0: %#v", len(snapshot), snapshot)
	}
	if got := m.ToJSON(); got != "{}" {
		t.Fatalf("空清单 ToJSON = %q，期望 %q", got, "{}")
	}
}

// TestLoadFromFileEmptyObject 验证内容为 {} 的文件同样得到可安全写入的空 manager
// {} 是正常的空对象路径，与 0 字节文件、裸 null 三条入口最终都归一成非 nil 的空清单
// （null 那条入口另由 TestLoadFromFileNullContentYieldsUsableManager 覆盖）
func TestLoadFromFileEmptyObject(t *testing.T) {
	path := writeStoreFile(t, "{}")

	m, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("{} 文件不应报错: %v", err)
	}
	if m == nil || m.Snapshot() == nil {
		t.Fatalf("{} 文件应返回清单可用的 manager，实际: %#v", m)
	}

	// 关键差异：Data 非 nil，追加记录不应 panic
	m.AddRecord("device-empty-object", "symlink", Entry{"real": foldedJoin("flk", "real"), "fake": foldedJoin("flk", "fake")})
	if n := len(entriesOf(m, "device-empty-object", "symlink")); n != 1 {
		t.Fatalf("空对象文件追加记录后 = %d 条，期望 1 条", n)
	}
}

// TestLoadFromFileMigratesLegacyFormat 验证旧 4 层格式被迁移为 3 层新格式
// 旧格式：{platform:{device:{linkType:{parentPath:[Entry]}}}}，其中 real/prim/src 是相对 parentPath 的相对路径
// 迁移后这三个字段必须拼成完整路径，fake/seco/dst 已存折叠绝对路径必须原值保留，并且文件被自动改写为新格式
func TestLoadFromFileMigratesLegacyFormat(t *testing.T) {
	const device = "device-legacy"
	platform := storeTestPlatform()

	// 第一个 parentPath 无尾部分隔符，第二个带尾部斜杠，用于覆盖 TrimRight 分支
	legacy := fmt.Sprintf(`{
    "%s": {
        "%s": {
            "symlink": {
                "~/.config/nvim": [
                    {"real": "init.lua", "fake": "~/.config/flk/nvim-init.lua"}
                ],
                "~/.config/old/": [
                    {"real": "a.lua", "fake": "~/.config/flk/a.lua"},
                    {"real": "b.lua", "fake": "~/.config/flk/b.lua"}
                ]
            },
            "hardlink": {
                "~/data": [{"prim": "p.bin", "seco": "~/links/p.bin"}]
            },
            "copy": {
                "~/src": [{"src": "s.txt", "dst": "~/dst/s.txt"}]
            }
        }
    }
}`, platform, device)

	path := writeStoreFile(t, legacy)

	m, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("迁移旧格式失败: %v", err)
	}
	if m == nil {
		t.Fatalf("迁移后 manager 异常: %#v", m)
	}

	// symlink：两个 parentPath 共 3 条记录；pathGroup 是 map，组间顺序不确定，故按 fake 查找而不是按下标断言
	symlinks := entriesOf(m, device, "symlink")
	if len(symlinks) != 3 {
		t.Fatalf("symlink 记录数 = %d，期望 3: %#v", len(symlinks), symlinks)
	}
	nvimEntry := findEntry(symlinks, "fake", "~/.config/flk/nvim-init.lua")
	if nvimEntry == nil {
		t.Fatalf("未找到 fake = ~/.config/flk/nvim-init.lua 的记录: %#v", symlinks)
	}
	if got := nvimEntry["real"]; got != "~/.config/nvim/init.lua" {
		t.Fatalf("相对 real 拼接结果 = %q，期望 %q", got, "~/.config/nvim/init.lua")
	}

	// 带尾部斜杠的 parentPath 应先去尾再拼接，不能出现 "~/.config/old//a.lua"
	oldEntry := findEntry(symlinks, "fake", "~/.config/flk/a.lua")
	if oldEntry == nil {
		t.Fatalf("未找到 fake = ~/.config/flk/a.lua 的记录: %#v", symlinks)
	}
	if got := oldEntry["real"]; got != "~/.config/old/a.lua" {
		t.Fatalf("带尾斜杠 parentPath 的拼接结果 = %q，期望 %q", got, "~/.config/old/a.lua")
	}

	// hardlink：prim 拼接，seco 原值保留
	hardlinks := entriesOf(m, device, "hardlink")
	if len(hardlinks) != 1 {
		t.Fatalf("hardlink 记录数 = %d，期望 1: %#v", len(hardlinks), hardlinks)
	}
	if got := hardlinks[0]["prim"]; got != "~/data/p.bin" {
		t.Fatalf("prim 拼接结果 = %q，期望 %q", got, "~/data/p.bin")
	}
	if got := hardlinks[0]["seco"]; got != "~/links/p.bin" {
		t.Fatalf("seco 应保持原值 %q，实际 %q", "~/links/p.bin", got)
	}

	// copy：src 拼接，dst 原值保留
	copies := entriesOf(m, device, "copy")
	if len(copies) != 1 {
		t.Fatalf("copy 记录数 = %d，期望 1: %#v", len(copies), copies)
	}
	if got := copies[0]["src"]; got != "~/src/s.txt" {
		t.Fatalf("src 拼接结果 = %q，期望 %q", got, "~/src/s.txt")
	}
	if got := copies[0]["dst"]; got != "~/dst/s.txt" {
		t.Fatalf("dst 应保持原值 %q，实际 %q", "~/dst/s.txt", got)
	}

	// 迁移后文件应被自动改写为新格式：新格式能被 RootConfig 直接反序列化（旧格式做不到，会报 cannot unmarshal object into []Entry）
	rewritten, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取迁移后文件失败: %v", err)
	}
	var decoded RootConfig
	if err := json.Unmarshal(rewritten, &decoded); err != nil {
		t.Fatalf("迁移后文件不是合法新格式: %v，内容: %s", err, rewritten)
	}
	// 落盘内容是排序过的（Save 只在 Snapshot 出的私有副本上排序，内存仍保持迁移时的插入顺序），
	// 因此比较前用同一个 sortRootConfig 把两侧归一成同一顺序，只校验内容集合是否一致
	// 顺序差异本身由 TestToJSONIsDeterministicAndSorted 单独钉住
	sortRootConfig(decoded)
	inMemory := m.Snapshot()
	sortRootConfig(inMemory)
	if !reflect.DeepEqual(decoded, inMemory) {
		t.Fatalf("迁移后落盘内容与内存不一致\n文件: %#v\n内存: %#v", decoded, inMemory)
	}
}

// TestLoadFromFileMigratesLegacyWindowsSeparator 验证旧格式 parentPath 使用反斜杠时沿用反斜杠拼接
// 防的回归：Windows 迁移出的路径混用分隔符（C:\a/b），使后续字符串比较与删除匹配全部失效
func TestLoadFromFileMigratesLegacyWindowsSeparator(t *testing.T) {
	const device = "device-legacy-windows"
	platform := storeTestPlatform()

	// JSON 中的 \\ 表示单个反斜杠；parentPath 带尾反斜杠用于覆盖 TrimRight("\\") 分支
	legacy := fmt.Sprintf(`{
    "%s": {
        "%s": {
            "symlink": {
                "C:\\Users\\flk\\.config\\": [
                    {"real": "init.lua", "fake": "~/flk/init.lua"}
                ]
            }
        }
    }
}`, platform, device)

	path := writeStoreFile(t, legacy)

	m, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("迁移反斜杠旧格式失败: %v", err)
	}

	symlinks := entriesOf(m, device, "symlink")
	if len(symlinks) != 1 {
		t.Fatalf("symlink 记录数 = %d，期望 1: %#v", len(symlinks), symlinks)
	}
	if got, want := symlinks[0]["real"], `C:\Users\flk\.config\init.lua`; got != want {
		t.Fatalf("反斜杠拼接结果 = %q，期望 %q", got, want)
	}
	if got, want := symlinks[0]["fake"], "~/flk/init.lua"; got != want {
		t.Fatalf("fake 应保持原值 %q，实际 %q", want, got)
	}
}

// TestLoadFromFileRejectsUnparsableJSON 验证既不是新格式也不是旧格式的内容如实报错
// 防的回归：损坏文件被静默当成空清单，用户会以为清单「自己清空了」而不知道文件已损坏
func TestLoadFromFileRejectsUnparsableJSON(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{name: "截断的 JSON", content: "{not json"},
		{name: "纯文本", content: "hello"},
		{name: "数组而不是对象", content: "[1, 2, 3]"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeStoreFile(t, tc.content)

			m, err := LoadFromFile(path)
			if err == nil {
				t.Fatalf("无法解析的内容应报错，实际 manager = %#v", m)
			}
			if m != nil {
				t.Fatalf("解析失败时 manager 应为 nil，实际 %#v", m)
			}
		})
	}
}

// TestLoadFromFileNullContentYieldsUsableManager 验证内容为 null 的文件被归一成可用空清单，而不是 nil 内部清单
// 已修复的隐患：json.Unmarshal("null") 对 map 会成功并把 RootConfig 留成 nil map，
// 旧实现于是返回 Manager{Data: nil} 且不报错，调用方一 AddRecord 就 panic（assignment to entry in nil map）
// 现在 LoadFromFile 的成功路径统一经 newManagerFromData 收口，null 与 0 字节 / {} 得到同样的空清单
func TestLoadFromFileNullContentYieldsUsableManager(t *testing.T) {
	path := writeStoreFile(t, "null")

	m, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("内容为 null 的文件不应报错: %v", err)
	}
	if m == nil {
		t.Fatal("LoadFromFile 不应返回 nil manager")
	}
	// 内部 data 已不导出，改用 Snapshot 观察：空清单必须是长度 0 的空表而不是会引发 panic 的 nil
	// 写入可用性由 TestAddRecordAfterNullLoadDoesNotPanic 直接覆盖，这里只钉住只读形态
	if snapshot := m.Snapshot(); len(snapshot) != 0 {
		t.Fatalf("null 内容归一后应是空清单，实际 %#v", snapshot)
	}
	if got := m.ToJSON(); got != "{}" {
		t.Fatalf("null 内容的 ToJSON = %q，期望 %q", got, "{}")
	}
}

// TestAddRecordAfterNullLoadDoesNotPanic 验证从 null 文件加载后直接 AddRecord 不再 panic 且记录落入正确位置
// 这是缺陷最直接的复现路径：命令加载存储后立刻 AddRecord（例如 cmd/symlink.go），一旦 Data 为 nil 就崩在赋值处
// 用例不放 recover，修复前它会以 panic 直接失败；修复后必须同时满足「层级正确」和「内容未被加工坏」
func TestAddRecordAfterNullLoadDoesNotPanic(t *testing.T) {
	const device = "device-null-load"
	path := writeStoreFile(t, "null")

	m, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("加载 null 文件失败: %v", err)
	}

	realPath := foldedJoin("flk", "real")
	fakePath := foldedJoin("flk", "fake")
	m.AddRecord(device, "symlink", Entry{"real": realPath, "fake": fakePath})

	entries := entriesOf(m, device, "symlink")
	if len(entries) != 1 {
		t.Fatalf("追加后记录数 = %d，期望 1: %#v", len(entries), entries)
	}
	if entries[0]["fake"] != fakePath {
		t.Fatalf("记录 fake = %q，期望 %q", entries[0]["fake"], fakePath)
	}
	if entries[0]["real"] != realPath {
		t.Fatalf("记录 real = %q，期望 %q", entries[0]["real"], realPath)
	}
}

// TestNilDataManagerIsUsable 验证内部清单为 nil 的 Manager 也能安全读序列化与写入
// 内部清单字段已不导出，包外再也构造不出这种状态；用例保留它是因为 AddRecord / ToJSON / Snapshot 三条出口
// 各自都留了兜底，兜底在防：nil map 赋值 panic，以及序列化出裸 null（前端 /api/config 按对象处理，null 取属性会出错）
func TestNilDataManagerIsUsable(t *testing.T) {
	m := &Manager{}

	if got := m.ToJSON(); got != "{}" {
		t.Fatalf("nil 清单的 ToJSON = %q，期望 %q", got, "{}")
	}
	// ToJSON 只允许在 Snapshot 出的私有副本上归一化与排序：一旦写回内部，就等于在只读锁内改写内部状态，
	// 并发读到的条目顺序会随序列化时机漂移。这里直读内部字段（单协程用例，刻意不取锁）来钉住这一点
	if m.data != nil {
		t.Fatalf("ToJSON 不应就地改写内部清单，实际被改成 %#v", m.data)
	}

	const device = "device-nil-data"
	m.AddRecord(device, "hardlink", Entry{"prim": foldedJoin("flk", "prim"), "seco": foldedJoin("flk", "seco")})

	if n := len(entriesOf(m, device, "hardlink")); n != 1 {
		t.Fatalf("nil 清单追加后记录数 = %d，期望 1", n)
	}
}

// TestNilDataManagerSaveWritesEmptyObject 验证内部清单为 nil 的 Save 写出 {} 而不是裸 null
// 否则缺陷会随着磁盘文件在「读入 → 写出」之间循环，每次启动都重新制造一份 null 清单
func TestNilDataManagerSaveWritesEmptyObject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flk-store.json")

	m := &Manager{}
	if err := m.Save(path); err != nil {
		t.Fatalf("nil 清单的 Save 失败: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 Save 产物失败: %v", err)
	}
	if got := string(raw); got != "{}" {
		t.Fatalf("nil 清单的 Save 产物 = %q，期望 %q", got, "{}")
	}
}

// TestToJSONMatchesData 验证 ToJSON 输出是合法 JSON 且与内存 Data 等价
// 防的回归：/api/config 与 serve 前端依赖这段输出，一旦字段丢失或结构变形会导致前端解析出错
func TestToJSONMatchesData(t *testing.T) {
	const device = "device-tojson"

	m := newTestManager()
	m.AddRecord(device, "symlink", Entry{"real": foldedJoin("flk", "real"), "fake": foldedJoin("flk", "fake")})
	m.AddRecord(device, "hardlink", Entry{"prim": foldedJoin("flk", "prim"), "seco": foldedJoin("flk", "seco")})
	m.AddRecord(device, "copy", Entry{"src": foldedJoin("flk", "src"), "dst": foldedJoin("flk", "dst")})

	output := m.ToJSON()
	var decoded RootConfig
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("ToJSON 输出不是合法 JSON: %v，输出: %s", err, output)
	}
	if !reflect.DeepEqual(decoded, m.Snapshot()) {
		t.Fatalf("ToJSON 反序列化结果与清单数据不一致\n实际: %#v\n期望: %#v", decoded, m.Snapshot())
	}
}

// TestToJSONEmptyDataIsEmptyObject 验证空清单序列化为 {} 而不是 null
// 前端按对象处理返回值，null 会在解析后取属性时出错
func TestToJSONEmptyDataIsEmptyObject(t *testing.T) {
	if got := newTestManager().ToJSON(); got != "{}" {
		t.Fatalf("空清单 ToJSON = %q，期望 %q", got, "{}")
	}
}

// TestToJSONIsDeterministicAndSorted 验证排序稳定性：插入顺序不同、调用多次，输出都完全一致
// toJSON/Save 共用 sortRootConfig，用户反复保存或前端轮询时不应出现无意义的 diff
//
// 排序的落点：sortRootConfig 会就地改写切片顺序，而 ToJSON/Save 都只对 Snapshot 出来的私有副本排序，
// 因此「输出稳定」与「内存保持插入顺序」需要分别断言，本用例两段都在
func TestToJSONIsDeterministicAndSorted(t *testing.T) {
	const device = "device-sort"

	dsts := []string{foldedJoin("flk", "z"), foldedJoin("flk", "a"), foldedJoin("flk", "m")}

	forward := newTestManager()
	for _, dst := range dsts {
		forward.AddRecord(device, "copy", map[string]string{"dst": dst})
	}
	backward := newTestManager()
	for i := len(dsts) - 1; i >= 0; i-- {
		backward.AddRecord(device, "copy", map[string]string{"dst": dsts[i]})
	}

	forwardJSON := forward.ToJSON()
	if got := backward.ToJSON(); got != forwardJSON {
		t.Fatalf("同一份数据不同插入顺序输出应一致\n正序: %s\n逆序: %s", forwardJSON, got)
	}
	if got := forward.ToJSON(); got != forwardJSON {
		t.Fatalf("同一实例多次调用 ToJSON 输出应一致\n第一次: %s\n第二次: %s", forwardJSON, got)
	}

	// 第一段：序列化结果里的条目按字段值升序排列（排序作用在私有副本上，所以只能从输出里观察）
	var decoded RootConfig
	if err := json.Unmarshal([]byte(forwardJSON), &decoded); err != nil {
		t.Fatalf("ToJSON 输出不是合法 JSON: %v，输出: %s", err, forwardJSON)
	}
	wantOrder := []string{foldedJoin("flk", "a"), foldedJoin("flk", "m"), foldedJoin("flk", "z")}
	got := decoded[storeTestPlatform()][device]["copy"]
	if len(got) != len(wantOrder) {
		t.Fatalf("序列化后记录数 = %d，期望 %d: %#v", len(got), len(wantOrder), got)
	}
	for i, want := range wantOrder {
		if got[i]["dst"] != want {
			t.Fatalf("序列化结果第 %d 条 dst = %q，期望 %q；实际顺序: %#v", i, got[i]["dst"], want, got)
		}
	}

	// 第二段：内存顺序保持插入顺序，不受 ToJSON / Save 的排序影响
	inMemory := entriesOf(forward, device, "copy")
	insertionOrder := []string{foldedJoin("flk", "z"), foldedJoin("flk", "a"), foldedJoin("flk", "m")}
	if len(inMemory) != len(insertionOrder) {
		t.Fatalf("内存记录数 = %d，期望 %d: %#v", len(inMemory), len(insertionOrder), inMemory)
	}
	for i, want := range insertionOrder {
		if inMemory[i]["dst"] != want {
			t.Fatalf("内存中第 %d 条 dst = %q，期望 %q（内存应保持插入顺序）；实际: %#v", i, inMemory[i]["dst"], want, inMemory)
		}
	}
}

// TestInitStoreMissingFileStartsEmpty 验证存储文件不存在时 InitStore 建立空清单且不报错
// 同时钉住当前契约：初始化阶段不会预先创建文件（首次真正写入时才由 Save 落盘）
func TestInitStoreMissingFileStartsEmpty(t *testing.T) {
	preserveGlobal(t)

	path := filepath.Join(t.TempDir(), "nested", "missing.json")
	if err := InitStore(path); err != nil {
		t.Fatalf("存储文件不存在时 InitStore 不应报错: %v", err)
	}
	mgr := Global()
	if mgr == nil {
		t.Fatal("InitStore 后全局实例不应为 nil")
	}
	// 新建清单必须是可用的空表：内部为 nil map 时后续 AddRecord 会 panic
	if snapshot := mgr.Snapshot(); len(snapshot) != 0 {
		t.Fatalf("新建清单应为空，实际: %#v", snapshot)
	}

	// 当前契约：初始化不落盘
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("InitStore 现状不应创建文件，实际 stat 错误: %v", err)
	}
}

// TestInitStoreLoadsExistingFile 验证 InitStore 读取已有清单并发布到全局实例
func TestInitStoreLoadsExistingFile(t *testing.T) {
	preserveGlobal(t)

	const device = "device-init"
	source := newTestManager()
	source.AddRecord(device, "symlink", Entry{"real": foldedJoin("flk", "real"), "fake": foldedJoin("flk", "fake")})
	source.AddRecord(device, "copy", Entry{"src": foldedJoin("flk", "src"), "dst": foldedJoin("flk", "dst")})

	path := filepath.Join(t.TempDir(), "flk-store.json")
	if err := source.Save(path); err != nil {
		t.Fatalf("准备存储文件失败: %v", err)
	}

	if err := InitStore(path); err != nil {
		t.Fatalf("InitStore 失败: %v", err)
	}
	mgr := Global()
	if mgr == nil {
		t.Fatal("InitStore 后全局实例不应为 nil")
	}
	if !reflect.DeepEqual(mgr.Snapshot(), source.Snapshot()) {
		t.Fatalf("全局清单与文件内容不一致\n实际: %#v\n期望: %#v", mgr.Snapshot(), source.Snapshot())
	}
}

// TestInitStoreBrokenFileKeepsGlobalStore 验证解析失败时 InitStore 返回错误且不覆盖已有的全局实例
// 防的回归：初始化失败后全局实例被换成半成品或 nil，后续 Save 会把用户清单覆盖成空
func TestInitStoreBrokenFileKeepsGlobalStore(t *testing.T) {
	preserveGlobal(t)

	// 预先放一个可识别的「旧值」作为哨兵
	sentinel := newTestManager()
	sentinel.AddRecord("device-sentinel", "symlink", Entry{"real": foldedJoin("flk", "real"), "fake": foldedJoin("flk", "fake")})
	SetGlobal(sentinel)

	path := writeStoreFile(t, "{not json")
	err := InitStore(path)
	if err == nil {
		t.Fatal("损坏的存储文件应让 InitStore 报错")
	}
	if Global() != sentinel {
		t.Fatalf("解析失败时全局实例应保持原值，实际被替换为: %#v", Global())
	}
	if n := len(entriesOf(sentinel, "device-sentinel", "symlink")); n != 1 {
		t.Fatalf("哨兵数据被破坏，记录数 = %d，期望 1", n)
	}
}

// TestInitStoreMigratesLegacyFile 验证 InitStore 对旧格式文件完成迁移并落盘为新格式
// 这是用户升级路径的关键回归防线：迁移只发生在读入时，必须同时改写文件，否则每次启动都要重算
func TestInitStoreMigratesLegacyFile(t *testing.T) {
	preserveGlobal(t)

	const device = "device-init-legacy"
	platform := storeTestPlatform()
	legacy := fmt.Sprintf(`{
    "%s": {
        "%s": {
            "copy": {
                "~/src": [{"src": "s.txt", "dst": "~/dst/s.txt"}]
            }
        }
    }
}`, platform, device)

	path := writeStoreFile(t, legacy)

	if err := InitStore(path); err != nil {
		t.Fatalf("InitStore 迁移旧格式失败: %v", err)
	}
	mgr := Global()
	if mgr == nil {
		t.Fatalf("迁移后全局实例异常: %#v", mgr)
	}

	copies := entriesOf(mgr, device, "copy")
	if len(copies) != 1 {
		t.Fatalf("迁移后 copy 记录数 = %d，期望 1: %#v", len(copies), copies)
	}
	if got := copies[0]["src"]; got != "~/src/s.txt" {
		t.Fatalf("迁移后 src = %q，期望 %q", got, "~/src/s.txt")
	}

	// 文件已被改写为新格式，可被 RootConfig 直接反序列化
	rewritten, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取迁移后文件失败: %v", err)
	}
	var decoded RootConfig
	if err := json.Unmarshal(rewritten, &decoded); err != nil {
		t.Fatalf("迁移后文件不是合法新格式: %v，内容: %s", err, rewritten)
	}
	if !reflect.DeepEqual(decoded, mgr.Snapshot()) {
		t.Fatalf("迁移后落盘内容与内存不一致\n文件: %#v\n内存: %#v", decoded, mgr.Snapshot())
	}
}

// TestSnapshotIsDeepCopy 逐层破坏 Snapshot 的返回值，验证内部清单毫发无损
// 防的回归：只复制最外层（或者干脆把内部引用交出去）时，调用方遍历期间一旦有并发写入就是
// map 并发读写 panic；本用例是结构性防线（比对内容），不依赖时序，因此不会偶发通过或失败
func TestSnapshotIsDeepCopy(t *testing.T) {
	const device = "device-snapshot"
	m := newTestManager()
	m.AddRecord(device, "symlink", Entry{"real": foldedJoin("flk", "real"), "fake": foldedJoin("flk", "fake")})

	snapshot := m.Snapshot()
	// 逐层破坏副本：加平台、加类型、改字段、追加条目
	snapshot["injected-platform"] = DeviceGroup{"dev": TypeGroup{"symlink": []Entry{{"fake": "~/injected"}}}}
	snapshot[storeTestPlatform()][device]["copy"] = []Entry{{"src": "~/s", "dst": "~/d"}}
	existing := snapshot[storeTestPlatform()][device]["symlink"]
	existing[0]["fake"] = "~/tampered"
	snapshot[storeTestPlatform()][device]["symlink"] = append(existing, Entry{"real": "~/extra", "fake": "~/extra"})

	internal := m.Snapshot()
	if len(internal) != 1 {
		t.Fatalf("内部平台数 = %d，期望 1（副本加平台不得影响内部）: %#v", len(internal), internal)
	}
	typeGroup := internal[storeTestPlatform()][device]
	if len(typeGroup) != 1 {
		t.Fatalf("内部类型数 = %d，期望 1（副本加类型不得影响内部）: %#v", len(typeGroup), typeGroup)
	}
	entries := typeGroup["symlink"]
	if len(entries) != 1 {
		t.Fatalf("内部条目数 = %d，期望 1（副本追加不得影响内部）: %#v", len(entries), entries)
	}
	if got := entries[0]["fake"]; got != foldedJoin("flk", "fake") {
		t.Fatalf("内部条目 fake = %q，期望 %q（副本改字段不得影响内部）", got, foldedJoin("flk", "fake"))
	}

	// 两次 Snapshot 之间同样互不影响，否则调用方之间会互相污染
	other := m.Snapshot()
	other[storeTestPlatform()][device]["symlink"][0]["real"] = "~/tampered-again"
	if got := m.Snapshot()[storeTestPlatform()][device]["symlink"][0]["real"]; got != foldedJoin("flk", "real") {
		t.Fatalf("内部条目 real = %q，期望 %q", got, foldedJoin("flk", "real"))
	}
}

// TestManagerConcurrentUseIsRaceFree 并发压测 Manager 的三类操作，配合 -race 检测数据竞争
// 复刻 serve 服务的真实形态：写（AddRecord / Replace）、读（Snapshot 遍历 / ToJSON）、落盘（Save）同时发生
// 断言的是「并发结束后仍自洽」而不是精确条目数（交错顺序不确定）：
// 同一去重键最多留一条、落盘内容与内存内容一致，这两条足以发现丢更新与半写状态
func TestManagerConcurrentUseIsRaceFree(t *testing.T) {
	const device = "device-concurrent"
	const workers = 8
	const iterations = 40

	m := newTestManager()
	path := filepath.Join(t.TempDir(), "flk-store.json")

	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				unique := fmt.Sprintf("%d-%d", worker, i)
				switch i % 4 {
				case 0:
					m.AddRecord(device, "symlink", map[string]string{
						"real": foldedJoin("flk", "real", unique),
						"fake": foldedJoin("flk", "fake", unique),
					})
				case 1:
					// 像 performCheck 那样长遍历快照：遍历过程中必须能读完整条目
					for _, deviceGroup := range m.Snapshot() {
						for _, typeGroup := range deviceGroup {
							for linkType, entries := range typeGroup {
								for _, e := range entries {
									if e["fake"] == "" {
										t.Errorf("快照中 %s 条目的 fake 为空: %#v", linkType, e)
									}
								}
							}
						}
					}
				case 2:
					if got := m.ToJSON(); !json.Valid([]byte(got)) {
						t.Errorf("ToJSON 输出不是合法 JSON: %s", got)
					}
					if err := m.Save(path); err != nil {
						t.Errorf("Save 失败: %v", err)
					}
				case 3:
					m.RemoveMatchingEntry(storeTestPlatform(), device, "symlink", Entry{
						"real": foldedJoin("flk", "real", unique),
						"fake": foldedJoin("flk", "fake", unique),
					})
				}
			}
		}(worker)
	}
	wg.Wait()

	// 并发结束后的收尾断言：落盘一次再读回，内容集合必须与内存一致（顺序用 sortRootConfig 归一）
	if err := m.Save(path); err != nil {
		t.Fatalf("并发结束后的 Save 失败: %v", err)
	}
	rewritten, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取落盘文件失败: %v", err)
	}
	var decoded RootConfig
	if err := json.Unmarshal(rewritten, &decoded); err != nil {
		t.Fatalf("落盘文件不是合法新格式: %v，内容: %s", err, rewritten)
	}
	sortRootConfig(decoded)
	inMemory := m.Snapshot()
	sortRootConfig(inMemory)
	if !reflect.DeepEqual(decoded, inMemory) {
		t.Fatalf("并发结束后落盘内容与内存不一致\n文件: %#v\n内存: %#v", decoded, inMemory)
	}

	// 去重键唯一性：每个 fake 路径最多出现一次，重复说明 AddRecord 的「读当前条目 → 去重 → 追加」被并发穿插了
	seen := make(map[string]bool)
	for _, e := range inMemory[storeTestPlatform()][device]["symlink"] {
		if seen[e["fake"]] {
			t.Fatalf("并发写入后 fake = %q 出现重复条目: %#v", e["fake"], inMemory)
		}
		seen[e["fake"]] = true
	}
}

// TestEnsureGlobalPublishesInstance 校验全局实例访问器的语义：为 nil 时创建空实例并发布，非 nil 时原样返回
// 使用场景：serve 的 POST /api/config 在 InitStore 失败时仍要有个落点承接写入
func TestEnsureGlobalPublishesInstance(t *testing.T) {
	preserveGlobal(t)
	SetGlobal(nil)

	created := EnsureGlobal()
	if created == nil {
		t.Fatal("全局实例为 nil 时 EnsureGlobal 应创建空实例")
	}
	if snapshot := created.Snapshot(); len(snapshot) != 0 {
		t.Fatalf("新建的空实例清单应为空，实际: %#v", snapshot)
	}
	// 发布语义：创建出来的实例必须立即可被 Global() 看到，否则调用方会各自拿到不同实例
	if Global() != created {
		t.Fatalf("EnsureGlobal 创建后应发布到全局，实际 Global() = %#v", Global())
	}

	existing := newTestManager()
	existing.AddRecord("device-ensure", "symlink", Entry{"real": foldedJoin("flk", "real"), "fake": foldedJoin("flk", "fake")})
	SetGlobal(existing)
	if got := EnsureGlobal(); got != existing {
		t.Fatalf("已有全局实例时 EnsureGlobal 不应替换它，实际 %#v", got)
	}
}
