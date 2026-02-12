// Package pathutil 提供跨平台的路径处理工具
// 主要功能包括：
// 1. 波浪号（~）展开为用户主目录
// 2. 相对路径与绝对路径转换
// 3. 路径规范化处理
package pathutil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ExpandHome 将路径中的 ~ 展开为用户主目录
// 例如: "~/documents" -> "/home/username/documents" (Linux)
//
//	"~/documents" -> "C:\Users\username\documents" (Windows)
func ExpandHome(path string) (string, error) {
	// 如果路径不以 ~ 开头，直接返回
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}

	// 获取用户主目录
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法获取用户主目录: %w", err)
	}

	// 如果只是 ~，直接返回主目录
	if path == "~" {
		return home, nil
	}

	// 如果是 ~/... 格式，拼接路径
	// 注意：这里使用 filepath.Join 自动处理不同操作系统的路径分隔符
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		return filepath.Join(home, path[2:]), nil
	}

	// 其他情况（如 ~username）暂不支持
	return "", fmt.Errorf("不支持的路径格式: %s", path)
}

// ToAbsolute 将路径转换为绝对路径
// basePath: 基准路径（通常是当前工作目录或 file-link-manager-links.json 所在目录）
// targetPath: 目标路径（可能是相对路径或绝对路径）
func ToAbsolute(basePath, targetPath string) (string, error) {
	// 首先展开波浪号
	expanded, err := ExpandHome(targetPath)
	if err != nil {
		return "", err
	}

	// 如果已经是绝对路径，直接返回规范化后的路径
	if filepath.IsAbs(expanded) {
		return filepath.Clean(expanded), nil
	}

	// 否则，相对于 basePath 进行拼接
	// filepath.Join 会自动处理路径分隔符
	absPath := filepath.Join(basePath, expanded)
	return filepath.Clean(absPath), nil
}

// ToRelative 将绝对路径转换为相对于 basePath 的相对路径
// basePath: 基准路径（通常是 file-link-manager-links.json 所在目录）
// targetPath: 目标绝对路径
func ToRelative(basePath, targetPath string) (string, error) {
	// 首先展开可能存在的波浪号
	expandedBase, err := ExpandHome(basePath)
	if err != nil {
		return "", err
	}
	expandedTarget, err := ExpandHome(targetPath)
	if err != nil {
		return "", err
	}

	// 确保两个路径都是绝对路径
	if !filepath.IsAbs(expandedBase) {
		expandedBase, err = filepath.Abs(expandedBase)
		if err != nil {
			return "", fmt.Errorf("无法获取绝对路径 %s: %w", expandedBase, err)
		}
	}
	if !filepath.IsAbs(expandedTarget) {
		expandedTarget, err = filepath.Abs(expandedTarget)
		if err != nil {
			return "", fmt.Errorf("无法获取绝对路径 %s: %w", expandedTarget, err)
		}
	}

	// 计算相对路径
	relPath, err := filepath.Rel(expandedBase, expandedTarget)
	if err != nil {
		return "", fmt.Errorf("无法计算相对路径: %w", err)
	}

	// 在 Windows 上，filepath.Rel 可能返回带反斜杠的路径
	// 为了跨平台一致性，我们统一使用正斜杠存储在 JSON 中
	return filepath.ToSlash(relPath), nil
}

// NormalizePath 规范化路径
// 主要功能：
// 1. 展开波浪号
// 2. 清理路径（去除冗余的 . 和 ..）
// 3. 转换为当前操作系统的路径分隔符
func NormalizePath(path string) (string, error) {
	// 展开波浪号
	expanded, err := ExpandHome(path)
	if err != nil {
		return "", err
	}

	// 清理路径
	cleaned := filepath.Clean(expanded)

	return cleaned, nil
}

// GetCurrentOS 返回当前操作系统类型
// 返回值直接使用 runtime.GOOS 的值：
// "windows", "linux", "darwin" 等
func GetCurrentOS() string {
	return runtime.GOOS
}

// EnsureDirExists 确保目录存在，如果不存在则创建
// 这个函数在创建链接前很有用，确保目标目录存在
func EnsureDirExists(path string) error {
	// 获取目录路径（如果 path 是文件路径，则获取其父目录）
	dir := filepath.Dir(path)

	// 检查目录是否已存在
	info, err := os.Stat(dir)
	if err == nil {
		// 路径存在，检查是否为目录
		if !info.IsDir() {
			return fmt.Errorf("路径存在但不是目录: %s", dir)
		}
		return nil
	}

	// 如果错误不是"不存在"，则返回错误
	if !os.IsNotExist(err) {
		return fmt.Errorf("检查目录失败: %w", err)
	}

	// 目录不存在，创建目录（包括所有必要的父目录）
	// 0755 权限：所有者可读写执行，组和其他用户可读执行
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败：%w", err)
	}

	return nil
}
