// Package location 提供全局位置追踪功能
// 管理 ~/.config/flk/file-link-manager-location.json 文件
// 记录不同系统下 file-link-manager-links.json 文件的父目录路径
package location

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// LocationData 表示位置记录文件的结构
type LocationData struct {
	// Locations 按操作系统分类的路径列表
	// 键为操作系统名称："windows", "linux", "darwin"
	// 值为 file-link-manager-links.json 的父目录路径列表
	Locations map[string][]string `json:"locations"`
}

// Manager 位置管理器
type Manager struct {
	filePath string        // file-link-manager-location.json 的完整路径
	mu       sync.Mutex    // 互斥锁
	data     *LocationData // 内存中的数据
}

// NewManager 创建一个新的位置管理器
func NewManager() (*Manager, error) {
	// 获取配置目录
	configDir, err := getConfigDir()
	if err != nil {
		return nil, err
	}

	// 确保配置目录存在
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("创建配置目录失败：%w", err)
	}

	filePath := filepath.Join(configDir, "file-link-manager-location.json")

	return &Manager{
		filePath: filePath,
		data:     nil, // 延迟加载
	}, nil
}

// getConfigDir 返回配置目录路径 ~/.config/flk/
func getConfigDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取家目录失败：%w", err)
	}

	return filepath.Join(homeDir, ".config", "flk"), nil
}

// Load 从磁盘加载位置记录
func (m *Manager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 检查文件是否存在
	if _, err := os.Stat(m.filePath); os.IsNotExist(err) {
		// 文件不存在，初始化空数据
		m.data = &LocationData{
			Locations: make(map[string][]string),
		}
		return nil
	}

	// 读取文件
	content, err := os.ReadFile(m.filePath)
	if err != nil {
		return fmt.Errorf("读取位置记录文件失败：%w", err)
	}

	// 解析 JSON
	var data LocationData
	if err := json.Unmarshal(content, &data); err != nil {
		return fmt.Errorf("解析位置记录文件失败：%w", err)
	}

	// 确保 map 不为 nil
	if data.Locations == nil {
		data.Locations = make(map[string][]string)
	}

	m.data = &data
	return nil
}

// Save 保存数据到磁盘
func (m *Manager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.data == nil {
		return fmt.Errorf("数据未加载，无法保存")
	}

	// 序列化为 JSON
	content, err := json.MarshalIndent(m.data, "", "    ")
	if err != nil {
		return fmt.Errorf("序列化JSON失败：%w", err)
	}

	// 确保目录存在
	dir := filepath.Dir(m.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败：%w", err)
	}

	// 写入文件
	if err := os.WriteFile(m.filePath, content, 0644); err != nil {
		return fmt.Errorf("写入位置记录文件失败：%w", err)
	}

	return nil
}

// AddLocation 添加一个位置
// osType: 操作系统类型（"windows", "linux", "darwin"）
// path: file-link-manager-links.json 的父目录路径
func (m *Manager) AddLocation(osType, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.data == nil {
		return fmt.Errorf("数据未加载")
	}

	// 规范化路径
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("获取绝对路径失败：%w", err)
	}

	// 获取该操作系统的路径列表
	locations := m.data.Locations[osType]

	// 检查路径是否已存在
	for _, loc := range locations {
		if loc == absPath {
			// 路径已存在，不需要添加
			return nil
		}
	}

	// 添加新路径
	locations = append(locations, absPath)
	m.data.Locations[osType] = locations

	return nil
}

// GetLocations 获取指定操作系统的所有位置
func (m *Manager) GetLocations(osType string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.data == nil {
		return nil
	}

	locations := m.data.Locations[osType]
	// 返回副本以避免并发问题
	result := make([]string, len(locations))
	copy(result, locations)
	return result
}

// RemoveLocation 移除一个位置
func (m *Manager) RemoveLocation(osType, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.data == nil {
		return fmt.Errorf("数据未加载")
	}

	// 规范化路径
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("获取绝对路径失败：%w", err)
	}

	// 获取该操作系统的路径列表
	locations := m.data.Locations[osType]

	// 查找并删除路径
	newLocations := make([]string, 0, len(locations))
	for _, loc := range locations {
		if loc != absPath {
			newLocations = append(newLocations, loc)
		}
	}

	m.data.Locations[osType] = newLocations

	return nil
}
