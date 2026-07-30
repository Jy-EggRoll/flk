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
	Run:     runUpgrade,
}

func init() {
	upgradeCmd.Flags().Bool("check", false, "仅检查版本，不升级")
	upgradeCmd.Flags().Bool("force", false, "强制升级")
	upgradeCmd.Flags().Bool("dev", false, "检查开发版本")
	rootCmd.AddCommand(upgradeCmd)
}

func runUpgrade(cmd *cobra.Command, args []string) {
	// 之前用 cmd.Flags().Changed(name) 判断，返回的是「该 flag 是否在命令行出现过」而非其布尔值
	// 导致 --check=false / --force=false 也会被当作 true。这里改用 GetBool 读取真实的布尔值
	checkOnly, _ := cmd.Flags().GetBool("check")
	forceUpdate, _ := cmd.Flags().GetBool("force")
	checkDev, _ := cmd.Flags().GetBool("dev")

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

	pterm.Info.Println("正在检查更新 (" + channel + ")...")
	pterm.Info.Printf("当前平台: %s\n", platform)

	info, err := updater.CheckForUpdate(Version, BuildTime, checkDev)
	if err != nil {
		pterm.Error.Println("检查更新失败: ", err)
		os.Exit(1)
	}

	if info == nil {
		pterm.Success.Println("当前已是最新版本")
		return
	}

	pterm.Info.Printf("当前版本: %s (构建: %s)\n", info.CurrentVersion, info.CurrentBuildTime)
	pterm.Info.Printf("最新版本: %s\n", info.LatestVersion)

	if checkOnly {
		return
	}

	if !forceUpdate {
		confirm, _ := pterm.DefaultInteractiveConfirm.Show(
			fmt.Sprintf("是否升级到 %s", info.LatestVersion),
		)
		if !confirm {
			pterm.Info.Println("已取消升级")
			return
		}
	}

	pterm.Info.Println("开始升级...")

	tempDir := os.TempDir()
	if err := updater.DownloadAndReplace(info.DownloadURL, info.AssetName, tempDir); err != nil {
		pterm.Error.Println("升级失败: ", err)
		os.Exit(1)
	}
}
