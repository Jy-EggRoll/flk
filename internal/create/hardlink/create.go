package hardlink

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
)

// 该函数只处理创建逻辑，需要保证传入的路径一定是最正确、最简洁的，函数被调用时，应该优先处理字符串
func Create(primPath, secoPath string, removeOpts safeop.RemoveOptions) error {
	if _, err := os.Stat(primPath); err == nil {
		logger.Debug("primPath 对应的文件存在，允许继续执行")
	} else {
		logger.Error("primPath 对应的文件不存在，中止执行")
		return err
	}

	// Windows: hardlink 不能跨盘（跨卷/跨文件系统）。提前给出明确错误，避免 --force 误删目标路径。
	if runtime.GOOS == "windows" {
		primVol := strings.ToUpper(filepath.VolumeName(primPath))
		secoVol := strings.ToUpper(filepath.VolumeName(secoPath))
		if primVol != "" && secoVol != "" && primVol != secoVol {
			return errors.New("不允许创建跨文件系统的硬链接")
		}
	}
	if _, err := os.Lstat(secoPath); err == nil { // 文件/链接/文件夹存在
		logger.Debug("secoPath 存在")
		if _, removeErr := safeop.RemoveWithConfirm(secoPath, removeOpts); removeErr != nil {
			if errors.Is(removeErr, safeop.ErrOperationCancelled) {
				logger.Info("用户取消删除 secoPath")
				return removeErr
			}
			logger.Error("删除失败 " + removeErr.Error())
			return removeErr
		}
	} else {
		logger.Debug("secoPath 不存在 " + err.Error())
	}

	if err := pathutil.EnsureDirExists(secoPath); err != nil {
		if errors.Is(err, &pathutil.ExistsButNotDirectoryError{}) {
			// secoPath 的父路径存在但不是目录（是文件或符号链接），删除它
			if _, removeErr := safeop.RemoveWithConfirm(filepath.Dir(secoPath), safeop.RemoveOptions{Force: removeOpts.Force}); removeErr != nil {
				if errors.Is(removeErr, safeop.ErrOperationCancelled) {
					logger.Info("用户取消删除 secoPath 父路径")
					return removeErr
				}
				logger.Error("删除非目录父路径失败: " + removeErr.Error())
				return removeErr
			}
			if retryErr := pathutil.EnsureDirExists(secoPath); retryErr != nil {
				return retryErr
			}
		} else {
			return err
		}
	}

	if err := os.Link(primPath, secoPath); err != nil {
		return err
	}
	return nil
}
