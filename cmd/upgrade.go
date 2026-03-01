package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/jy-eggroll/flk/internal/updater"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
)

var upgradeCmd = &cobra.Command{
	Use:     "upgrade",
	Aliases: []string{"update"},
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
	checkOnly := cmd.Flags().Changed("check")
	forceUpdate := cmd.Flags().Changed("force")
	checkDev := cmd.Flags().Changed("dev")

	channel := "正式版"
	if checkDev {
		channel = "开发版"
	}
	pterm.Info.Println("正在检查更新 (" + channel + ")...")

	info, err := updater.CheckForUpdate(Version, BuildTime, checkDev)
	if err != nil {
		pterm.Error.Println("检查更新失败:", err)
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
		fmt.Printf("是否升级到 %s? [y/N]: ", info.LatestVersion)
		input := strings.ToLower(strings.TrimSpace(readLine()))
		if input != "y" && input != "yes" {
			pterm.Info.Println("已取消升级")
			return
		}
	}

	pterm.Info.Println("开始升级...")

	tempDir := os.TempDir()
	if err := updater.DownloadAndReplace(info.DownloadURL, info.AssetName, tempDir); err != nil {
		pterm.Error.Println("升级失败:", err)
		os.Exit(1)
	}
}

func readLine() string {
	r, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return r
}
