package cmd

import (
	"fmt"
	"os"
	"runtime"

	"github.com/jy-eggroll/flk/internal/updater"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

var upgradeCmd = &cobra.Command{
	Use:     "upgrade",
	Aliases: []string{"update", "up"},
	Short:   "检查并升级到最新版本",
	Long:    "检查并升级 flk 到最新版本",
	RunE:    runUpgrade,
}

func init() {
	upgradeCmd.Flags().Bool("check", false, "仅检查版本，不升级")
	upgradeCmd.Flags().Bool("force", false, "强制升级")
	upgradeCmd.Flags().Bool("dev", false, "检查开发版本")
	rootCmd.AddCommand(upgradeCmd)
}

// runUpgrade 完成版本检查、用户确认和升级程序启动，所有用户状态都写入 Cobra 的 stderr
// 业务失败只返回带上下文的未渲染错误，由根命令统一打印一次并决定退出码，避免命令内部 os.Exit 绕过生命周期
func runUpgrade(cmd *cobra.Command, args []string) error {
	errOut := cmd.ErrOrStderr()

	// GetBool 读取 flag 的真实布尔值，避免 --check=false 等显式 false 被 Flags().Changed 误判为启用
	// 任一读取失败都必须沿 RunE 返回，不能以零值继续执行并掩盖命令定义或测试注入错误
	checkOnly, err := cmd.Flags().GetBool("check")
	if err != nil {
		return fmt.Errorf("读取 --check 参数失败: %w", err)
	}
	forceUpdate, err := cmd.Flags().GetBool("force")
	if err != nil {
		return fmt.Errorf("读取 --force 参数失败: %w", err)
	}
	checkDev, err := cmd.Flags().GetBool("dev")
	if err != nil {
		return fmt.Errorf("读取 --dev 参数失败: %w", err)
	}

	channel := "正式版"
	if checkDev {
		channel = "开发版"
	}

	goos := runtime.GOOS
	goarch := runtime.GOARCH
	platform := fmt.Sprintf("%s-%s", goos, goarch)
	if goos == "windows" {
		platform += " (exe)"
	}

	pterm.Info.WithWriter(errOut).Println("正在检查更新 (" + channel + ")...")
	pterm.Info.WithWriter(errOut).Printf("当前平台: %s\n", platform)

	info, err := updater.CheckForUpdate(Version, BuildTime, checkDev)
	if err != nil {
		return fmt.Errorf("检查更新失败: %w", err)
	}

	if info == nil {
		pterm.Success.WithWriter(errOut).Println("当前已是最新版本")
		return nil
	}

	pterm.Info.WithWriter(errOut).Printf("当前版本: %s (构建: %s)\n", info.CurrentVersion, info.CurrentBuildTime)
	pterm.Info.WithWriter(errOut).Printf("最新版本: %s\n", info.LatestVersion)

	if checkOnly {
		return nil
	}

	if !forceUpdate {
		// 根生命周期已将 pterm 默认输出设置为当前命令的 stderr，交互组件必须沿用该设置以正确绘制和读取终端提示
		confirm, err := pterm.DefaultInteractiveConfirm.Show(
			fmt.Sprintf("是否升级到 %s", info.LatestVersion),
		)
		if err != nil {
			return fmt.Errorf("读取升级确认失败: %w", err)
		}
		if !confirm {
			pterm.Info.WithWriter(errOut).Println("已取消升级")
			return nil
		}
	}

	pterm.Info.WithWriter(errOut).Println("开始升级...")

	// 下载器接收同一个 stderr writer，使认证提示、下载进度和替换脚本状态都服从 Cobra 的输出注入
	if err := updater.DownloadAndReplace(info.DownloadURL, info.AssetName, os.TempDir(), errOut); err != nil {
		return fmt.Errorf("升级失败: %w", err)
	}
	return nil
}
