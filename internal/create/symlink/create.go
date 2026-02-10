package symlink

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
)

/*
该函数只处理创建逻辑，需要保证传入的路径一定是最正确、最简洁的，函数被调用时，应该优先处理字符串

realPath: 真实文件路径（请保证形式标准）

fakePath: 链接文件路径（请保证形式标准）

force: 是否强制覆盖
*/
func Create(realPath, fakePath string, force bool) error {
	logger.Init(nil)
	logger.Debug("进入了 Symlink 的 Create 函数")
	if _, err := os.Stat(realPath); err == nil {
		logger.Debug("realPath 对应的文件存在，允许继续执行")
	} else {
		logger.Error("realPath 对应的文件不存在，中止执行")
		return err
	}
	if force {
		logger.Info("检测到 force 选项，尝试删除已存在的链接文件或冲突的非目录文件")
		if _, err := os.Stat(fakePath); err == nil { // 文件存在
			logger.Debug("fakePath 对应的文件存在")
			if err := os.Remove(fakePath); err == nil {
				logger.Info("已成功删除 fakePath 对应的文件")
			} else {
				logger.Error("删除失败" + err.Error())
			}
		} else {
			logger.Debug("fakePath 对应的文件不存在，无需删除")
		}
		if err := pathutil.EnsureDirExists(fakePath); err != nil {
			if errors.Is(err, &pathutil.ExistsButNotDirectoryError{}) {
				if removeErr := os.Remove(filepath.Dir(fakePath)); removeErr == nil {
					logger.Info("已成功删除非目录文件")
				} else {
					logger.Error("删除非目录文件失败：" + removeErr.Error())
					return removeErr
				}
			}
		}
	}

	err := pathutil.EnsureDirExists(fakePath)
	if err != nil {
		return err
	}

	if err := os.Symlink(realPath, fakePath); err != nil {
		return err
	}
	return nil
}
