package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/pkg/l10n"
)

// Entry 链接记录，底层为键值对映射
type Entry map[string]string

// TypeGroup 按链接类型聚合的 Entry 列表
type TypeGroup map[string][]Entry

// DeviceGroup 按设备标识聚合的 TypeGroup
type DeviceGroup map[string]TypeGroup

// RootConfig 按操作系统平台聚合的 DeviceGroup
type RootConfig map[string]DeviceGroup

// Manager 存储管理对象，内部用读写锁保护清单数据
//
// 为什么需要锁：serve 服务里同时存在 4 条并发路径访问同一份清单
//   - cmd/serve_web.go 的 POST /api/config：整体替换内存数据后落盘（Replace + Save）
//   - cmd/serve_web.go 的轮询 goroutine：每秒读磁盘文件并重新发布到全局实例（SetGlobal）
//   - cmd/serve_web.go 的 GET /api/config：读序列化结果（ToJSON）
//   - cmd/serve_web.go 的 GET /api/check → cmd/check.go 的 performCheck：遍历读取（Snapshot）
//
// 改造前 data 是裸的导出字段且无任何同步，只因写入内容是纯内存 JSON 覆盖、窗口是毫秒级才侥幸没炸；
// 一旦写入链路加入耗时操作（备份文件、等待用户确认），map 并发读写 panic 与丢更新就会成为常态
//
// 两条硬约束：
//  1. Manager 内含 sync.RWMutex，绝不可按值复制（go vet 的 copylocks 会拦截），一律以 *Manager 传递
//  2. data 不导出：外部只能经 Snapshot 拿到深拷贝，杜绝有人绕开锁直接改内部 map
type Manager struct {
	mu   sync.RWMutex
	data RootConfig
}

// New 构造一个空 Manager，等价于此前的 &Manager{Data: make(RootConfig)}
//
// 之所以提供构造函数而不是让调用方写 &Manager{}：后者得到的是 nil data，
// 虽然本包各出口都做了兜底，但「空表」才是明确的空清单语义，nil 只代表「尚未初始化」，
// 让调用方从构造期就拿到确定状态，后续都不必再猜
func New() *Manager {
	return &Manager{data: make(RootConfig)}
}

// rootConfigOrEmpty 把 nil 的 RootConfig 归一成空表，是本包「nil 不流出」的唯一收口点
// 防的是两类问题：nil map 赋值 panic（assignment to entry in nil map），以及序列化出裸 null
// （json.MarshalIndent(nil) 得到 "null"，它不是对象，前端 /api/config 取属性会出错）
// 潜在影响点：AddRecord / Replace / ToJSON / Save 都依赖它；新增任何读写 data 的出口都应先过这里，别再各写一份判空
func rootConfigOrEmpty(rc RootConfig) RootConfig {
	if rc == nil {
		return make(RootConfig)
	}
	return rc
}

// cloneRootConfig 逐层深拷贝 RootConfig，是本包唯一的「把内部数据交出去」的复制实现
//
// 为什么必须逐层新建：RootConfig → DeviceGroup → TypeGroup 都是 map，[]Entry 是切片，Entry 又是 map，
// 只复制最外层只能挡住「调用方替换整张表」，内层的 map / 切片仍与 Manager 共享同一份底层数据，
// 调用方遍历期间只要有并发写入，依然是 map 并发读写 panic
//
// 切片一律复制成非 nil 的空切片（nil 与空语义等价，但非 nil 会让调用方的 len/range 写法更省心）
// 顶层 nil 经 rootConfigOrEmpty 归一成空表，保持「nil 不流出」的既有语义
func cloneRootConfig(rc RootConfig) RootConfig {
	source := rootConfigOrEmpty(rc)
	out := make(RootConfig, len(source))
	for platform, deviceGroup := range source {
		copiedDeviceGroup := make(DeviceGroup, len(deviceGroup))
		for device, typeGroup := range deviceGroup {
			copiedTypeGroup := make(TypeGroup, len(typeGroup))
			for linkType, entries := range typeGroup {
				copiedEntries := make([]Entry, 0, len(entries))
				for _, e := range entries {
					copiedEntry := make(Entry, len(e))
					for k, v := range e {
						copiedEntry[k] = v
					}
					copiedEntries = append(copiedEntries, copiedEntry)
				}
				copiedTypeGroup[linkType] = copiedEntries
			}
			copiedDeviceGroup[device] = copiedTypeGroup
		}
		out[platform] = copiedDeviceGroup
	}
	return out
}

