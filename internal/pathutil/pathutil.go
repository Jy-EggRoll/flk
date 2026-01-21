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
	// "github.com/pterm/pterm"
)

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

func ExpandHome(path string) (string, error) { // 定义ExpandHome函数，接收字符串类型的路径参数，返回处理后的路径字符串和错误对象
	// 如果路径不以 ~ 开头，直接返回
	if !strings.HasPrefix(path, "~") { // 判断输入的路径字符串是否不以波浪号(~)开头，strings.HasPrefix用于检测字符串前缀
		return path, nil // 若路径不以~开头，直接返回原路径和nil（表示无错误）
	}

	// 获取用户主目录
	home, err := os.UserHomeDir() // 调用 os 包的 UserHomeDir 函数获取当前用户的主目录路径，返回主目录字符串和错误对象
	if err != nil {               // 判断获取用户主目录的操作是否产生错误
		return "", err // 若获取主目录出错，返回空字符串和该错误对象
	}

	// 如果只是 ~，直接返回主目录
	if path == "~" { // 判断输入的路径是否严格等于单个波浪号（~）
		return home, nil // 若路径仅为 ~，返回获取到的用户主目录和 nil（表示无错误）
	}

	// 如果是 ~/... 格式，拼接路径
	// filepath.Join 自动处理不同操作系统的路径分隔符，但是不会将路径清理到最简形态
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") { // 判断路径是否以~/（Unix/Linux/Mac系统）或~\（Windows系统）开头
		return filepath.Join(home, path[2:]), nil // 使用filepath.Join拼接主目录和~后的路径（path[2:]截取从索引2开始的子串，去掉~和分隔符），返回拼接后的路径和nil（表示无错误）
	}

	return "", err // 若以上条件都不满足（如~后接非分隔符的情况），返回空字符串和错误对象
}

func NormalizePath(path string) (string, error) { // 定义NormalizePath函数，接收字符串类型的路径参数，返回规范化后的路径字符串和错误对象
	expanded, err := ExpandHome(path) // 调用ExpandHome函数展开路径中的波浪号（~），接收展开后的路径和错误对象
	if err != nil {                   // 判断展开波浪号的操作是否产生错误
		return "", err // 若展开波浪号出错，返回空字符串和该错误对象
	}

	cleaned := filepath.Clean(expanded) // 调用filepath.Clean函数清理展开后的路径，解析路径中的.和..、合并冗余分隔符，生成最简路径

	return cleaned, nil // 返回清理后的规范化路径和nil（表示无错误）
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
