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

func Create(src, dst string, force, smart bool) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Error("源文件不存在 " + src)
			return errors.New("源文件不存在: " + src)
		}
		logger.Error("获取源文件信息失败 " + err.Error())
		return err
	}

	if srcInfo.IsDir() {
		logger.Error("源文件是目录，不支持复制")
		return errors.New("源文件是目录，不支持复制")
	}

	dstInfo, err := os.Lstat(dst)
	dstExists := err == nil

	if !srcInfo.Mode().IsRegular() {
		logger.Error("源文件不是普通文件")
		return errors.New("源文件不是普通文件")
	}

	if !dstExists {
		logger.Debug("目标文件不存在")
	}

	if dstExists && dstInfo.IsDir() {
		logger.Error("目标路径是目录，不支持覆盖")
		return errors.New("目标路径是目录，不支持覆盖")
	}

	if dstExists && !smart {
		logger.Debug("目标文件存在，无 smart 模式，询问是否删除")
		if _, removeErr := safeop.RemoveWithConfirm(dst, safeop.RemoveOptions{Force: force}); removeErr != nil {
			if errors.Is(removeErr, safeop.ErrOperationCancelled) {
				logger.Info("用户取消删除目标文件")
				return removeErr
			}
			logger.Error("删除目标文件失败 " + removeErr.Error())
			return removeErr
		}
	}

	if err := pathutil.EnsureDirExists(dst); err != nil {
		if errors.Is(err, &pathutil.ExistsButNotDirectoryError{}) {
			if _, removeErr := safeop.RemoveWithConfirm(filepath.Dir(dst), safeop.RemoveOptions{Force: force}); removeErr != nil {
				if errors.Is(removeErr, safeop.ErrOperationCancelled) {
					logger.Info("用户取消删除目标父路径")
					return removeErr
				}
				logger.Error("删除非目录父路径失败 " + removeErr.Error())
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
		logger.Error("打开源文件失败 " + err.Error())
		return err
	}
	defer from.Close()

	to, err := os.Create(dst)
	if err != nil {
		logger.Error("创建目标文件失败 " + err.Error())
		return err
	}
	defer to.Close()

	if _, err := io.Copy(to, from); err != nil {
		logger.Error("复制数据失败 " + err.Error())
		os.Remove(dst)
		return err
	}

	if err := to.Close(); err != nil {
		logger.Error("关闭目标文件失败 " + err.Error())
		os.Remove(dst)
		return err
	}

	if err := os.Chmod(dst, srcInfo.Mode().Perm()); err != nil {
		logger.Warn("设置权限失败 " + err.Error())
	}

	if err := os.Chtimes(dst, srcInfo.ModTime(), srcInfo.ModTime()); err != nil {
		logger.Warn("设置时间戳失败 " + err.Error())
	}

	logger.Info("复制完成", "src", src, "dst", dst)
	return nil
}
