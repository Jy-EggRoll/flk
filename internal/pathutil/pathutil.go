package pathutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ValidateSafePath 检查路径是否安全删除
// 受保护目标包括：根目录、家目录本身、以及家目录的直接一级子项（如 ~/.ssh、~/.config）
// 这样做既能避免误删整个家目录子树下的系统关键目录，又允许删除用户通过 flk 管理的更深层子目录
func ValidateSafePath(path string) error {
	if path == "" {
		return errors.New("路径为空")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// 统一使用分隔符，避免 Windows 上混合分隔符导致的前缀判断失效
	absPath = filepath.Clean(absPath)

	home, herr := os.UserHomeDir()
	if herr == nil {
		homeAbs := filepath.Clean(home)
		// 家目录本身
		if absPath == homeAbs {
			return errors.New("不能删除家目录本身")
		}
		// 家目录的直接一级子项（~/xxx 或 ~\xxx）：路径段数比家目录多 1 且以家目录为前缀
		if strings.HasPrefix(absPath, homeAbs+string(os.PathSeparator)) {
			rel, relErr := filepath.Rel(homeAbs, absPath)
			if relErr == nil && !strings.ContainsRune(rel, os.PathSeparator) {
				return fmt.Errorf("不能删除家目录下的顶层目录或文件: %s", absPath)
			}
		}
	}

	// 检查根目录（跨平台统一处理，Windows 下覆盖 C:\、C:、C:\ 等变体）
	if absPath == string(os.PathSeparator) {
		return errors.New("不能删除根目录")
	}
	if runtime.GOOS == "windows" {
		// Windows 盘符根目录形如 C:\，也可能出现 C: 或尾部带多余分隔符的写法，统一归一化后判断
		if len(absPath) >= 2 && absPath[1] == ':' {
			drive := absPath[:2]
			rest := absPath[2:]
			// 去掉开头的反斜杠后应为空，才表示盘符根目录
			if rest == "" || rest == "\\" || rest == "/" {
				return fmt.Errorf("不能删除根目录: %s", drive)
			}
		}
	}

	return nil
}

var workDir string

// SetWorkDir 设置工作目录，用于相对路径解析
func SetWorkDir(dir string) {
	workDir = dir
}

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

	// 若以上条件都不满足（如~后接非分隔符的情况，例如 "~foo"），属于非法路径前缀，必须返回明确错误
	// 之前此处返回 ("", nil)，会静默得到空路径并被后续逻辑当作当前目录使用，属于严重隐患，故改为显式报错
	return "", fmt.Errorf("非法的 ~ 路径前缀: %s", path)
}

func NormalizePath(path string) (string, error) { // 定义 NormalizePath 函数，接收字符串类型的路径参数，返回规范化后的路径字符串和错误对象
	expanded, err := ExpandHome(path) // 调用 ExpandHome 函数展开路径中的波浪号（~），接收展开后的路径和错误对象
	if err != nil {                   // 判断展开波浪号的操作是否产生错误
		return "", err // 若展开波浪号出错，返回空字符串和该错误对象
	}

	// 如果路径不是绝对路径，且设置了工作目录，则相对于工作目录
	if !filepath.IsAbs(expanded) && workDir != "" {
		expanded = filepath.Join(workDir, expanded)
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
	// 使用 Lstat，这样可以检测到符号链接本身（不跟随符号链接）
	info, err := os.Lstat(dir)
	if err == nil {
		// 路径存在，检查是否为目录或符号链接
		// 如果是符号链接或存在但不是目录，返回特殊错误，调用方在 --force 模式下会处理它
		if info.Mode()&os.ModeSymlink != 0 {
			return &ExistsButNotDirectoryError{Path: dir}
		}
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

// CopyFile 复制单个文件，保留权限
func CopyFile(dst, src string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	from, err := os.Open(src)
	if err != nil {
		return err
	}
	defer from.Close()

	to, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer to.Close()

	_, err = io.Copy(to, from)
	if err != nil {
		return err
	}

	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	return os.Chmod(dst, srcInfo.Mode())
}

// CopyDir 递归复制目录，处理符号链接
func CopyDir(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}

	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(link, dst)
	}

	if err := os.MkdirAll(dst, info.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := CopyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(srcPath)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, dstPath); err != nil {
				if !errors.Is(err, os.ErrExist) {
					return err
				}
				if err := os.Remove(dstPath); err != nil {
					return err
				}
				if err := os.Symlink(link, dstPath); err != nil {
					return err
				}
			}
		} else {
			if err := CopyFile(dstPath, srcPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// Copy 智能复制（文件或目录），处理符号链接
func Copy(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}

	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(link, dst)
	}

	if info.IsDir() {
		return CopyDir(src, dst)
	}
	return CopyFile(dst, src)
}
