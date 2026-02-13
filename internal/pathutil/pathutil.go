package pathutil

import (
	"fmt"
	"os"
	"path/filepath"

	// "runtime"
	"strings"
)

type ExistsButNotDirectoryError struct {
	Path string
}

func (e *ExistsButNotDirectoryError) Error() string {
	return fmt.Sprintf("路径 %s 存在但不是目录，如果使用 --force 将会删除存在的文件，并将其顶替为一个中间目录。", e.Path)
}

func (e *ExistsButNotDirectoryError) Is(target error) bool {
	_, ok := target.(*ExistsButNotDirectoryError)
	if !ok {
		return false
	}
	return true
}

// FoldHome 函数，接收原始路径字符串，返回将用户主目录替换为~的简化路径
func FoldHome(path string) (string, error) { // 定义 fold

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	normPath, _ := NormalizePath(path)
	if strings.HasPrefix(normPath, home) { // 判断传入的原始路径是否以用户主目录路径为前缀
		return strings.Replace(normPath, home, "~", 1), nil // 若路径包含主目录前缀，将第一个主目录子串替换为~后返回
	}
	return normPath, nil
}

// ExpandHome ，接收字符串类型的路径参数，返回处理后的路径字符串和错误对象
func ExpandHome(path string) (string, error) {
	// 如果路径不以 ~ 开头，直接返回
	if !strings.HasPrefix(path, "~") { // 判断输入的路径字符串是否不以波浪号(~)开头，strings.HasPrefix 用于检测字符串前缀
		return path, nil // 若路径不以~开头，直接返回原路径和 nil（表示无错误）
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
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") { // 判断路径是否以~/（Unix/Linux/Mac 系统）或~\（Windows 系统）开头
		return filepath.Join(home, path[2:]), nil // 使用 filepath.Join 拼接主目录和~后的路径（path[2:]截取从索引 2 开始的子串，去掉~和分隔符），返回拼接后的路径和 nil（表示无错误）
	}

	return "", err // 若以上条件都不满足（如~后接非分隔符的情况），返回空字符串和错误对象
}

func NormalizePath(path string) (string, error) { // 定义 NormalizePath 函数，接收字符串类型的路径参数，返回规范化后的路径字符串和错误对象
	expanded, err := ExpandHome(path) // 调用 ExpandHome 函数展开路径中的波浪号（~），接收展开后的路径和错误对象
	if err != nil {                   // 判断展开波浪号的操作是否产生错误
		return "", err // 若展开波浪号出错，返回空字符串和该错误对象
	}

	cleaned := filepath.Clean(expanded) // 调用 filepath.Clean 函数清理展开后的路径，解析路径中的.和..、合并冗余分隔符，生成最简路径

	return cleaned, nil // 返回清理后的规范化路径和 nil
}

func ToAbsolute(normalizePath string) (string, error) {
	absPath, err := filepath.Abs(normalizePath)
	if err != nil {
		return "", err
	}
	return absPath, nil
}

// EnsureDirExists 确保目录存在，如果不存在则创建
func EnsureDirExists(path string) error {
	// 获取目录路径（如果 path 是文件路径，则获取其父目录）
	dir := filepath.Dir(path)

	// 检查目录是否已存在
	info, err := os.Stat(dir)
	if err == nil {
		// 路径存在，检查是否为目录
		if !info.IsDir() {
			return &ExistsButNotDirectoryError{Path: dir}
		}
		return nil
	}

	// 目录不存在，创建目录（包括所有必要的父目录）
	// 0755 权限：所有者可读写执行，组和其他用户可读执行，属于泛用权限
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return nil
}
