package hardlink

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
)


// 该函数只处理创建逻辑，需要保证传入的路径一定是最正确、最简洁的，函数被调用时，应该优先处理字符串
func Create(primPath, secoPath string, force bool) error {
	logger.Init(nil)
	if _, err := os.Stat(primPath); err == nil {
		logger.Debug("primPath 对应的文件存在，允许继续执行")
	} else {
		logger.Error("primPath 对应的文件不存在，中止执行")
		return err
	}
	if force {
		logger.Info("检测到 force 选项，尝试删除已存在的链接文件或冲突的非目录文件")
		if _, err := os.Stat(secoPath); err == nil { // 文件存在
			logger.Debug("secoPath 对应的文件存在")
			if err := os.Remove(secoPath); err == nil {
				logger.Info("已成功删除 secoPath 对应的文件")
			} else {
				logger.Error("删除失败" + err.Error())
			}
		} else {
			logger.Debug("secoPath 对应的文件不存在，无需删除")
		}
		if err := pathutil.EnsureDirExists(secoPath); err != nil {
			if errors.Is(err, &pathutil.ExistsButNotDirectoryError{}) {
				if removeErr := os.Remove(filepath.Dir(secoPath)); removeErr == nil {
					logger.Info("已成功删除非目录文件")
				} else {
					logger.Error("删除非目录文件失败：" + removeErr.Error())
					return removeErr
				}
			}
		}
	}

	err := pathutil.EnsureDirExists(secoPath)
	if err != nil {
		return err
	}

	if err := os.Link(primPath, secoPath); err != nil {
		return err
	}
	return nil
}