// newManagerFromData 是 LoadFromFile 所有成功路径的统一出口，保证返回的 Manager 里 data 永不为 nil
// 关键场景：文件内容为 JSON null 时 json.Unmarshal 会成功并把 RootConfig 留成 nil map，
// 若直接把 data 塞进 Manager，调用方一 AddRecord 就 panic
// 潜在影响点：空文件、空对象、旧格式迁移结果都从这里出去，迁移分支也一并被覆盖
func newManagerFromData(data RootConfig) *Manager {
	return &Manager{data: rootConfigOrEmpty(data)}
}

// Snapshot 返回当前清单的深拷贝，调用方可安全地遍历与改写而不影响 Manager 内部状态
//
// 这是本包唯一的对外读取出口：任何新增的读取需求都应复用它，
// 不要为了「只想看一个字段」而给 Manager 另开一个返回内部引用的出口，那等于把锁的意义作废
// 潜在影响点：深拷贝成本与清单规模成正比，调用方应一次取够而不是在循环里反复调用
func (m *Manager) Snapshot() RootConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneRootConfig(m.data)
}

// Replace 用 rc 整体替换当前清单，nil 归一成空表（语义同其他出口，避免裸 null 又从内存流回磁盘）
//
// 所有权约定：入参 rc 的所有权移交给 Manager，调用方在调用后不得再改动 rc（含其内层 map / 切片）
// 当前唯一调用方是 serve 的 POST /api/config，它刚从 JSON 反序列化出 rc 且不留引用，因此不需要再复制一遍
// 潜在影响点：若日后有人在调用后继续复用 rc，会与 Manager 内部数据互相污染，应由调用方自己先深拷贝
func (m *Manager) Replace(rc RootConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = rootConfigOrEmpty(rc)
}

// AddRecord 添加一条链接记录，所有路径统一存储为折叠绝对路径（~ 格式）
func (m *Manager) AddRecord(device, linkType string, fields map[string]string) {
	platform := runtime.GOOS

	// 将所有路径字段统一存储为折叠绝对路径
	// 归一化放在加锁之前：NormalizePath / FoldHome 是纯字符串运算，但仍要遍历入参 map，
	// 提前算好能把锁的持有窗口压缩成「读当前条目 → 去重 → 追加」这一小段，减少写锁对读请求的阻塞
	processedEntry := make(Entry, len(fields))
	for k, v := range fields {
		normalizedV, err := pathutil.NormalizePath(v)
		if err != nil {
			processedEntry[k] = v
			continue
		}
		foldedPath, err := pathutil.FoldHome(normalizedV)
		if err != nil {
			processedEntry[k] = normalizedV
		} else {
			processedEntry[k] = foldedPath
		}
	}

	m.mu.Lock()

	// 兜底：data 为 nil 时，下面的 m.data[platform] = ... 会 panic（assignment to entry in nil map）
	// LoadFromFile / New 都已保证 data 非 nil，这里防的是本包内直接写 &Manager{} 的构造方式
	// 潜在影响点：此处归一后 m.data 会被就地替换成空表，后续写入和序列化都走正常路径
	m.data = rootConfigOrEmpty(m.data)

	if m.data[platform] == nil {
		m.data[platform] = make(DeviceGroup)
	}
	if m.data[platform][device] == nil {
		m.data[platform][device] = make(TypeGroup)
	}

	// 去重：symlink 以 fake 去重，hardlink 以 seco 去重，copy 以 dst 去重
	// 未识别的 linkType 不做去重，直接追加（dedupField 留空走下面的追加分支）
	var dedupField string
	switch linkType {
	case "symlink":
		dedupField = "fake"
	case "hardlink":
		dedupField = "seco"
	case "copy":
		dedupField = "dst"
	}

	currentEntries := m.data[platform][device][linkType]

	if dedupField != "" {
		var newEntries []Entry
		for _, e := range currentEntries {
			if e[dedupField] != processedEntry[dedupField] {
				newEntries = append(newEntries, e)
			}
		}
		m.data[platform][device][linkType] = append(newEntries, processedEntry)
	} else {
		m.data[platform][device][linkType] = append(currentEntries, processedEntry)
	}

	m.mu.Unlock()

	// 日志刻意放在解锁之后：logger 落到 stderr / 文件属于可能阻塞的 I/O，
	// 在写锁内做 I/O 会把所有并发读请求一起拖住
	logger.Info(l10n.T("Structure created successfully", nil))
}

