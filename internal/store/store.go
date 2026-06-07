package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
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

// AddRecord 添加一条链接记录，所有路径统一存储为折叠绝对路径（~ 格式）
func (m *Manager) AddRecord(device, linkType string, fields map[string]string) {
	platform := runtime.GOOS

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

	// 去重：symlink 以 fake 去重，hardlink 以 seco 去重
	var dedupField string
	switch linkType {
	case "symlink":
		dedupField = "fake"
	case "hardlink":
		dedupField = "seco"
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

	logger.Info("结构创建成功")
}

func (m *Manager) ToJSON() string {
	jsonResult, _ := json.MarshalIndent(m.Data, "", "    ")
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
	data, err := json.MarshalIndent(m.Data, "", "    ")
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
	if err := os.WriteFile(expanded, data, 0644); err != nil {
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
		return &Manager{Data: make(RootConfig)}, nil
	}

	// 先尝试新格式（3 层：platform → device → []Entry）
	var data RootConfig
	if err := json.Unmarshal(b, &data); err == nil {
		return &Manager{Data: data}, nil
	}

	// 新格式解析失败，尝试旧格式（4 层带 parentPath）并迁移
	var legacyData map[string]map[string]map[string]map[string][]Entry
	if err := json.Unmarshal(b, &legacyData); err != nil {
		return nil, fmt.Errorf("无法解析存储文件: 不支持的格式")
	}

	migratedData := migrateFromLegacy(legacyData)
	data = migratedData

	// 自动写回新格式
	manager := &Manager{Data: data}
	if saveErr := manager.Save(filePath); saveErr != nil {
		logger.Warn("自动迁移存储格式后保存失败: " + saveErr.Error())
	}

	return manager, nil
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
