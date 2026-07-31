package copy

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/jy-eggroll/flk/internal/safeop"
)

// Create 复制单个普通文件，并把删除计划写入可选的 outputs[0]
// 可选 writer 让 create 命令把交互过程定向到 stderr，同时保留 fix 等非命令调用方的既有调用形式
func Create(src, dst string, force, smart bool, outputs ...io.Writer) error {
	var progressOutput io.Writer
	if len(outputs) > 0 {
		progressOutput = outputs[0]
	}

	srcInfo, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("源文件不存在: " + src)
		}
		return err
	}

	if srcInfo.IsDir() {
		return errors.New("源文件是目录，不支持复制")
	}

	dstInfo, err := os.Lstat(dst)
	dstExists := err == nil

	if !srcInfo.Mode().IsRegular() {
		return errors.New("源文件不是普通文件")
	}

	if !dstExists {
		logger.Debug("目标文件不存在")
	}

	if dstExists && dstInfo.IsDir() {
		return errors.New("目标路径是目录，不支持覆盖")
	}

	if dstExists && !smart {
		logger.Debug("目标文件存在，无 smart 模式，询问是否删除", "path", dst)
		if _, removeErr := safeop.RemoveWithConfirm(dst, safeop.RemoveOptions{Force: force, Output: progressOutput}); removeErr != nil {
			if errors.Is(removeErr, safeop.ErrOperationCancelled) {
				logger.Info("用户取消删除目标文件", "path", dst)
				return removeErr
			}
			return removeErr
		}
	}

	if err := pathutil.EnsureDirExists(dst); err != nil {
		if errors.Is(err, &pathutil.ExistsButNotDirectoryError{}) {
			// 父路径删除计划属于交互过程，必须使用调用方注入的进度流，不能混入最终结果 stdout
			parentPath := filepath.Dir(dst)
			if _, removeErr := safeop.RemoveWithConfirm(parentPath, safeop.RemoveOptions{Force: force, Output: progressOutput}); removeErr != nil {
				if errors.Is(removeErr, safeop.ErrOperationCancelled) {
					logger.Info("用户取消删除目标父路径", "path", parentPath)
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
			logger.Warn("复制失败后清理目标文件失败", "path", dst, "error", removeErr)
		}
		return err
	}

	if err := to.Close(); err != nil {
		// 关闭失败意味着写入结果不可信，尽力删除半成品；清理失败只记 warn，保留原始关闭错误
		if removeErr := os.Remove(dst); removeErr != nil {
			logger.Warn("关闭目标文件失败后清理目标文件失败", "path", dst, "error", removeErr)
		}
		return err
	}

	if err := os.Chmod(dst, srcInfo.Mode().Perm()); err != nil {
		logger.Warn("设置权限失败", "path", dst, "error", err)
	}

	if err := os.Chtimes(dst, srcInfo.ModTime(), srcInfo.ModTime()); err != nil {
		logger.Warn("设置时间戳失败", "path", dst, "error", err)
	}

	logger.Info("复制完成", "src", src, "dst", dst)
	return nil
}
