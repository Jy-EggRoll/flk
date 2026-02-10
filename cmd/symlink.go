package cmd

import (
	"fmt"
	"os"

	"github.com/jy-eggroll/flk/internal/create/symlink"
	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
	store "github.com/jy-eggroll/flk/internal/store"
	"github.com/spf13/cobra"
)

var (
	symlinkReal string
	symlinkFake string
)

var symlinkCmd = &cobra.Command{
	Use:   "symlink",
	Short: "创建符号链接（支持文件和文件夹）",
	Long:  "创建符号链接（支持文件和文件夹）",
	RunE:  Symlink,
}

func init() {
	createCmd.AddCommand(symlinkCmd)
	symlinkCmd.Flags().StringVarP(&symlinkReal, "real", "r", "", "真实文件路径")
	symlinkCmd.Flags().StringVarP(&symlinkFake, "fake", "f", "", "链接文件路径")
	symlinkCmd.Flags().BoolVar(&createForce, "force", false, "强制覆盖已存在的文件或文件夹")
	symlinkCmd.Flags().StringVar(&createDevice, "device", "", "设备名称，用于后续设备过滤检查")
	symlinkCmd.MarkFlagRequired("real")
	symlinkCmd.MarkFlagRequired("fake")
}

func Symlink(cmd *cobra.Command, args []string) error {
	logger.Init(nil)
	logger.Debug("符号链接创建函数被调用了")
	logger.Debug("真实文件路径：" + symlinkReal)
	normalizedReal, err := pathutil.NormalizePath(symlinkReal)
	if err != nil {
		logger.Debug("处理真实文件路径时出错: " + err.Error())
	} else {
		logger.Debug("经过处理的真实文件路径：" + normalizedReal)
	}
	logger.Debug("链接文件路径：" + symlinkFake)
	normalizedFake, err := pathutil.NormalizePath(symlinkFake)
	if err != nil {
		logger.Debug("处理链接文件路径时出错: " + err.Error())
	} else {
		logger.Debug("经过处理的链接文件路径：" + normalizedFake)
	}
	logger.Debug("强制覆盖选项：" + fmt.Sprint(createForce))
	logger.Debug("设备名称：" + createDevice)
	if err := symlink.Create(normalizedReal, normalizedFake, createForce); err != nil {
		logger.Error("符号链接创建失败：" + err.Error() + fmt.Sprintf("错误类型是：%T", err))
		return nil
	}
	logger.Info("符号链接创建成功")
	// 持久化数据：追加写入现有 store 数据，1:1 同步硬链接逻辑
	if store.GlobalManager == nil {
		if err := store.InitStore(store.StorePath); err != nil {
			logger.Error("初始化存储失败：" + err.Error())
		}
	}
	mgr := store.GlobalManager
	if mgr != nil {
		absFakePath, _ := pathutil.ToAbsolute(normalizedFake)
		fields := map[string]string{
			"prim": normalizedReal,
			"seco": absFakePath,
		}
		parentPath, _ := os.Getwd()
		mgr.AddRecord(createDevice, "symlink", parentPath, fields)
		if err := mgr.Save(store.StorePath); err != nil {
			logger.Error("持久化失败：" + err.Error())
		}
	}
	return nil
}
