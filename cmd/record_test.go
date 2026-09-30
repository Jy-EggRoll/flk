package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/jy-eggroll/flk/internal/output"
	"github.com/jy-eggroll/flk/internal/store"
)

// 本文件是 cmd 包的第一个单元测试文件，只覆盖组3 抽取出来的共享函数：
//   - buildRecordEntry：三类型到 store 匹配键的纯映射
//   - removeTrackedRecord：store 判空、空匹配键保护、从内存移除
//   - saveTrackedStore：store 判空、落盘
// 约定：cmd 包的测试与被测代码同包，可直接访问未导出函数；
// 任何会写全局状态（store 的全局实例 / store.StorePath）的用例都必须用 t.Cleanup 还原，
// 否则会污染同包其它用例，而且绝不允许触碰真实的 ~/.config/flk/flk-store.json

// withTempTrackedStore 为单个用例装配一份临时全局存储，并登记还原逻辑
// 返回临时存储文件路径（位于 t.TempDir()，用例结束由 testing 框架自动清理）
// 关键点：先备份旧的全局实例 / StorePath，再用 t.Cleanup 还原，
// 保证用例之间互不影响，也不会把测试数据写进用户真实配置
// 全局实例的读写一律走 store.SetGlobal / store.Global：裸变量已被删除，这正是并发安全的落点
func withTempTrackedStore(t *testing.T, data store.RootConfig) string {
	t.Helper()

	storePath := filepath.Join(t.TempDir(), "flk-store.json")
	prevManager, prevStorePath := store.Global(), store.StorePath
	// 把用例给定的清单装进新实例：Replace 语义等价于「整体替换」，且不必依赖 Manager 内部字段
	tempManager := store.New()
	tempManager.Replace(data)
	store.SetGlobal(tempManager)
	store.StorePath = storePath

	t.Cleanup(func() {
		store.SetGlobal(prevManager)
		store.StorePath = prevStorePath
	})

	return storePath
}

// symlinkResult 造一条 symlink 类型的检查结果
// performCheck 在读取存储时会先按折叠路径归一，测试里直接使用折叠形式的值，
// 与真实调用路径（result 字段原样来自存储）保持一致
func symlinkResult(device, real, fake string) output.CheckResult {
	return output.CheckResult{Type: "symlink", Device: device, Real: real, Fake: fake}
}

// TestBuildRecordEntry 校验三类型字段映射与未知类型的返回值
// 映射必须与 performCheck 读取存储字段的方式逐字对应，映射错位会导致「删不掉记录」或「删错记录」
func TestBuildRecordEntry(t *testing.T) {
	tests := []struct {
		name   string
		result output.CheckResult
		want   store.Entry
	}{
		{
			name:   "symlink 使用 real/fake",
			result: output.CheckResult{Type: "symlink", Real: "~/real.txt", Fake: "~/fake.txt"},
			want:   store.Entry{"real": "~/real.txt", "fake": "~/fake.txt"},
		},
		{
			name:   "hardlink 使用 prim/seco",
			result: output.CheckResult{Type: "hardlink", Prim: "~/prim.txt", Seco: "~/seco.txt"},
			want:   store.Entry{"prim": "~/prim.txt", "seco": "~/seco.txt"},
		},
		{
			name:   "copy 使用 src/dst",
			result: output.CheckResult{Type: "copy", Src: "~/src.txt", Dst: "~/dst.txt"},
			want:   store.Entry{"src": "~/src.txt", "dst": "~/dst.txt"},
		},
		{
			name:   "空类型返回 nil",
			result: output.CheckResult{Type: "", Real: "~/real.txt", Fake: "~/fake.txt"},
			want:   nil,
		},
		{
			name:   "未知类型返回 nil",
			result: output.CheckResult{Type: "unknown", Real: "~/real.txt", Fake: "~/fake.txt"},
			want:   nil,
		},
		{
			name:   "字段缺失时保留空值字段（仍属已知类型）",
			result: output.CheckResult{Type: "copy"},
			want:   store.Entry{"src": "", "dst": ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildRecordEntry(tt.result)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("buildRecordEntry() = %#v, 期望 %#v", got, tt.want)
			}
		})
	}
}

// TestRemoveTrackedRecordNilManager 校验全局实例为 nil 时安全跳过
// 重构前 fix 的删除分支直接使用 mgr，这条路径会 panic；unlink 已有判空，本次抽取补齐了差异
func TestRemoveTrackedRecordNilManager(t *testing.T) {
	prevManager, prevStorePath := store.Global(), store.StorePath
	store.SetGlobal(nil)
	store.StorePath = filepath.Join(t.TempDir(), "flk-store.json")
	t.Cleanup(func() {
		store.SetGlobal(prevManager)
		store.StorePath = prevStorePath
	})

	if got := removeTrackedRecord(symlinkResult("dev", "~/real.txt", "~/fake.txt")); got {
		t.Fatalf("全局实例为 nil 时 removeTrackedRecord() = true, 期望 false（安全跳过）")
	}

	// 落盘同样必须安全跳过：判空后返回 nil，命令不应因 store 不可用再报一次保存失败
	if err := saveTrackedStore(); err != nil {
		t.Fatalf("全局实例为 nil 时 saveTrackedStore() 返回错误 %v, 期望 nil", err)
	}
}

