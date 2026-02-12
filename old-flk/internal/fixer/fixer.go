// Package fixer 提供链接修复功能
// 主要功能包括：
// 1. 自动修复无效的符号链接和硬链接
// 2. 支持交互式和非交互式（自动）修复模式
// 3. 提供修复统计和报告
//
// 使用示例：
//
//	flk fix                    # 交互式修复当前目录下的问题链接
//	flk fix --auto             # 自动修复（仅修复可自动恢复的链接）
//	flk fix --symlink          # 仅修复符号链接
//	flk fix --device laptop    # 仅修复 laptop 设备的链接
//	flk -w /path/to/project fix # 在指定目录下执行修复
package fixer

import (
	"fmt"
	"os"

	"file-link-manager/internal/interact"
	"file-link-manager/internal/linkop"
	"file-link-manager/internal/pathutil"
	"file-link-manager/internal/storage"
)

// FixMode 修复模式
type FixMode int

const (
	// FixSymlink 只修复符号链接
	FixSymlink FixMode = iota
	// FixHardlink 只修复硬链接
	FixHardlink
	// FixAll 修复所有链接
	FixAll
)

// Fixer 链接修复器
type Fixer struct {
	storage  *storage.Storage
	workDir  string
	osType   string  // 操作系统类型
	mode     FixMode // 修复模式
	device   string  // 设备名称筛选
	autoMode bool    // 是否为自动修复模式（非交互）
}

// NewFixer 创建一个新的修复器
// st: 存储实例
// osType: 操作系统类型（"windows"、"linux"、"darwin"）
// mode: 修复模式
// device: 设备筛选（空字符串表示不筛选）
// autoMode: 是否启用自动修复模式
func NewFixer(st *storage.Storage, osType string, mode FixMode, device string, autoMode bool) *Fixer {
	return &Fixer{
		storage:  st,
		workDir:  st.GetWorkDir(),
		osType:   osType,
		mode:     mode,
		device:   device,
		autoMode: autoMode,
	}
}

// FixResult 修复结果
type FixResult struct {
	SymlinksFixed   int // 已修复的符号链接数
	SymlinksFailed  int // 修复失败的符号链接数
	SymlinksDeleted int // 删除的符号链接记录数
	SymlinksSkipped int // 跳过的符号链接数

	HardlinksFixed   int // 已修复的硬链接数
	HardlinksFailed  int // 修复失败的硬链接数
	HardlinksDeleted int // 删除的硬链接记录数
	HardlinksSkipped int // 跳过的硬链接数
}

// Fix 执行修复
// 返回修复结果和可能的错误
func (f *Fixer) Fix() (*FixResult, error) {
	result := &FixResult{}

	// 根据模式修复
	if f.mode == FixSymlink || f.mode == FixAll {
		if err := f.fixSymlinks(result); err != nil {
			return nil, err
		}
	}

	if f.mode == FixHardlink || f.mode == FixAll {
		if err := f.fixHardlinks(result); err != nil {
			return nil, err
		}
	}

	return result, nil
}

// fixSymlinks 修复符号链接
func (f *Fixer) fixSymlinks(result *FixResult) error {
	records := f.storage.GetSymlinks(f.osType)
	records = f.filterSymlinksByDevice(records)

	var updatedRecords []storage.SymlinkRecord
	deletedCount := 0

	for i := 0; i < len(records); i++ {
		record := records[i]

		// 将相对路径转换为绝对路径
		realPath, err := pathutil.ToAbsolute(f.workDir, record.RealRelative)
		if err != nil {
			interact.PrintError("转换真实路径失败: %v", err)
			result.SymlinksFailed++
			updatedRecords = append(updatedRecords, record)
			continue
		}
		fakePath := record.FakeAbsolute

		// 检查链接状态
		status := f.checkSymlinkStatus(realPath, fakePath)

		if status == "ok" {
			// 链接正常，保留记录
			updatedRecords = append(updatedRecords, record)
			continue
		}

		// 处理问题链接
		action := f.handleSymlinkIssue(&record, realPath, fakePath, status, result)

		switch action {
		case "keep":
			updatedRecords = append(updatedRecords, record)
		case "delete":
			deletedCount++
		}
	}

	// 保存更新后的记录
	if deletedCount > 0 {
		result.SymlinksDeleted = deletedCount
		if err := f.storage.SetSymlinks(f.osType, updatedRecords); err != nil {
			return fmt.Errorf("更新符号链接记录失败：%w", err)
		}
		if err := f.storage.Save(); err != nil {
			return fmt.Errorf("保存符号链接记录失败：%w", err)
		}
	}

	return nil
}

