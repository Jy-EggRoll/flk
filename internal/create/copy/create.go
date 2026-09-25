package copy

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
	"github.com/jy-eggroll/flk/pkg/l10n"
)

// Create 复制单个普通文件，并把删除计划写入可选的 outputs[0]
// 可选 writer 让 create 命令把交互过程定向到 stderr，同时保留 fix 等非命令调用方的既有调用形式
// noTrash 决定覆盖目标时的删除策略：为真则真实删除，为假（默认）则移入回收站
func Create(src, dst string, force, smart, noTrash bool, outputs ...io.Writer) error {
	var progressOutput io.Writer
	if len(outputs) > 0 {
		progressOutput = outputs[0]
	}

	srcInfo, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New(l10n.T("The source file does not exist: {{.Path}}", map[string]any{"Path": src}))
		}
		return err
	}

	if srcInfo.IsDir() {
		return errors.New(l10n.T("The source file is a directory; copying is not supported", nil))
	}

	dstInfo, err := os.Lstat(dst)
	dstExists := err == nil

	if !srcInfo.Mode().IsRegular() {
		return errors.New(l10n.T("The source file is not a regular file", nil))
	}

	if !dstExists {
		logger.Debug(l10n.T("The destination file does not exist", nil))
	}

	if dstExists && dstInfo.IsDir() {
		return errors.New(l10n.T("The destination path is a directory; overwriting is not supported", nil))
	}

	if dstExists && !smart {
		logger.Debug(l10n.T("The destination file exists and smart mode is off; asking whether to delete", nil), "path", dst)
		if _, removeErr := safeop.RemoveWithConfirm(dst, safeop.RemoveOptions{Force: force, NoTrash: noTrash, Output: progressOutput}); removeErr != nil {
			if errors.Is(removeErr, safeop.ErrOperationCancelled) {
				logger.Info(l10n.T("User cancelled deleting the destination file", nil), "path", dst)
				return removeErr
			}
			return removeErr
		}
	}

	if err := pathutil.EnsureDirExists(dst); err != nil {
		if errors.Is(err, &pathutil.ExistsButNotDirectoryError{}) {
			// 父路径删除计划属于交互过程，必须使用调用方注入的进度流，不能混入最终结果 stdout
			parentPath := filepath.Dir(dst)
			if _, removeErr := safeop.RemoveWithConfirm(parentPath, safeop.RemoveOptions{Force: force, NoTrash: noTrash, Output: progressOutput}); removeErr != nil {
				if errors.Is(removeErr, safeop.ErrOperationCancelled) {
					logger.Info(l10n.T("User cancelled deleting the destination parent path", nil), "path", parentPath)
					return removeErr
				}
				return removeErr
			}
			if retryErr := pathutil.EnsureDirExists(dst); retryErr != nil {
				return retryErr
			}
		} else {
			return err
		}
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

	if _, err := io.Copy(to, from); err != nil {
		// 主错误继续上抛；清理失败无法替代主错误，因此只记录 warn 供诊断
		if removeErr := os.Remove(dst); removeErr != nil {
			logger.Warn(l10n.T("Failed to clean up the destination file after the copy failed", nil), "path", dst, "error", removeErr)
		}
		return err
	}

	if err := to.Close(); err != nil {
		// 关闭失败意味着写入结果不可信，尽力删除半成品；清理失败只记 warn，保留原始关闭错误
		if removeErr := os.Remove(dst); removeErr != nil {
			logger.Warn(l10n.T("Failed to clean up the destination file after closing it failed", nil), "path", dst, "error", removeErr)
		}
		return err
	}

	if err := os.Chmod(dst, srcInfo.Mode().Perm()); err != nil {
		logger.Warn(l10n.T("Failed to set permissions", nil), "path", dst, "error", err)
	}

	if err := os.Chtimes(dst, srcInfo.ModTime(), srcInfo.ModTime()); err != nil {
		logger.Warn(l10n.T("Failed to set the timestamp", nil), "path", dst, "error", err)
	}

	logger.Info(l10n.T("Copy complete", nil), "src", src, "dst", dst)
	return nil
}
