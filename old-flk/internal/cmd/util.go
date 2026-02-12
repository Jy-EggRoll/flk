package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"file-link-manager/internal/interact"
	"file-link-manager/internal/pathutil"
	"file-link-manager/internal/storage"
)

// updateHomePathsForStorage 更新指定 storage 中的家目录路径
func updateHomePathsForStorage(st *storage.Storage) error {
	interact.PrintInfo("正在更新家目录路径")

	// 加载 storage
	if err := st.Load(); err != nil {
		return err
	}

	// 获取当前家目录
	currentHome, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("获取当前家目录失败：%w", err)
	}

	interact.PrintInfo("当前家目录：“%s”", currentHome)

	osType := pathutil.GetCurrentOS()
	updatedCount := 0

	// 更新符号链接记录
	symlinks := st.GetSymlinks(osType)
	for i := range symlinks {
		updated := updatePathWithHome(&symlinks[i].FakeAbsolute, currentHome, osType)
		if updated {
			updatedCount++
		}
	}
	st.SetSymlinks(osType, symlinks)

	// 更新硬链接记录
	hardlinks := st.GetHardlinks(osType)
	for i := range hardlinks {
		updated := updatePathWithHome(&hardlinks[i].SecondaryAbsolute, currentHome, osType)
		if updated {
			updatedCount++
		}
	}
	st.SetHardlinks(osType, hardlinks)

	// 保存更新
	if updatedCount > 0 {
		if err := st.Save(); err != nil {
			return err
		}
	}

	interact.PrintSuccess("家目录更新完成")
	return nil
}

// updatePathWithHome 更新路径中的家目录部分
// 返回是否进行了更新
func updatePathWithHome(path *string, currentHome, osType string) bool {
	if path == nil || *path == "" {
		return false
	}

	// 根据操作系统类型确定旧的家目录模式
	var oldHomePattern string
	switch osType {
	case "linux", "darwin":
		oldHomePattern = "/home/"
	case "windows":
		oldHomePattern = ":\\Users\\"
	default:
		return false
	}

	absPath := *path
	var updated bool

	if osType == "linux" || osType == "darwin" {
		// Linux/Mac: 查找 /home/xxx/ 并替换为当前家目录
		if len(absPath) > len(oldHomePattern) {
			idx := 0
			if absPath[idx:idx+len(oldHomePattern)] == oldHomePattern {
				// 找到 /home/ 后的第一个 /
				nextSlash := -1
				for i := idx + len(oldHomePattern); i < len(absPath); i++ {
					if absPath[i] == '/' {
						nextSlash = i
						break
					}
				}
				if nextSlash > 0 {
					// 替换家目录部分
					*path = filepath.Join(currentHome, absPath[nextSlash+1:])
					updated = true
				}
			}
		}
	} else if osType == "windows" {
		// Windows: 查找 C:\Users\xxx\ 并替换为当前家目录
		idx := -1
		for i := 0; i < len(absPath)-len(oldHomePattern); i++ {
			if absPath[i:i+len(oldHomePattern)] == oldHomePattern {
				idx = i - 1 // 包含驱动器字母
				break
			}
		}
		if idx >= 0 {
			// 找到 Users\ 后的第一个 \
			startIdx := idx + len(oldHomePattern) + 1
			nextBackslash := -1
			for i := startIdx; i < len(absPath); i++ {
				if absPath[i] == '\\' {
					nextBackslash = i
					break
				}
			}
			if nextBackslash > 0 {
				// 替换家目录部分
				*path = filepath.Join(currentHome, absPath[nextBackslash+1:])
				updated = true
			}
		}
	}

	return updated
}

// GetEffectiveWorkDir 获取有效的工作目录
// 优先使用全局 -w/--work-dir 参数指定的目录，
// 如果未指定则使用当前工作目录
// 返回值：
//   - string: 有效的工作目录绝对路径
//   - error: 获取目录失败时的错误信息
//
// 使用场景：
//   - check 命令需要知道在哪个目录下检查链接
//   - create 命令需要知道在哪个目录下保存记录
//   - 其他需要工作目录的命令
func GetEffectiveWorkDir() (string, error) {
	// 如果用户通过 -w 或 --work-dir 指定了工作目录
	if globalWorkDir != "" {
		// 将用户指定的路径转换为绝对路径
		absPath, err := filepath.Abs(globalWorkDir)
		if err != nil {
			return "", fmt.Errorf("获取工作目录绝对路径失败：%w", err)
		}

		// 验证指定的目录是否存在
		info, err := os.Stat(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("指定的工作目录不存在：\"%s\"", absPath)
			}
			return "", fmt.Errorf("访问工作目录失败：%w", err)
		}

		// 验证是否为目录
		if !info.IsDir() {
			return "", fmt.Errorf("指定的路径不是目录：\"%s\"", absPath)
		}

		return absPath, nil
	}

	// 未指定时，使用当前工作目录
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("获取当前目录失败：%w", err)
	}
	return cwd, nil
}
