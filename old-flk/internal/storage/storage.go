// Package storage 提供 file-link-manager-links.json 文件的读写和管理功能
// 主要功能包括：
// 1. 读取和解析 file-link-manager-links.json
// 2. 保存链接记录到 file-link-manager-links.json
// 3. 添加新的链接记录
// 4. 去重处理
package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// LinkRecord 表示一个链接记录的基本结构
// 所有记录都包含相对路径和绝对路径，便于跨环境复用

// SymlinkRecord 符号链接记录
type SymlinkRecord struct {
	// RealRelative 真实文件的相对路径（相对于 file-link-manager-links.json 所在目录）
	RealRelative string `json:"real_relative"`
	// FakeAbsolute 符号链接的绝对路径
	FakeAbsolute string `json:"fake_absolute"`
	// Device 设备标识，"common"表示通用文件，其他值表示特定设备
	Device string `json:"device"`
	// Status 仅用于检查日志，表示链接状态（正常运行时不使用此字段）
	Status string `json:"status,omitempty"`
}

// HardlinkRecord 硬链接记录
type HardlinkRecord struct {
	// PrimaryRelative 主要文件的相对路径（相对于 file-link-manager-links.json 所在目录）
	PrimaryRelative string `json:"primary_relative"`
	// SecondaryAbsolute 次要文件的绝对路径
	SecondaryAbsolute string `json:"secondary_absolute"`
	// Device 设备标识，"common"表示通用文件，其他值表示特定设备
	Device string `json:"device"`
	// Status 仅用于检查日志，表示链接状态（正常运行时不使用此字段）
	Status string `json:"status,omitempty"`
}

// LinksData 表示整个 file-link-manager-links.json 文件的结构
type LinksData struct {
	// Symlinks 按操作系统分类的符号链接记录
	// 键为操作系统名称："Windows", "Linux", "MacOS"
	Symlinks map[string][]SymlinkRecord `json:"symlinks"`
	// Hardlinks 按操作系统分类的硬链接记录
	Hardlinks map[string][]HardlinkRecord `json:"hardlinks"`
}

// Storage 提供线程安全的 file-link-manager-links.json 访问
type Storage struct {
	filePath string     // file-link-manager-links.json 文件的完整路径
	mu       sync.Mutex // 互斥锁，保护并发访问
	data     *LinksData // 内存中的数据
}

// NewStorage 创建一个新的 Storage 实例
// workDir: file-link-manager-links.json 所在的目录（通常是程序的工作目录）
func NewStorage(workDir string) *Storage {
	return &Storage{
		filePath: filepath.Join(workDir, "file-link-manager-links.json"),
		data:     nil, // 延迟加载
	}
}

// GetFilePath 返回 file-link-manager-links.json 的完整路径
func (s *Storage) GetFilePath() string {
	return s.filePath
}

// GetWorkDir 返回 file-link-manager-links.json 所在的工作目录
func (s *Storage) GetWorkDir() string {
	return filepath.Dir(s.filePath)
}

// FileExists 检查 file-link-manager-links.json 文件是否存在
func (s *Storage) FileExists() bool {
	_, err := os.Stat(s.filePath)
	return err == nil
}

// Load 从磁盘加载 file-link-manager-links.json
// 如果文件不存在，会创建一个空的数据结构
func (s *Storage) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 检查文件是否存在
	if _, err := os.Stat(s.filePath); os.IsNotExist(err) {
		// 文件不存在，初始化空数据
		s.data = &LinksData{
			Symlinks:  make(map[string][]SymlinkRecord),
			Hardlinks: make(map[string][]HardlinkRecord),
		}
		return nil
	}

	// 读取文件
	content, err := os.ReadFile(s.filePath)
	if err != nil {
		return fmt.Errorf("读取“file-link-manager-links.json”失败：%w", err)
	}

	// 解析 JSON
	var data LinksData
	if err := json.Unmarshal(content, &data); err != nil {
		return fmt.Errorf("解析“file-link-manager-links.json”失败：%w", err)
	}

	// 确保 map 不为 nil
	if data.Symlinks == nil {
		data.Symlinks = make(map[string][]SymlinkRecord)
	}
	if data.Hardlinks == nil {
		data.Hardlinks = make(map[string][]HardlinkRecord)
	}

	s.data = &data
	return nil
}