// filterSymlinksByDevice 根据设备名称筛选符号链接记录
func (f *Fixer) filterSymlinksByDevice(records []storage.SymlinkRecord) []storage.SymlinkRecord {
	if f.device == "" {
		return records
	}

	var filtered []storage.SymlinkRecord
	for _, record := range records {
		if record.Device == "common" || record.Device == f.device {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

// checkSymlinkStatus 检查符号链接状态
// 返回状态码：ok, missing_real, missing_fake, not_symlink, mismatch, error
func (f *Fixer) checkSymlinkStatus(realPath, fakePath string) string {
	// 检查真实路径是否存在
	if !linkop.PathExistsFollowSymlink(realPath) {
		return "missing_real"
	}

	// 检查链接路径是否存在
	if !linkop.PathExists(fakePath) {
		return "missing_fake"
	}

	// 检查链接路径是否为符号链接
	isSymlink, err := linkop.IsSymlink(fakePath)
	if err != nil {
		return "error"
	}
	if !isSymlink {
		return "not_symlink"
	}

	// 检查符号链接目标是否正确
	target, err := linkop.GetSymlinkTargetAbs(fakePath)
	if err != nil {
		return "error"
	}

	// 规范化路径后比较
	realAbs, _ := pathutil.NormalizePath(realPath)
	targetAbs, _ := pathutil.NormalizePath(target)

	if realAbs != targetAbs {
		return "mismatch"
	}

	return "ok"
}

// handleSymlinkIssue 处理符号链接问题
// 返回值："keep"（保留记录）或 "delete"（删除记录）
func (f *Fixer) handleSymlinkIssue(record *storage.SymlinkRecord, realPath, fakePath, status string, result *FixResult) string {
	switch status {
	case "missing_real":
		// 真实路径不存在 - 无法自动修复
		interact.PrintWarning("真实路径不存在：\"%s\"", realPath)
		interact.PrintInfo("链接路径：\"%s\"", fakePath)

		if f.autoMode {
			// 自动模式下跳过
			interact.PrintInfo("自动模式：跳过（无法自动修复）")
			result.SymlinksSkipped++
			return "keep"
		}

		if interact.AskYesNo("是否删除此记录", false) {
			return "delete"
		}
		result.SymlinksSkipped++
		return "keep"

	case "missing_fake":
		// 链接路径不存在 - 可以自动重建
		interact.PrintWarning("链接路径不存在：\"%s\"", fakePath)
		interact.PrintInfo("真实路径：\"%s\"", realPath)

		if f.autoMode {
			// 自动模式下尝试重建
			if err := f.recreateSymlink(realPath, fakePath); err != nil {
				interact.PrintError("自动重建失败：%v", err)
				result.SymlinksFailed++
			} else {
				interact.PrintSuccess("符号链接已自动重建")
				result.SymlinksFixed++
			}
			return "keep"
		}

		// 交互模式
		choices := map[string]string{
			"r": "重新创建符号链接",
			"d": "删除此记录",
			"i": "忽略",
			"e": "退出",
		}
		choice := interact.AskChoice("请选择操作", choices)

		switch choice {
		case "r":
			if err := f.recreateSymlink(realPath, fakePath); err != nil {
				interact.PrintError("重新创建失败：%v", err)
				result.SymlinksFailed++
			} else {
				interact.PrintSuccess("符号链接已重新创建")
				result.SymlinksFixed++
			}
		case "d":
			return "delete"
		case "i":
			result.SymlinksSkipped++
		case "e":
			interact.PrintInfo("操作已取消")
			os.Exit(0)
		}
		return "keep"

	case "not_symlink":
		// 路径存在但不是符号链接
		interact.PrintWarning("路径存在但不是符号链接：\"%s\"", fakePath)

		if f.autoMode {
			// 自动模式下跳过（需要用户确认删除文件）
			interact.PrintInfo("自动模式：跳过（需要手动确认删除现有文件）")
			result.SymlinksSkipped++
			return "keep"
		}

		choices := map[string]string{
			"r": "删除现有文件并创建符号链接",
			"d": "删除此记录",
			"i": "忽略",
			"e": "退出",
		}
		choice := interact.AskChoice("请选择操作", choices)

		switch choice {
		case "r":
			if err := os.Remove(fakePath); err != nil {
				interact.PrintError("删除文件失败：%v", err)
				result.SymlinksFailed++
			} else if err := f.recreateSymlink(realPath, fakePath); err != nil {
				interact.PrintError("重新创建失败：%v", err)
				result.SymlinksFailed++
			} else {
				interact.PrintSuccess("符号链接已创建")
				result.SymlinksFixed++
			}
		case "d":
			return "delete"
		case "i":
			result.SymlinksSkipped++
		case "e":
			interact.PrintInfo("操作已取消")
			os.Exit(0)
		}
		return "keep"

	case "mismatch":
		// 符号链接目标不匹配 - 可以自动修复
		currentTarget, _ := linkop.GetSymlinkTargetAbs(fakePath)
		interact.PrintWarning("符号链接目标不匹配")
		interact.PrintInfo("当前目标：\"%s\"", currentTarget)
		interact.PrintInfo("期望目标：\"%s\"", realPath)

		if f.autoMode {
			// 自动模式下尝试重建
			if err := os.Remove(fakePath); err != nil {
				interact.PrintError("自动删除旧链接失败：%v", err)
				result.SymlinksFailed++
			} else if err := f.recreateSymlink(realPath, fakePath); err != nil {
				interact.PrintError("自动重建失败：%v", err)
				result.SymlinksFailed++
			} else {
				interact.PrintSuccess("符号链接已自动修复")
				result.SymlinksFixed++
			}
			return "keep"
		}

		choices := map[string]string{
			"r": "重新创建符号链接",
			"d": "删除此记录",
			"i": "忽略",
			"e": "退出",
		}
		choice := interact.AskChoice("请选择操作", choices)

		switch choice {
		case "r":
			if err := os.Remove(fakePath); err != nil {
				interact.PrintError("删除旧链接失败：%v", err)
				result.SymlinksFailed++
			} else if err := f.recreateSymlink(realPath, fakePath); err != nil {
				interact.PrintError("重新创建失败：%v", err)
				result.SymlinksFailed++
			} else {
				interact.PrintSuccess("符号链接已重新创建")
				result.SymlinksFixed++
			}
		case "d":
			return "delete"
		case "i":
			result.SymlinksSkipped++
		case "e":
			interact.PrintInfo("操作已取消")
			os.Exit(0)
		}
		return "keep"
	}

	return "keep"
}

// recreateSymlink 重新创建符号链接
func (f *Fixer) recreateSymlink(realPath, fakePath string) error {
	// 确保目标目录存在
	if err := pathutil.EnsureDirExists(fakePath); err != nil {
		return err
	}

	return linkop.CreateSymlink(realPath, fakePath)
}

// fixHardlinks 修复硬链接
func (f *Fixer) fixHardlinks(result *FixResult) error {
	records := f.storage.GetHardlinks(f.osType)
	records = f.filterHardlinksByDevice(records)

	var updatedRecords []storage.HardlinkRecord
	deletedCount := 0

	for i := 0; i < len(records); i++ {
		record := records[i]

		// 将相对路径转换为绝对路径
		primaryPath, err := pathutil.ToAbsolute(f.workDir, record.PrimaryRelative)
		if err != nil {
			interact.PrintError("转换主要文件路径失败: %v", err)
			result.HardlinksFailed++
			updatedRecords = append(updatedRecords, record)
			continue
		}
		secondaryPath := record.SecondaryAbsolute

		// 检查链接状态
		status := f.checkHardlinkStatus(primaryPath, secondaryPath)

		if status == "ok" {
			// 链接正常，保留记录
			updatedRecords = append(updatedRecords, record)
			continue
		}

		// 处理问题链接
		action := f.handleHardlinkIssue(&record, primaryPath, secondaryPath, status, result)

		switch action {
		case "keep":
			updatedRecords = append(updatedRecords, record)
		case "delete":
			deletedCount++
		}
	}

	// 保存更新后的记录
	if deletedCount > 0 {
		result.HardlinksDeleted = deletedCount
		if err := f.storage.SetHardlinks(f.osType, updatedRecords); err != nil {
			return fmt.Errorf("更新硬链接记录失败：%w", err)
		}
		if err := f.storage.Save(); err != nil {
			return fmt.Errorf("保存硬链接记录失败：%w", err)
		}
	}

	return nil
}

// filterHardlinksByDevice 根据设备名称筛选硬链接记录
func (f *Fixer) filterHardlinksByDevice(records []storage.HardlinkRecord) []storage.HardlinkRecord {
	if f.device == "" {
		return records
	}

	var filtered []storage.HardlinkRecord
	for _, record := range records {
		if record.Device == "common" || record.Device == f.device {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

// checkHardlinkStatus 检查硬链接状态
func (f *Fixer) checkHardlinkStatus(primaryPath, secondaryPath string) string {
	// 检查主要文件是否存在
	if !linkop.PathExistsFollowSymlink(primaryPath) {
		return "missing_primary"
	}

	// 检查次要文件是否存在
	if !linkop.PathExists(secondaryPath) {
		return "missing_secondary"
	}

	// 检查主要文件是否为符号链接
	isPrimarySymlink, err := linkop.IsSymlink(primaryPath)
	if err != nil {
		return "error"
	}
	if isPrimarySymlink {
		return "primary_is_symlink"
	}

	// 检查次要文件是否为符号链接
	isSecondarySymlink, err := linkop.IsSymlink(secondaryPath)
	if err != nil {
		return "error"
	}
	if isSecondarySymlink {
		return "secondary_is_symlink"
	}

	// 检查是否为硬链接关系
	if !linkop.IsHardlink(primaryPath, secondaryPath) {
		return "not_hardlink"
	}

	return "ok"
}

// handleHardlinkIssue 处理硬链接问题
// 返回值："keep"（保留记录）或 "delete"（删除记录）
func (f *Fixer) handleHardlinkIssue(record *storage.HardlinkRecord, primaryPath, secondaryPath, status string, result *FixResult) string {
	switch status {
	case "missing_primary":
		// 主要文件不存在 - 无法自动修复
		interact.PrintWarning("主要文件不存在：\"%s\"", primaryPath)
		interact.PrintInfo("次要文件：\"%s\"", secondaryPath)

		if f.autoMode {
			interact.PrintInfo("自动模式：跳过（无法自动修复）")
			result.HardlinksSkipped++
			return "keep"
		}

		if interact.AskYesNo("是否删除此记录", false) {
			return "delete"
		}
		result.HardlinksSkipped++
		return "keep"

	case "missing_secondary":
		// 次要文件不存在 - 可以自动重建
		interact.PrintWarning("次要文件不存在：\"%s\"", secondaryPath)
		interact.PrintInfo("主要文件：\"%s\"", primaryPath)

		if f.autoMode {
			if err := f.recreateHardlink(primaryPath, secondaryPath); err != nil {
				interact.PrintError("自动重建失败：%v", err)
				result.HardlinksFailed++
			} else {
				interact.PrintSuccess("硬链接已自动重建")
				result.HardlinksFixed++
			}
			return "keep"
		}

		choices := map[string]string{
			"r": "重新创建硬链接",
			"d": "删除此记录",
			"i": "忽略",
			"e": "退出",
		}
		choice := interact.AskChoice("请选择操作", choices)

		switch choice {
		case "r":
			if err := f.recreateHardlink(primaryPath, secondaryPath); err != nil {
				interact.PrintError("重新创建失败：%v", err)
				result.HardlinksFailed++
			} else {
				interact.PrintSuccess("硬链接已重新创建")
				result.HardlinksFixed++
			}
		case "d":
			return "delete"
		case "i":
			result.HardlinksSkipped++
		case "e":
			interact.PrintInfo("操作已取消")
			os.Exit(0)
		}
		return "keep"

	case "primary_is_symlink":
		// 主要文件是符号链接 - 无法自动修复
		interact.PrintWarning("主要文件是符号链接而非普通文件：\"%s\"", primaryPath)
		interact.PrintInfo("次要文件：\"%s\"", secondaryPath)

		if f.autoMode {
			interact.PrintInfo("自动模式：跳过（需要手动处理）")
			result.HardlinksSkipped++
			return "keep"
		}

		choices := map[string]string{
			"d": "删除此记录",
			"i": "忽略",
			"e": "退出",
		}
		choice := interact.AskChoice("请选择操作", choices)

		switch choice {
		case "d":
			return "delete"
		case "i":
			result.HardlinksSkipped++
		case "e":
			interact.PrintInfo("操作已取消")
			os.Exit(0)
		}
		return "keep"

	case "secondary_is_symlink":
		// 次要文件是符号链接
		interact.PrintWarning("次要文件是符号链接而非硬链接：\"%s\"", secondaryPath)
		interact.PrintInfo("主要文件：\"%s\"", primaryPath)

		if f.autoMode {
			interact.PrintInfo("自动模式：跳过（需要手动确认删除符号链接）")
			result.HardlinksSkipped++
			return "keep"
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
				result.HardlinksFailed++
			} else if err := f.recreateHardlink(primaryPath, secondaryPath); err != nil {
				interact.PrintError("重新创建硬链接失败：%v", err)
				result.HardlinksFailed++
			} else {
				interact.PrintSuccess("硬链接已创建")
				result.HardlinksFixed++
			}
		case "d":
			return "delete"
		case "i":
			result.HardlinksSkipped++
		case "e":
			interact.PrintInfo("操作已取消")
			os.Exit(0)
		}
		return "keep"

	case "not_hardlink":
		// 路径存在但不是硬链接
		interact.PrintWarning("路径存在但不是硬链接：\"%s\"", secondaryPath)

		if f.autoMode {
			interact.PrintInfo("自动模式：跳过（需要手动确认删除现有文件）")
			result.HardlinksSkipped++
			return "keep"
		}

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
				result.HardlinksFailed++
			} else if err := f.recreateHardlink(primaryPath, secondaryPath); err != nil {
				interact.PrintError("重新创建失败：%v", err)
				result.HardlinksFailed++
			} else {
				interact.PrintSuccess("硬链接已创建")
				result.HardlinksFixed++
			}
		case "d":
			return "delete"
		case "i":
			result.HardlinksSkipped++
		case "e":
			interact.PrintInfo("操作已取消")
			os.Exit(0)
		}
		return "keep"
	}

	return "keep"
}

// recreateHardlink 重新创建硬链接
func (f *Fixer) recreateHardlink(primaryPath, secondaryPath string) error {
	// 确保目标目录存在
	if err := pathutil.EnsureDirExists(secondaryPath); err != nil {
		return err
	}

	return linkop.CreateHardlink(primaryPath, secondaryPath)
}
