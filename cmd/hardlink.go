package cmd

import (
	"fmt"

	"github.com/jy-eggroll/flk/internal/create/hardlink"
	"github.com/jy-eggroll/flk/internal/logger"
	"github.com/jy-eggroll/flk/internal/pathutil"
	"github.com/spf13/cobra"
)

var (
	hardlinkPrim string
	hardlinkSeco string
)

var hardlinkCmd = &cobra.Command{
	Use:   "hardlink",
	Short: "创建硬链接（仅支持同分区文件）",
	Long:  "创建硬链接（仅支持同分区文件）",
	RunE:  Hardlink,
}

func init() {
	createCmd.AddCommand(hardlinkCmd)
	hardlinkCmd.Flags().StringVarP(&hardlinkPrim, "prim", "p", "", "主要文件路径")
	hardlinkCmd.Flags().StringVarP(&hardlinkSeco, "seco", "s", "", "次要文件路径")
	hardlinkCmd.Flags().BoolVar(&createForce, "force", false, "强制覆盖已存在的文件或文件夹")
	hardlinkCmd.Flags().StringVar(&createDevice, "device", "", "设备名称，用于后续设备过滤检查")
	hardlinkCmd.MarkFlagRequired("prim")
	hardlinkCmd.MarkFlagRequired("seco")
}

func Hardlink(cmd *cobra.Command, args []string) error {
	logger.Init(nil)
	logger.Debug("硬链接创建函数被调用了")
	logger.Debug("主要文件路径：" + hardlinkPrim)
	normalizedReal, err := pathutil.NormalizePath(hardlinkPrim)
	if err != nil {
		logger.Debug("处理主要文件路径时出错: " + err.Error())
	} else {
		logger.Debug("经过处理的主要文件路径：" + normalizedReal)
	}
	logger.Debug("次要文件路径：" + hardlinkSeco)
	normalizedFake, err := pathutil.NormalizePath(hardlinkSeco)
	if err != nil {
		logger.Debug("处理次要文件路径时出错: " + err.Error())
	} else {
		logger.Debug("经过处理的次要文件路径：" + normalizedFake)
	}
	logger.Debug("强制覆盖选项：" + fmt.Sprint(createForce))
	logger.Debug("设备名称：" + createDevice)
	if err := hardlink.Create(normalizedReal, normalizedFake, createForce); err != nil {
		logger.Error("硬链接创建失败：" + err.Error() + fmt.Sprintf("错误类型是：%T", err))
	}
	return nil
}