// Save 保存数据到磁盘
// 使用临时文件+重命名的方式确保原子性写入
func (s *Storage) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data == nil {
		return fmt.Errorf("数据未加载，无法保存")
	}

	// 序列化为 JSON（带缩进，便于人类阅读）
	content, err := json.MarshalIndent(s.data, "", "    ")
	if err != nil {
		return fmt.Errorf("序列化JSON失败：%w", err)
	}

	// 确保目录存在
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败：%w", err)
	}

	// 写入临时文件
	tempFile := s.filePath + ".tmp"
	if err := os.WriteFile(tempFile, content, 0644); err != nil {
		return fmt.Errorf("写入临时文件失败：%w", err)
	}

	// 原子性重命名（在大多数文件系统上，重命名是原子操作）
	if err := os.Rename(tempFile, s.filePath); err != nil {
		// 如果重命名失败，清理临时文件
		os.Remove(tempFile)
		return fmt.Errorf("重命名文件失败：%w", err)
	}

	return nil
}

// AddSymlink 添加一个符号链接记录
// osType: 操作系统类型（"Windows", "Linux", "MacOS"）
// record: 符号链接记录
func (s *Storage) AddSymlink(osType string, record SymlinkRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data == nil {
		return fmt.Errorf("数据未加载")
	}

	// 获取或创建该操作系统的记录列表
	records := s.data.Symlinks[osType]

	// 添加新记录
	records = append(records, record)

	// 更新数据
	s.data.Symlinks[osType] = records

	return nil
}

// AddHardlink 添加一个硬链接记录
// osType: 操作系统类型
// record: 硬链接记录
func (s *Storage) AddHardlink(osType string, record HardlinkRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data == nil {
		return fmt.Errorf("数据未加载")
	}

	// 获取或创建该操作系统的记录列表
	records := s.data.Hardlinks[osType]

	// 添加新记录
	records = append(records, record)

	// 更新数据
	s.data.Hardlinks[osType] = records

	return nil
}

// DeduplicateSymlinks 去除指定操作系统的符号链接记录中的重复项
// 如果 real_relative 和 fake_absolute 都相同，保留最后一个
func (s *Storage) DeduplicateSymlinks(osType string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data == nil {
		return
	}

	records := s.data.Symlinks[osType]
	if len(records) == 0 {
		return
	}

	// 使用 map 进行去重，键为 "real_relative|fake_absolute"
	// map 的插入顺序在遍历时不保证，所以我们需要两次遍历
	seen := make(map[string]int) // 值为最后一次出现的索引

	// 第一次遍历：记录每个键最后出现的位置
	for i, record := range records {
		key := record.RealRelative + "|" + record.FakeAbsolute
		seen[key] = i
	}

	// 第二次遍历：只保留最后出现的记录
	var deduplicated []SymlinkRecord
	for i, record := range records {
		key := record.RealRelative + "|" + record.FakeAbsolute
		if seen[key] == i {
			deduplicated = append(deduplicated, record)
		}
	}

	s.data.Symlinks[osType] = deduplicated
}

// DeduplicateHardlinks 去除指定操作系统的硬链接记录中的重复项
// 如果 primary_relative 和 secondary_absolute 都相同，保留最后一个
func (s *Storage) DeduplicateHardlinks(osType string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data == nil {
		return
	}

	records := s.data.Hardlinks[osType]
	if len(records) == 0 {
		return
	}

	seen := make(map[string]int)

	for i, record := range records {
		key := record.PrimaryRelative + "|" + record.SecondaryAbsolute
		seen[key] = i
	}

	var deduplicated []HardlinkRecord
	for i, record := range records {
		key := record.PrimaryRelative + "|" + record.SecondaryAbsolute
		if seen[key] == i {
			deduplicated = append(deduplicated, record)
		}
	}

	s.data.Hardlinks[osType] = deduplicated
}

// GetSymlinks 获取指定操作系统的符号链接记录（返回副本，避免外部修改）
func (s *Storage) GetSymlinks(osType string) []SymlinkRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data == nil {
		return nil
	}

	records := s.data.Symlinks[osType]
	// 返回副本
	result := make([]SymlinkRecord, len(records))
	copy(result, records)
	return result
}

// GetHardlinks 获取指定操作系统的硬链接记录（返回副本）
func (s *Storage) GetHardlinks(osType string) []HardlinkRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data == nil {
		return nil
	}

	records := s.data.Hardlinks[osType]
	// 返回副本
	result := make([]HardlinkRecord, len(records))
	copy(result, records)
	return result
}

// SetSymlinks 设置指定操作系统的符号链接记录
func (s *Storage) SetSymlinks(osType string, records []SymlinkRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data == nil {
		return fmt.Errorf("数据未加载")
	}

	s.data.Symlinks[osType] = records
	return nil
}

// SetHardlinks 设置指定操作系统的硬链接记录
func (s *Storage) SetHardlinks(osType string, records []HardlinkRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data == nil {
		return fmt.Errorf("数据未加载")
	}

	s.data.Hardlinks[osType] = records
	return nil
}
