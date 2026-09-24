package symlink

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/jy-eggroll/flk/pkg/l10n"
)

// 该函数只处理创建逻辑，需要保证传入的路径一定是最正确、最简洁的，函数被调用时，应该优先处理字符串
func Create(realPath, fakePath string, removeOpts safeop.RemoveOptions) error {
	if _, err := os.Stat(realPath); err == nil {
		logger.Debug(l10n.T("The file for realPath exists; proceeding", nil), "path", realPath)
	} else {
		// 错误由命令层渲染为唯一的 CreateResult，此处直接上抛，避免同一失败同时写入日志和结果流
		return err
	}

	if _, err := os.Lstat(fakePath); err == nil { // 文件/链接/文件夹存在
		logger.Debug(l10n.T("fakePath exists", nil), "path", fakePath)
		if _, removeErr := safeop.RemoveWithConfirm(fakePath, removeOpts); removeErr != nil {
			if errors.Is(removeErr, safeop.ErrOperationCancelled) {
				logger.Info(l10n.T("User cancelled deleting fakePath", nil), "path", fakePath)
				return removeErr
			}
			return removeErr
		}
	} else {
		logger.Debug(l10n.T("fakePath does not exist", nil), "path", fakePath, "error", err)
	}

	if err := pathutil.EnsureDirExists(fakePath); err != nil {
		if errors.Is(err, &pathutil.ExistsButNotDirectoryError{}) {
			// fakePath 的父路径存在但不是目录（是文件或符号链接），删除计划必须沿用命令层注入的 stderr
			parentPath := filepath.Dir(fakePath)
			if _, removeErr := safeop.RemoveWithConfirm(parentPath, safeop.RemoveOptions{Force: removeOpts.Force, Output: removeOpts.Output}); removeErr != nil {
				if errors.Is(removeErr, safeop.ErrOperationCancelled) {
					logger.Info(l10n.T("User cancelled deleting the parent path of fakePath", nil), "path", parentPath)
					return removeErr
				}
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
