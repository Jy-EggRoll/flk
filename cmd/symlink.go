/*
Copyright © 2026 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"fmt"

	"github.com/jy-eggroll/flk/internal/create/symlink"
	"github.com/jy-eggroll/flk/internal/logger"

	"github.com/spf13/cobra"
)

// symlinkCmd represents the symlink command
var symlinkCmd = &cobra.Command{
	Use:   "symlink",
	Short: "软链接文件或文件夹",
	Long:  "创建一个指向真实文件或文件夹的软链接",
	RunE:  Symlink,
}

var (
	symlinkReal  string
	symlinkFake  string
	createForce  bool
	createDevice string
)

func init() {
	logger.Init(nil)
	createCmd.AddCommand(symlinkCmd)
	symlinkCmd.Flags().StringVarP(&symlinkReal, "real", "r", "", "真实文件路径")
	symlinkCmd.Flags().StringVarP(&symlinkFake, "fake", "f", "", "链接文件路径")
	symlinkCmd.Flags().BoolVar(&createForce, "force", false, "强制覆盖已存在的文件或文件夹")
	symlinkCmd.Flags().StringVar(&createDevice, "device", "", "设备名称，用于后续设备过滤检查")
	symlinkCmd.MarkFlagRequired("real")
	symlinkCmd.MarkFlagRequired("fake")
}

func Symlink(cmd *cobra.Command, args []string) error {
	logger.Debug("软链接创建函数被调用了")
	logger.Debug("真实文件路径：" + symlinkReal)
	logger.Debug("链接文件路径：" + symlinkFake)
	logger.Debug("强制覆盖选项：" + fmt.Sprint(createForce))
	logger.Debug("设备名称：" + createDevice)
	symlink.Create(symlinkReal, symlinkFake)
	return nil
}
