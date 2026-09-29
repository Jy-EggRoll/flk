package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

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

// Manager 存储管理对象
type Manager struct {
	Data RootConfig
}

// rootConfigOrEmpty 把 nil 的 RootConfig 归一成空表，是本包「nil 不流出」的唯一收口点
// 防的是两类问题：nil map 赋值 panic（assignment to entry in nil map），以及序列化出裸 null
// （json.MarshalIndent(nil) 得到 "null"，它不是对象，前端 /api/config 取属性会出错）
// 潜在影响点：AddRecord / ToJSON / Save 都依赖它；新增任何读写 Data 的出口都应先过这里，别再各写一份判空
func rootConfigOrEmpty(rc RootConfig) RootConfig {
	if rc == nil {
		return make(RootConfig)
	}
	return rc
}

// newManagerFromData 是 LoadFromFile 所有成功路径的统一出口，保证返回的 Manager 里 Data 永不为 nil
// 关键场景：文件内容为 JSON null 时 json.Unmarshal 会成功并把 RootConfig 留成 nil map，
// 若直接把 data 塞进 Manager，调用方一 AddRecord 就 panic
// 潜在影响点：空文件、空对象、旧格式迁移结果都从这里出去，迁移分支也一并被覆盖
func newManagerFromData(data RootConfig) *Manager {
	return &Manager{Data: rootConfigOrEmpty(data)}
}

// AddRecord 添加一条链接记录，所有路径统一存储为折叠绝对路径（~ 格式）
func (m *Manager) AddRecord(device, linkType string, fields map[string]string) {
	platform := runtime.GOOS

	// 兜底：Manager 的 Data 为 nil 时，下面的 m.Data[platform] = ... 会 panic（assignment to entry in nil map）
	// LoadFromFile 已经保证不再返回 nil Data，这里防的是绕过它自行构造的使用者（例如调用方写 &Manager{}）
	// 潜在影响点：这是唯一的兜底，此处归一后 m.Data 会被就地替换成空表，后续写入和序列化都走正常路径
	m.Data = rootConfigOrEmpty(m.Data)

	if m.Data[platform] == nil {
		m.Data[platform] = make(DeviceGroup)
	}
	if m.Data[platform][device] == nil {
		m.Data[platform][device] = make(TypeGroup)
	}

	// 将所有路径字段统一存储为折叠绝对路径
	processedEntry := make(Entry)
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

	currentEntries := m.Data[platform][device][linkType]

	if dedupField != "" {
		var newEntries []Entry
		for _, e := range currentEntries {
			if e[dedupField] != processedEntry[dedupField] {
				newEntries = append(newEntries, e)
			}
		}
		m.Data[platform][device][linkType] = append(newEntries, processedEntry)
	} else {
		m.Data[platform][device][linkType] = append(currentEntries, processedEntry)
	}

	logger.Info(l10n.T("Structure created successfully", nil))
}

// ToJSON 将当前数据序列化为格式化 JSON 字符串
// 之前用 jsonResult, _ := 忽略了错误，序列化失败会静默返回空串，调用方（如 serve 的 /api/config）
// 无法区分「空数据」与「序列化失败」。现在出错时记 warn 并返回 "{}"，保证返回值始终是合法 JSON
// Data 为 nil 时也归一成空表再序列化，否则 json.MarshalIndent(nil) 会输出 "null" 这个合法但非对象的字面量，
// 前端（cmd/ui/config.html）按对象取属性会出错
func (m *Manager) ToJSON() string {
	data := rootConfigOrEmpty(m.Data)
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
	entries := m.Data[platform][device][linkType]
	for i, e := range entries {
		match := true
		for k, v := range entry {
			if e[k] != v {
				match = false
				break
			}
		}
		if match {
			m.Data[platform][device][linkType] = append(entries[:i], entries[i+1:]...)
			break
		}
	}
}

// DefaultStorePath 默认持久化存储路径
const DefaultStorePath = "~/.config/flk/flk-store.json"

// StorePath 用于 Cobra 参数绑定
var StorePath = DefaultStorePath

// GlobalManager 全局共享的 Manager 实例
var GlobalManager *Manager

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
			m = &Manager{Data: make(RootConfig)}
		} else {
			return err
		}
	}
	GlobalManager = m
	return nil
}

// Save 将数据持久化到指定文件
func (m *Manager) Save(filePath string) error {
	// Data 为 nil 时同样归一成空表：否则会把裸 null 写进存储文件，下次启动读回又是 nil Data
	// 那样本缺陷会随磁盘文件在「读入 → 写出」之间来回传递，永远清除不掉
	data := rootConfigOrEmpty(m.Data)
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
