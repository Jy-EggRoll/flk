package symlink

import (
	// "fmt"
	// "os"
	// "path/filepath"
	// "runtime"

	// "fmt"
	"os"

	"github.com/jy-eggroll/flk/internal/logger"
)

func Create(realPath, fakePath string, force bool) error { //该该函数只处理创建逻辑，需要保证传入的
	logger.Init(nil)
	logger.Debug("进入了 Create 函数")
	if force {
		logger.Debug("检测到 force 选项，尝试删除已存在的链接文件")
	}
	if err := os.Symlink(realPath, fakePath); err != nil {
		logger.Error("创建软链接失败：" + err.Error())
		return err
	}
	logger.Info("软链接创建成功")
	return nil
}