// ToJSON 将当前数据序列化为格式化 JSON 字符串
// 之前用 jsonResult, _ := 忽略了错误，序列化失败会静默返回空串，调用方（如 serve 的 /api/config）
// 无法区分「空数据」与「序列化失败」。现在出错时记 warn 并返回 "{}"，保证返回值始终是合法 JSON
// data 为 nil 时也归一成空表再序列化，否则 json.MarshalIndent(nil) 会输出 "null" 这个合法但非对象的字面量，
// 前端（cmd/ui/config.html）按对象取属性会出错
//
// 排序与序列化都作用在 Snapshot 出来的私有副本上：
//   - 一致性：Snapshot 期间的读锁把写操作挡在外面，拿到的是某一时刻的完整快照
//   - 不改内部状态：sortRootConfig 会就地改写切片的元素顺序，若在只读锁下改写内部数据，
//     「只读」就名不副实，并发读到的顺序会随序列化时机漂移
//
// 代价是内存中的条目顺序保持「插入顺序」，只有序列化结果被排序；两份顺序不同不影响任何调用方
func (m *Manager) ToJSON() string {
	data := m.Snapshot()
	sortRootConfig(data)
	jsonResult, err := json.MarshalIndent(data, "", "    ")
	if err != nil {
		logger.Warn(l10n.T("Failed to serialize the store data", nil), "error", err)
		return "{}"
	}
	return string(jsonResult)
}

// RemoveMatchingEntry 从指定平台/设备/类型中删除第一个匹配字段的 Entry
func (m *Manager) RemoveMatchingEntry(platform, device, linkType string, entry Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entries := m.data[platform][device][linkType]
	for i, e := range entries {
		match := true
		for k, v := range entry {
			if e[k] != v {
				match = false
				break
			}
		}
		if match {
			m.data[platform][device][linkType] = append(entries[:i], entries[i+1:]...)
			break
		}
	}
}

// DefaultStorePath 默认持久化存储路径
const DefaultStorePath = "~/.config/flk/flk-store.json"

// StorePath 用于 Cobra 参数绑定
var StorePath = DefaultStorePath

// 全局实例及其锁
//
// 为什么用一把独立的锁而不是「锁住某个 Manager」：全局实例本身会被整体替换
// （serve 的轮询 goroutine 会把磁盘重载结果发布回来），读写实例变量的动作必须自成临界区，
// 这与 Manager 内部的 mu 是两件事——前者保护「指针变量」，后者保护「指针指向的清单」
//
// 为什么不让调用方自己持有这把锁：包级变量的赋值点分散在初始化、serve 写路径、轮询路径三处，
// 只有把访问收成函数，才能保证每处都真的加锁，也才能在日后排查时一处看全
var (
	globalMu      sync.RWMutex
	globalManager *Manager
)

// Global 返回当前全局实例，可能为 nil（初始化失败等极端场景），调用方必须自行判空
func Global() *Manager {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalManager
}

// SetGlobal 设置全局实例，允许传 nil（少数调用方用它表达「存储不可用」）
func SetGlobal(m *Manager) {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalManager = m
}

// EnsureGlobal 返回全局实例，为 nil 时先创建空实例并发布，因此返回值保证非 nil
//
// 使用场景：serve 的 POST /api/config 在 InitStore 失败时仍要承接写入，
// 此时需要一个可用的落点而不是让服务崩掉
func EnsureGlobal() *Manager {
	globalMu.Lock()
	defer globalMu.Unlock()
	if globalManager == nil {
		globalManager = New()
	}
	return globalManager
}

// InitStore 初始化全局存储，支持自动迁移旧格式
func InitStore(storePath string) error {
	if !filepath.IsAbs(storePath) {
		var err error
		storePath, err = pathutil.NormalizePath(storePath)
		if err != nil {
			return err
		}
	}
	m, err := LoadFromFile(storePath)
	if err != nil {
		if os.IsNotExist(err) {
			// 首次运行：文件不存在不是故障，建立空清单
			m = New()
		} else {
			return err
		}
	}
	// 只在加载成功后才发布：失败时保持既有全局实例不变，避免半成品覆盖用户清单
	SetGlobal(m)
	return nil
}

