package symlink

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
)

// 该函数只处理创建逻辑，需要保证传入的路径一定是最正确、最简洁的，函数被调用时，应该优先处理字符串
func Create(realPath, fakePath string, removeOpts safeop.RemoveOptions) error {
	if _, err := os.Stat(realPath); err == nil {
		logger.Debug("realPath 对应的文件存在，允许继续执行")
	} else {
		logger.Error("realPath 对应的文件不存在，中止执行")
		return err
	}

	if _, err := os.Lstat(fakePath); err == nil { // 文件/链接/文件夹存在
		logger.Debug("fakePath 存在")
		if _, removeErr := safeop.RemoveWithConfirm(fakePath, removeOpts); removeErr != nil {
			if errors.Is(removeErr, safeop.ErrOperationCancelled) {
				logger.Info("用户取消删除 fakePath")
				return removeErr
			}
			logger.Error("删除失败 " + removeErr.Error())
			return removeErr
		}
	} else {
		logger.Debug("fakePath 不存在 " + err.Error())
	}

	if err := pathutil.EnsureDirExists(fakePath); err != nil {
		if errors.Is(err, &pathutil.ExistsButNotDirectoryError{}) {
			// fakePath 的父路径存在但不是目录（是文件或符号链接），删除它
			if _, removeErr := safeop.RemoveWithConfirm(filepath.Dir(fakePath), safeop.RemoveOptions{Force: removeOpts.Force}); removeErr != nil {
				if errors.Is(removeErr, safeop.ErrOperationCancelled) {
					logger.Info("用户取消删除 fakePath 父路径")
					return removeErr
				}
				logger.Error("删除非目录父路径失败 " + removeErr.Error())
				return removeErr
			}
			if retryErr := pathutil.EnsureDirExists(fakePath); retryErr != nil {
				return retryErr
			}
		} else {
			return err
		}
	}

	absRealPath, err := filepath.Abs(realPath)
	if err != nil {
		return err
	}

	if err := os.Symlink(absRealPath, fakePath); err != nil {
		return err
	}
	return nil
}
