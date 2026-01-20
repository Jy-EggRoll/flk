package symlink

import (
	// "fmt"
	// "os"
	// "path/filepath"
	// "runtime"

	// "fmt"
	"os"

	"github.com/pterm/pterm"
)

var (
	logger = pterm.DefaultLogger.WithLevel(pterm.LogLevelTrace) // 默认以 Trace 级别输出
)

func Create(realPath, fakePath string) error {
	logger.Trace("进入了 Create 函数")
	if err := os.Symlink(realPath, fakePath); err != nil {
		logger.Error("创建软链接失败：" + err.Error())
		return err
	}
	logger.Info("软链接创建成功")
	return nil
}
