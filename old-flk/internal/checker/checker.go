// Package checker 提供链接检查和修复功能
// 主要功能包括：
// 1. 检查符号链接是否有效
// 2. 检查硬链接是否有效
// 3. 修复无效的链接
// 4. 返回问题链接列表供调用方处理（控制台输出或 Web 展示）
package checker

import (
	"fmt"
	"os"
	"path/filepath"

	"file-link-manager/internal/interact"
	"file-link-manager/internal/linkop"
	"file-link-manager/internal/pathutil"
	"file-link-manager/internal/storage"
)

// CheckMode 检查模式
type CheckMode int

const (
	// CheckSymlink 只检查符号链接
	CheckSymlink CheckMode = iota
	// CheckHardlink 只检查硬链接
	CheckHardlink
	// CheckAll 检查所有链接
	CheckAll
)

// Checker 链接检查器
type Checker struct {
	storage *storage.Storage
	workDir string
	logOnly bool   // 是否为仅记录模式
	osType  string // 操作系统类型
	mode    CheckMode
	device  string // 设备名称
}

// NewChecker 创建一个新的检查器
func NewChecker(st *storage.Storage, osType string, mode CheckMode, logOnly bool, device string) *Checker {
	return &Checker{
		storage: st,
		workDir: st.GetWorkDir(),
		logOnly: logOnly,
		osType:  osType,
		mode:    mode,
		device:  device,
	}
}

// CheckResult 检查结果
// 包含链接统计信息以及问题链接列表
type CheckResult struct {
	TotalSymlinks    int // 符号链接总数
	ValidSymlinks    int // 有效符号链接数
	InvalidSymlinks  int // 无效符号链接数
	TotalHardlinks   int // 硬链接总数
	ValidHardlinks   int // 有效硬链接数
	InvalidHardlinks int // 无效硬链接数

	// ProblematicSymlinks 存储有问题的符号链接记录
	// 仅在 log-only 模式下填充，用于控制台输出或 Web 展示
	ProblematicSymlinks []storage.SymlinkRecord

	// ProblematicHardlinks 存储有问题的硬链接记录
	// 仅在 log-only 模式下填充，用于控制台输出或 Web 展示
	ProblematicHardlinks []storage.HardlinkRecord
}

// Check 执行检查
func (c *Checker) Check() (*CheckResult, error) {
	result := &CheckResult{}

	// 根据模式检查
	if c.mode == CheckSymlink || c.mode == CheckAll {
		if err := c.checkSymlinks(result); err != nil {
			return nil, err
		}
	}

	if c.mode == CheckHardlink || c.mode == CheckAll {
		if err := c.checkHardlinks(result); err != nil {
			return nil, err
		}
	}

	return result, nil
}

// checkSymlinks 检查符号链接
func (c *Checker) checkSymlinks(result *CheckResult) error {
	records := c.storage.GetSymlinks(c.osType)
	records = c.filterSymlinksByDevice(records) // 根据设备筛选
	result.TotalSymlinks = len(records)

	// 如果是 log-question-only 模式，准备记录有问题的链接
	var problematicRecords []storage.SymlinkRecord

	for i := 0; i < len(records); i++ {
		record := &records[i]

		// 将相对路径转换为绝对路径
		realPath, err := pathutil.ToAbsolute(c.workDir, record.RealRelative)
		if err != nil {
			return fmt.Errorf("转换真实路径失败: %w", err)
		}
		fakePath := record.FakeAbsolute

		// 检查链接状态
		status, needsFix := c.checkSymlinkStatus(realPath, fakePath)

		if needsFix {
			result.InvalidSymlinks++

			if c.logOnly {
				// 仅记录模式：将问题链接存入结果，不写入文件
				record.Status = status
				problematicRecords = append(problematicRecords, *record)
			} else {
				// 交互模式：询问用户如何处理
				if err := c.handleSymlinkIssue(record, realPath, fakePath, status); err != nil {
					return err
				}
			}
		} else {
			result.ValidSymlinks++
		}
	}

	// 如果是 log-question-only 模式，将问题链接存入结果（不再写入文件）
	if c.logOnly && len(problematicRecords) > 0 {
		result.ProblematicSymlinks = problematicRecords
	}

	// 交互模式下，删除标记为 to_delete 的记录
	if !c.logOnly {
		var updatedRecords []storage.SymlinkRecord
		deletedCount := 0
		for _, record := range records {
			if record.Status != "to_delete" {
				updatedRecords = append(updatedRecords, record)
			} else {
				deletedCount++
			}
		}

		// 如果有记录被删除，更新并保存
		if deletedCount > 0 {
			if err := c.storage.SetSymlinks(c.osType, updatedRecords); err != nil {
				return fmt.Errorf("更新符号链接记录失败：%w", err)
			}
			if err := c.storage.Save(); err != nil {
				return fmt.Errorf("保存符号链接记录失败：%w", err)
			}
			interact.PrintSuccess("已删除 %d 条符号链接记录", deletedCount)
		}
	}

	return nil
}