// TestRemoveTrackedRecordEmptyEntrySkips 校验空匹配键不会误删该类型下的第一条记录
// store.RemoveMatchingEntry 的匹配方式是「遍历匹配键逐字段比对」，匹配键为空时 match 恒为 true，
// 于是会删掉该类型的第一条记录；本用例锁住这层保护，防止有人把判空删掉后静默误删用户数据
func TestRemoveTrackedRecordEmptyEntrySkips(t *testing.T) {
	platform := runtime.GOOS
	data := store.RootConfig{
		platform: store.DeviceGroup{
			"dev": store.TypeGroup{
				"symlink": []store.Entry{{"real": "~/real.txt", "fake": "~/fake.txt"}},
			},
		},
	}
	withTempTrackedStore(t, data)

	for _, unknownType := range []string{"", "unknown"} {
		result := output.CheckResult{Type: unknownType, Device: "dev", Real: "~/real.txt", Fake: "~/fake.txt"}
		if got := removeTrackedRecord(result); got {
			t.Fatalf("类型 %q 的匹配键为空时 removeTrackedRecord() = true, 期望 false", unknownType)
		}
	}

	entries := store.Global().Snapshot()[platform]["dev"]["symlink"]
	if len(entries) != 1 {
		t.Fatalf("空匹配键不得改动存储, 现有 %d 条记录, 期望 1 条", len(entries))
	}
}

// TestRemoveTrackedRecordRemovesAndPersists 校验正常移除链路：内存中被选中记录消失、同组其它记录保留，
// 且只有显式调用 saveTrackedStore 才落盘（removeTrackedRecord 本身不写盘）
func TestRemoveTrackedRecordRemovesAndPersists(t *testing.T) {
	platform := runtime.GOOS
	data := store.RootConfig{
		platform: store.DeviceGroup{
			"dev": store.TypeGroup{
				"symlink": []store.Entry{
					{"real": "~/keep-real.txt", "fake": "~/keep-fake.txt"},
					{"real": "~/drop-real.txt", "fake": "~/drop-fake.txt"},
				},
			},
		},
	}
	storePath := withTempTrackedStore(t, data)

	// 只从内存移除，不落盘：文件此时必须还不存在
	if got := removeTrackedRecord(symlinkResult("dev", "~/drop-real.txt", "~/drop-fake.txt")); !got {
		t.Fatalf("removeTrackedRecord() = false, 期望 true（store 可用且匹配键非空）")
	}
	if _, err := os.Stat(storePath); !os.IsNotExist(err) {
		t.Fatalf("removeTrackedRecord 不应写盘, 但 %s 已存在 (err=%v)", storePath, err)
	}

	entries := store.Global().Snapshot()[platform]["dev"]["symlink"]
	if len(entries) != 1 {
		t.Fatalf("内存中剩余 %d 条记录, 期望 1 条", len(entries))
	}
	if entries[0]["real"] != "~/keep-real.txt" || entries[0]["fake"] != "~/keep-fake.txt" {
		t.Fatalf("内存中保留的记录不正确: %#v", entries[0])
	}

	// 显式落盘后重新读回，确认磁盘内容与内存一致（被删记录不再出现）
	if err := saveTrackedStore(); err != nil {
		t.Fatalf("saveTrackedStore() 返回错误 %v, 期望 nil", err)
	}
	reloaded, err := store.LoadFromFile(storePath)
	if err != nil {
		t.Fatalf("重新加载临时存储失败: %v", err)
	}
	reloadedEntries := reloaded.Snapshot()[platform]["dev"]["symlink"]
	if len(reloadedEntries) != 1 {
		t.Fatalf("落盘后读回 %d 条记录, 期望 1 条", len(reloadedEntries))
	}
	if reloadedEntries[0]["real"] != "~/keep-real.txt" || reloadedEntries[0]["fake"] != "~/keep-fake.txt" {
		t.Fatalf("落盘后读回的记录不正确: %#v", reloadedEntries[0])
	}
}

// TestRemoveTrackedRecordMissingRecord 校验「匹配键存在但存储中没有对应记录」时不 panic 且返回 true
// 该情形在交互式删除里真实存在：用户选择的记录可能已被并发改动，
// 语义上仍算执行过一次移除尝试（与重构前 RemoveMatchingEntry 无副作用地空转一致）
func TestRemoveTrackedRecordMissingRecord(t *testing.T) {
	platform := runtime.GOOS
	data := store.RootConfig{
		platform: store.DeviceGroup{
			"dev": store.TypeGroup{
				"symlink": []store.Entry{{"real": "~/real.txt", "fake": "~/fake.txt"}},
			},
		},
	}
	withTempTrackedStore(t, data)

	if got := removeTrackedRecord(symlinkResult("dev", "~/other-real.txt", "~/other-fake.txt")); !got {
		t.Fatalf("匹配键非空时 removeTrackedRecord() = false, 期望 true")
	}
	if entries := store.Global().Snapshot()[platform]["dev"]["symlink"]; len(entries) != 1 {
		t.Fatalf("未匹配到记录时不应改动存储, 现有 %d 条记录, 期望 1 条", len(entries))
	}
}
