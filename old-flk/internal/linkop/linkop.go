// Package linkop 提供符号链接和硬链接的操作功能
// 主要功能包括：
// 1. 创建符号链接（symlink）
// 2. 创建硬链接（hardlink）
// 3. 检查路径是否为符号链接
// 4. 检查两个文件是否为硬链接关系
// 5. 获取符号链接的目标路径
package linkop

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// CreateSymlink 创建符号链接
// realPath: 真实文件/目录的路径（链接指向的目标）
// fakePath: 符号链接的路径（要创建的链接）
// 注意：在 Windows 上创建符号链接通常需要管理员权限
func CreateSymlink(realPath, fakePath string) error {
	// 检查真实路径是否存在
	realInfo, err := os.Lstat(realPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("真实路径不存在：“%s”", realPath)
		}
		return fmt.Errorf("检查真实路径失败：%w", err)
	}

	// 检查链接路径是否已存在
	if _, err := os.Lstat(fakePath); err == nil {
		return fmt.Errorf("链接路径已存在：“%s”", fakePath)
	}

	// 创建符号链接
	// os.Symlink 在所有平台上都有实现
	// 第一个参数是目标（real），第二个参数是链接（fake）
	err = os.Symlink(realPath, fakePath)
	if err != nil {
		// 在 Windows 上，如果权限不足会返回特定错误
		if runtime.GOOS == "windows" {
			return fmt.Errorf("创建符号链接失败（请使用管理员权限）：%w", err)
		}
		return fmt.Errorf("创建符号链接失败：%w", err)
	}

	// 验证符号链接是否创建成功
	if realInfo.IsDir() {
		// 对于目录，我们只检查链接是否存在
		if _, err := os.Lstat(fakePath); err != nil {
			return fmt.Errorf("符号链接创建后验证失败：%w", err)
		}
	} else {
		// 对于文件，额外检查目标是否正确
		target, err := os.Readlink(fakePath)
		if err != nil {
			return fmt.Errorf("读取符号链接目标失败：%w", err)
		}
		// 规范化路径后比较
		targetAbs, _ := filepath.Abs(target)
		realAbs, _ := filepath.Abs(realPath)
		if targetAbs != realAbs && target != realPath {
			// 某些情况下可能使用相对路径，所以我们也接受原始路径匹配
			return fmt.Errorf("符号链接目标不匹配，期望：“%s”，实际：“%s”", realPath, target)
		}
	}

	return nil
}

// CreateHardlink 创建硬链接
// primaryPath: 主要文件路径（已存在的文件）
// secondaryPath: 次要文件路径（要创建的硬链接）
// 注意：硬链接仅支持文件，不支持目录
// 注意：硬链接要求两个文件在同一文件系统上
func CreateHardlink(primaryPath, secondaryPath string) error {
	// 检查主要文件是否存在
	primaryInfo, err := os.Lstat(primaryPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("主要文件不存在：“%s”", primaryPath)
		}
		return fmt.Errorf("检查主要文件失败：%w", err)
	}

	// 硬链接只能用于文件，不能用于目录
	if primaryInfo.IsDir() {
		return fmt.Errorf("硬链接不支持目录：“%s”", primaryPath)
	}

	// 检查次要文件路径是否已存在
	if _, err := os.Lstat(secondaryPath); err == nil {
		return fmt.Errorf("次要文件路径已存在：“%s”", secondaryPath)
	}

	// 创建硬链接
	// os.Link(oldname, newname) 创建 newname 作为 oldname 的硬链接
	err = os.Link(primaryPath, secondaryPath)
	if err != nil {
		return fmt.Errorf("创建硬链接失败：%w", err)
	}

	// 验证硬链接是否创建成功
	// 硬链接的两个文件应该有相同的 inode（在支持的文件系统上）
	if !IsHardlink(primaryPath, secondaryPath) {
		return fmt.Errorf("硬链接创建后验证失败")
	}

	return nil
}

// IsSymlink 检查路径是否为符号链接
func IsSymlink(path string) (bool, error) {
	// 使用 Lstat 而不是 Stat，因为 Stat 会跟随符号链接
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("检查路径失败：%w", err)
	}

	// 检查文件模式中的符号链接标志
	return info.Mode()&os.ModeSymlink != 0, nil
}

// IsHardlink 检查两个文件是否为硬链接关系
// 通过比较 inode 和设备号来判断
func IsHardlink(path1, path2 string) bool {
	// 获取两个文件的信息
	info1, err1 := os.Stat(path1)
	info2, err2 := os.Stat(path2)

	// 如果任一文件不存在或无法访问，返回 false
	if err1 != nil || err2 != nil {
		return false
	}

	// 使用 Go 标准库的 SameFile 方法
	// 它在各个平台上都有正确的实现
	return os.SameFile(info1, info2)
}

// GetSymlinkTarget 获取符号链接指向的目标路径
// 返回的是符号链接中存储的原始目标路径（可能是相对路径）
func GetSymlinkTarget(symlinkPath string) (string, error) {
	// 首先确认这是一个符号链接
	isSymlink, err := IsSymlink(symlinkPath)
	if err != nil {
		return "", err
	}
	if !isSymlink {
		return "", fmt.Errorf("路径不是符号链接：“%s”", symlinkPath)
	}

	// 读取符号链接的目标
	target, err := os.Readlink(symlinkPath)
	if err != nil {
		return "", fmt.Errorf("读取符号链接失败：%w", err)
	}

	return target, nil
}

// GetSymlinkTargetAbs 获取符号链接指向的目标路径（绝对路径形式）
func GetSymlinkTargetAbs(symlinkPath string) (string, error) {
	target, err := GetSymlinkTarget(symlinkPath)
	if err != nil {
		return "", err
	}

	// 如果目标是相对路径，需要相对于符号链接所在目录解析
	if !filepath.IsAbs(target) {
		symlinkDir := filepath.Dir(symlinkPath)
		target = filepath.Join(symlinkDir, target)
	}

	// 清理路径
	return filepath.Clean(target), nil
}

// PathExists 检查路径是否存在（不跟随符号链接）
func PathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// PathExistsFollowSymlink 检查路径是否存在（跟随符号链接）
func PathExistsFollowSymlink(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// RemoveLink 删除链接（符号链接或硬链接）
// 对于符号链接，只删除链接本身，不影响目标
// 对于硬链接，删除一个链接，原文件仍然存在（如果还有其他硬链接或原始文件）
func RemoveLink(linkPath string) error {
	// os.Remove 可以删除文件或符号链接
	err := os.Remove(linkPath)
	if err != nil {
		return fmt.Errorf("删除链接失败：%w", err)
	}
	return nil
}