// filterSymlinksByDevice 根据设备名称筛选符号链接记录
func (c *Checker) filterSymlinksByDevice(records []storage.SymlinkRecord) []storage.SymlinkRecord {
	if c.device == "" {
		return records // 如果没有指定设备，则返回所有记录
	}

	var filtered []storage.SymlinkRecord
	for _, record := range records {
		// 基于记录的 Device 字段进行筛选
		// 保留通用文件（device 为 "common"）和指定设备的文件
		if record.Device == "common" || record.Device == c.device {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

// checkSymlinkStatus 检查符号链接状态
// 返回: (状态描述, 是否需要修复)
func (c *Checker) checkSymlinkStatus(realPath, fakePath string) (string, bool) {
	// 检查真实路径是否存在
	if !linkop.PathExistsFollowSymlink(realPath) {
		return "missing_real", true
	}

	// 检查链接路径是否存在
	if !linkop.PathExists(fakePath) {
		return "missing_fake", true
	}

	// 检查链接路径是否为符号链接
	isSymlink, err := linkop.IsSymlink(fakePath)
	if err != nil {
		return "error", true
	}
	if !isSymlink {
		return "not_symlink", true
	}

	// 检查符号链接目标是否正确
	target, err := linkop.GetSymlinkTargetAbs(fakePath)
	if err != nil {
		return "error", true
	}

	// 规范化路径后比较
	realAbs, _ := filepath.Abs(realPath)
	targetAbs, _ := filepath.Abs(target)

	if realAbs != targetAbs {
		return "mismatch", true
	}

	return "ok", false
}

// handleSymlinkIssue 处理符号链接问题（交互模式）
func (c *Checker) handleSymlinkIssue(record *storage.SymlinkRecord, realPath, fakePath, status string) error {
	switch status {
	case "missing_real":
		// 真实路径不存在
		interact.PrintWarning("真实路径不存在：“%s”", realPath)
		interact.PrintInfo("链接路径：“%s”", fakePath)

		if interact.AskYesNo("是否删除此记录", false) {
			// 删除记录的逻辑将在检查完成后统一处理
			record.Status = "to_delete"
		}

	case "missing_fake":
		// 链接路径不存在
		interact.PrintWarning("链接路径不存在：“%s”", fakePath)
		interact.PrintInfo("真实路径：“%s”", realPath)

		choices := map[string]string{
			"r": "重新创建符号链接",
			"d": "删除此记录",
			"i": "忽略",
			"e": "退出",
		}
		choice := interact.AskChoice("请选择操作", choices)

		switch choice {
		case "r": // 重新创建
			if err := c.recreateSymlink(realPath, fakePath); err != nil {
				interact.PrintError("重新创建失败：%v", err)
			} else {
				interact.PrintSuccess("符号链接已重新创建")
			}
		case "d": // 删除记录
			record.Status = "to_delete"
		case "i": // 忽略
			// 不做任何操作
		case "e": // 退出
			interact.PrintInfo("操作已取消")
			os.Exit(0)
		}

	case "not_symlink":
		// 路径存在但不是符号链接
		interact.PrintWarning("路径存在但不是符号链接：“%s”", fakePath)

		choices := map[string]string{
			"r": "删除现有文件并创建符号链接",
			"d": "删除此记录",
			"i": "忽略",
			"e": "退出",
		}
		choice := interact.AskChoice("请选择操作", choices)

		switch choice {
		case "r": // 覆盖
			if err := os.Remove(fakePath); err != nil {
				interact.PrintError("删除文件失败：%v", err)
			} else if err := c.recreateSymlink(realPath, fakePath); err != nil {
				interact.PrintError("重新创建失败：%v", err)
			} else {
				interact.PrintSuccess("符号链接已创建")
			}
		case "d": // 删除记录
			record.Status = "to_delete"
		case "i": // 忽略
			// 不做任何操作
		case "e": // 退出
			interact.PrintInfo("操作已取消")
			os.Exit(0)
		}

	case "mismatch":
		// 符号链接目标不匹配
		currentTarget, _ := linkop.GetSymlinkTargetAbs(fakePath)
		interact.PrintWarning("符号链接目标不匹配")
		interact.PrintInfo("当前目标：“%s”", currentTarget)
		interact.PrintInfo("期望目标：“%s”", realPath)

		choices := map[string]string{
			"r": "重新创建符号链接",
			"d": "删除此记录",
			"i": "忽略",
			"e": "退出",
		}
		choice := interact.AskChoice("请选择操作", choices)

		switch choice {
		case "r": // 重新创建
			if err := os.Remove(fakePath); err != nil {
				interact.PrintError("删除旧链接失败：%v", err)
			} else if err := c.recreateSymlink(realPath, fakePath); err != nil {
				interact.PrintError("重新创建失败：%v", err)
			} else {
				interact.PrintSuccess("符号链接已重新创建")
			}
		case "d": // 删除记录
			record.Status = "to_delete"
		case "i": // 忽略
			// 不做任何操作
		case "e": // 退出
			interact.PrintInfo("操作已取消")
			os.Exit(0)
		}
	}

	return nil
}

// recreateSymlink 重新创建符号链接
func (c *Checker) recreateSymlink(realPath, fakePath string) error {
	// 确保目标目录存在
	if err := pathutil.EnsureDirExists(fakePath); err != nil {
		return err
	}

	return linkop.CreateSymlink(realPath, fakePath)
}

// checkHardlinks 检查硬链接
func (c *Checker) checkHardlinks(result *CheckResult) error {
	records := c.storage.GetHardlinks(c.osType)
	records = c.filterHardlinksByDevice(records) // 根据设备筛选
	result.TotalHardlinks = len(records)

	var problematicRecords []storage.HardlinkRecord

	for i := 0; i < len(records); i++ {
		record := &records[i]

		// 将相对路径转换为绝对路径
		primaryPath, err := pathutil.ToAbsolute(c.workDir, record.PrimaryRelative)
		if err != nil {
			return fmt.Errorf("转换主要文件路径失败: %w", err)
		}
		secondaryPath := record.SecondaryAbsolute

		// 检查链接状态
		status, needsFix := c.checkHardlinkStatus(primaryPath, secondaryPath)

		if needsFix {
			result.InvalidHardlinks++

			if c.logOnly {
				record.Status = status
				problematicRecords = append(problematicRecords, *record)
			} else {
				if err := c.handleHardlinkIssue(record, primaryPath, secondaryPath, status); err != nil {
					return err
				}
			}
		} else {
			result.ValidHardlinks++
		}
	}

	// 如果是 log-question-only 模式，将问题链接存入结果（不再写入文件）
	if c.logOnly && len(problematicRecords) > 0 {
		result.ProblematicHardlinks = problematicRecords
	}

	// 交互模式下，删除标记为 to_delete 的记录
	if !c.logOnly {
		var updatedRecords []storage.HardlinkRecord
		deletedCount := 0
		for _, record := range records {
			if record.Status != "to_delete" {
				updatedRecords = append(updatedRecords, record)
			} else {
				deletedCount++
			}
		}

		// 如果有记录被删除，更新并保存
		if deletedCount > 0 {
			if err := c.storage.SetHardlinks(c.osType, updatedRecords); err != nil {
				return fmt.Errorf("更新硬链接记录失败：%w", err)
			}
			if err := c.storage.Save(); err != nil {
				return fmt.Errorf("保存硬链接记录失败：%w", err)
			}
			interact.PrintSuccess("已删除 %d 条硬链接记录", deletedCount)
		}
	}

	return nil
}

// filterHardlinksByDevice 根据设备名称筛选硬链接记录
func (c *Checker) filterHardlinksByDevice(records []storage.HardlinkRecord) []storage.HardlinkRecord {
	if c.device == "" {
		return records // 如果没有指定设备，则返回所有记录
	}

	var filtered []storage.HardlinkRecord
	for _, record := range records {
		// 基于记录的 Device 字段进行筛选
		// 保留通用文件（device 为 "common"）和指定设备的文件
		if record.Device == "common" || record.Device == c.device {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

// checkHardlinkStatus 检查硬链接状态
func (c *Checker) checkHardlinkStatus(primaryPath, secondaryPath string) (string, bool) {
	// 检查主要文件是否存在
	if !linkop.PathExistsFollowSymlink(primaryPath) {
		return "missing_primary", true
	}

	// 检查次要文件是否存在
	if !linkop.PathExists(secondaryPath) {
		return "missing_secondary", true
	}

	// 检查主要文件是否为符号链接（硬链接记录中的主要文件不应该是符号链接）
	isPrimarySymlink, err := linkop.IsSymlink(primaryPath)
	if err != nil {
		return "error", true
	}
	if isPrimarySymlink {
		return "primary_is_symlink", true
	}

	// 检查次要文件是否为符号链接（硬链接记录中的次要文件不应该是符号链接）
	isSecondarySymlink, err := linkop.IsSymlink(secondaryPath)
	if err != nil {
		return "error", true
	}
	if isSecondarySymlink {
		return "secondary_is_symlink", true
	}

	// 检查是否为硬链接关系
	if !linkop.IsHardlink(primaryPath, secondaryPath) {
		return "not_hardlink", true
	}

	return "ok", false
}

// handleHardlinkIssue 处理硬链接问题（交互模式）
func (c *Checker) handleHardlinkIssue(record *storage.HardlinkRecord, primaryPath, secondaryPath, status string) error {
	switch status {
	case "missing_primary":
		interact.PrintWarning("主要文件不存在：“%s”", primaryPath)
		interact.PrintInfo("次要文件：“%s”", secondaryPath)

		if interact.AskYesNo("是否删除此记录", false) {
			record.Status = "to_delete"
		}

	case "missing_secondary":
		interact.PrintWarning("次要文件不存在：“%s”", secondaryPath)
		interact.PrintInfo("主要文件：“%s”", primaryPath)

		choices := map[string]string{
			"r": "重新创建硬链接",
			"d": "删除此记录",
			"i": "忽略",
			"e": "退出",
		}
		choice := interact.AskChoice("请选择操作", choices)

		switch choice {
		case "r":
			if err := c.recreateHardlink(primaryPath, secondaryPath); err != nil {
				interact.PrintError("重新创建失败：%v", err)
			} else {
				interact.PrintSuccess("硬链接已重新创建")
			}
		case "d":
			record.Status = "to_delete"
		case "i":
			// 忽略
		case "e":
			interact.PrintInfo("操作已取消")
			os.Exit(0)
		}

	case "primary_is_symlink":
		interact.PrintWarning("主要文件是符号链接而非普通文件：\"%s\"", primaryPath)
		interact.PrintInfo("硬链接记录的主要文件不应该是符号链接")
		interact.PrintInfo("次要文件：\"%s\"", secondaryPath)

		choices := map[string]string{
			"d": "删除此记录",
			"i": "忽略",
			"e": "退出",
		}
		choice := interact.AskChoice("请选择操作", choices)

		switch choice {
		case "d":
			record.Status = "to_delete"
		case "i":
			// 忽略
		case "e":
			interact.PrintInfo("操作已取消")
			os.Exit(0)
		}

	case "secondary_is_symlink":
		interact.PrintWarning("次要文件是符号链接而非硬链接：\"%s\"", secondaryPath)
		interact.PrintInfo("硬链接记录的次要文件不应该是符号链接")
		interact.PrintInfo("主要文件：\"%s\"", primaryPath)

		// 获取符号链接的目标
		target, err := linkop.GetSymlinkTargetAbs(secondaryPath)
		if err == nil {
			interact.PrintInfo("符号链接目标：\"%s\"", target)
		}

		choices := map[string]string{
			"r": "删除符号链接并创建硬链接",
			"d": "删除此记录",
			"i": "忽略",
			"e": "退出",
		}
		choice := interact.AskChoice("请选择操作", choices)

		switch choice {
		case "r":
			if err := os.Remove(secondaryPath); err != nil {
				interact.PrintError("删除符号链接失败：%v", err)
			} else if err := c.recreateHardlink(primaryPath, secondaryPath); err != nil {
				interact.PrintError("重新创建硬链接失败：%v", err)
			} else {
				interact.PrintSuccess("硬链接已创建")
			}
		case "d":
			record.Status = "to_delete"
		case "i":
			// 忽略
		case "e":
			interact.PrintInfo("操作已取消")
			os.Exit(0)
		}

	case "not_hardlink":
		interact.PrintWarning("路径存在但不是硬链接：“%s”", secondaryPath)

		choices := map[string]string{
			"r": "删除现有文件并创建硬链接",
			"d": "删除此记录",
			"i": "忽略",
			"e": "退出",
		}
		choice := interact.AskChoice("请选择操作", choices)

		switch choice {
		case "r":
			if err := os.Remove(secondaryPath); err != nil {
				interact.PrintError("删除文件失败：%v", err)
			} else if err := c.recreateHardlink(primaryPath, secondaryPath); err != nil {
				interact.PrintError("重新创建失败：%v", err)
			} else {
				interact.PrintSuccess("硬链接已创建")
			}
		case "d":
			record.Status = "to_delete"
		case "i":
			// 忽略
		case "e":
			interact.PrintInfo("操作已取消")
			os.Exit(0)
		}
	}

	return nil
}

// recreateHardlink 重新创建硬链接
func (c *Checker) recreateHardlink(primaryPath, secondaryPath string) error {
	// 确保目标目录存在
	if err := pathutil.EnsureDirExists(secondaryPath); err != nil {
		return err
	}

	return linkop.CreateHardlink(primaryPath, secondaryPath)
}

// GetStatusDescription 获取状态的中文描述
// 用于在 log-only 模式下向控制台输出可读的问题描述
// status: 内部状态码，如 "missing_real"、"missing_fake" 等
// 返回值：对应的中文描述字符串
func GetStatusDescription(status string) string {
	descriptions := map[string]string{
		// 符号链接状态
		"missing_real": "真实路径不存在",
		"missing_fake": "链接路径不存在",
		"not_symlink":  "路径存在但不是符号链接",
		"mismatch":     "符号链接目标不匹配",
		"error":        "检查时发生错误",
		// 硬链接状态
		"missing_primary":      "主要文件不存在",
		"missing_secondary":    "次要文件不存在",
		"primary_is_symlink":   "主要文件是符号链接而非普通文件",
		"secondary_is_symlink": "次要文件是符号链接而非硬链接",
		"not_hardlink":         "路径存在但不是硬链接",
	}

	if desc, ok := descriptions[status]; ok {
		return desc
	}
	return status // 未知状态直接返回原值
}
