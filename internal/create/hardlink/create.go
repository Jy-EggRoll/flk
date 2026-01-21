package hardlink

import (
	"os"

	"github.com/jy-eggroll/flk/internal/logger"
)

/*
该函数只处理创建逻辑，需要保证传入的路径一定是最正确、最简洁的，函数被调用时，应该优先处理字符串

primPath: 主要文件路径（请保证形式标准）

secoPath: 次要文件路径（请保证形式标准）

force: 是否强制覆盖
*/
func Create(primPath, secoPath string, force bool) error {
	logger.Init(nil)
	logger.Debug("进入了 Create 函数")
	if _,err := os.Stat(primPath); err == nil {
		logger.Debug("primPath 对应的文件存在，允许继续执行")
	} else {
		logger.Error("primPath 对应的文件不存在，中止执行")
		return err
	}
	if force {
		logger.Info("检测到 force 选项，尝试删除已存在的链接文件")
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
	}
	if err := os.Link(primPath, secoPath); err != nil {
		logger.Error("硬链接创建失败：" + err.Error())
		return err
	}

	logger.Info("硬链接创建成功")
	return nil
}