// Save 将数据持久化到指定文件
func (m *Manager) Save(filePath string) error {
	// 序列化在 Snapshot 出的私有副本上进行：读锁保证拿到的是某一时刻完整一致的清单，
	// 随后的排序与 Marshal 都作用在这份副本上，既不需要长时间持锁，也不会为落盘而去改写内部顺序
	// 潜在影响点：副本为 nil data 时同样归一成空表，否则会把裸 null 写进存储文件，
	// 下次启动读回又是 nil data，本缺陷会随磁盘文件在「读入 → 写出」之间来回传递，永远清除不掉
	data := m.Snapshot()
	sortRootConfig(data)
	payload, err := json.MarshalIndent(data, "", "    ")
	if err != nil {
		return err
	}
	expanded, err := pathutil.NormalizePath(filePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(expanded), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(expanded, payload, 0644); err != nil {
		return err
	}
	return nil
}

// LoadFromFile 加载存储文件，自动检测并迁移旧格式（4 层嵌套带 parentPath）
func LoadFromFile(filePath string) (*Manager, error) {
	expanded, err := pathutil.NormalizePath(filePath)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(expanded)
	if err != nil {
		return nil, err
	}

	if len(b) == 0 {
		return newManagerFromData(nil), nil
	}

	// 先尝试新格式（3 层：platform → device → []Entry）
	// 内容为裸 null 时这里也会解析成功，data 仍是 nil，交由 newManagerFromData 归一成空表
	var data RootConfig
	if err := json.Unmarshal(b, &data); err == nil {
		return newManagerFromData(data), nil
	}

	// 新格式解析失败，尝试旧格式（4 层带 parentPath）并迁移
	var legacyData map[string]map[string]map[string]map[string][]Entry
	if err := json.Unmarshal(b, &legacyData); err != nil {
		return nil, fmt.Errorf("%s", l10n.T("Failed to parse the store file: unsupported format", nil))
	}

	migratedData := migrateFromLegacy(legacyData)

	// 自动写回新格式
	manager := newManagerFromData(migratedData)
	if saveErr := manager.Save(filePath); saveErr != nil {
		logger.Warn(l10n.T("Failed to save after automatically migrating the store format", nil), "error", saveErr)
	}

	return manager, nil
}

// sortEntrySlice 对 []Entry 按所有字段值的字典序排序
// 条目比较：提取每个 Entry 的所有值，各自升序排列后逐位比较，确保稳定可预测的输出
func sortEntrySlice(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		vi := entrySortValues(entries[i])
		vj := entrySortValues(entries[j])
		for idx := 0; idx < len(vi) && idx < len(vj); idx++ {
			if vi[idx] != vj[idx] {
				return vi[idx] < vj[idx]
			}
		}
		return len(vi) < len(vj)
	})
}

// entrySortValues 提取 Entry 中所有值并按字母序排列，用于排序比较
func entrySortValues(e Entry) []string {
	vals := make([]string, 0, len(e))
	for _, v := range e {
		vals = append(vals, v)
	}
	sort.Strings(vals)
	return vals
}

// sortRootConfig 递归遍历 RootConfig，对所有 []Entry 进行排序
//
// 注意本函数会就地改写传入的切片顺序，因此调用方必须传「自己独占的副本」
// （ToJSON / Save 传的都是 Snapshot 出来的深拷贝），不可直接传 Manager 内部数据
func sortRootConfig(rc RootConfig) {
	for _, dg := range rc {
		for _, tg := range dg {
			for _, entries := range tg {
				sortEntrySlice(entries)
			}
		}
	}
}

// migrateFromLegacy 将旧格式（4层带 parentPath）迁移到新格式（3层扁平结构）
func migrateFromLegacy(legacyData map[string]map[string]map[string]map[string][]Entry) RootConfig {
	newData := make(RootConfig)
	for platform, deviceGroup := range legacyData {
		for device, typeGroup := range deviceGroup {
			for linkType, pathGroup := range typeGroup {
				for foldedParent, entries := range pathGroup {
					// 沿用 parentPath 自身使用的分隔符，保持跨平台一致性
					sep := "/"
					if strings.Contains(foldedParent, "\\") {
						sep = "\\"
					}
					parentBase := strings.TrimRight(foldedParent, "/\\")
					for _, entry := range entries {
						newEntry := make(Entry)
						for k, v := range entry {
							if k == "real" || k == "prim" || k == "src" {
								// 旧格式中这些字段是相对于 parentPath 的相对路径，直接拼接 parentPath + 原分隔符 + v
								newEntry[k] = parentBase + sep + v
							} else {
								// fake/seco/dst 已存储为折叠绝对路径，直接保留
								newEntry[k] = v
							}
						}
						if newData[platform] == nil {
							newData[platform] = make(DeviceGroup)
						}
						if newData[platform][device] == nil {
							newData[platform][device] = make(TypeGroup)
						}
						newData[platform][device][linkType] = append(
							newData[platform][device][linkType], newEntry)
					}
				}
			}
		}
	}
	return newData
}
