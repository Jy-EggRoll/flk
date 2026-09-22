package cmd

import (
	"fmt"
	"runtime"

	"github.com/jy-eggroll/flk/internal/updater"
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

// runUpgrade 完成版本检查、用户确认与升级执行
// 所有用户可见输出都写入命令的 stderr，业务失败只返回带上下文的错误交由根命令统一打印一次，
// 命令内部绝不调用 os.Exit，以免绕过统一的错误渲染与退出码边界
func runUpgrade(cmd *cobra.Command, args []string) error {
	errOut := cmd.ErrOrStderr()

	// GetBool 读取 flag 的真实布尔值，避免 --check=false 这类显式 false 被 Flags().Changed 误判为启用；
	// 任一读取失败都必须沿 RunE 返回，不能以零值继续执行而掩盖命令定义或测试注入错误
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

	channel := updater.ChannelStable
	channelLabel := "正式版"
	if checkDev {
		channel = updater.ChannelDev
		channelLabel = "开发版"
	}

	// reporter 承接升级器的全部输出，令牌查找结果被升级器与下面的提示共用，只会真正查找一次
	reporter := &ptermReporter{writer: errOut}
	tokenProvider := updater.EnvTokenProvider("")

	// 升级器只接收注入的仓库坐标、资产规则与界面，不感知 flk 的任何发布约定
	upgrader, err := updater.New(updater.Config{
		Owner:         upstreamOwner,
		Repo:          upstreamRepo,
		AssetName:     flkAssetName,
		Reporter:      reporter,
		TokenProvider: tokenProvider,
		ProxyPrefix:   downloadProxyPrefix,
		UserAgent:     "flk-updater",
	})
	if err != nil {
		return fmt.Errorf("初始化升级器失败: %w", err)
	}

	platform := fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		platform += " (exe)"
	}

	reporter.Info("正在检查更新（%s）...", channelLabel)
	reporter.Info("当前平台: %s", platform)

	// 令牌状态提示属于 flk 的运维建议而非升级器职责，因此留在命令层；
	// 只在缺失时提示，配置正确的用户无需被无关信息打扰
	if tokenProvider() == "" {
		reporter.Info("未配置 GitHub Token，API 请求受匿名限速影响，可设置 GITHUB_TOKEN 提升配额")
	}

	info, err := upgrader.Check(Version, BuildTime, channel)
	if err != nil {
		return fmt.Errorf("检查更新失败: %w", err)
	}
	if info == nil {
		reporter.Success("当前已是最新版本")
		return nil
	}

	reporter.Info("当前版本: %s (构建: %s)", info.CurrentVersion, info.CurrentBuildTime)
	if info.CurrentComparable {
		reporter.Info("最新版本: %s", info.LatestVersion)
	} else {
		// 本地版本无法比较时不能宣称"最新"：这里给出的只是通道内的最高版本，
		// 是否比本地构建新需要用户自行判断
		reporter.Info("%s 通道内最高版本: %s", channelLabel, info.LatestVersion)
	}

	if checkOnly {
		return nil
	}

	if !forceUpdate {
		confirmed, confirmErr := reporter.Confirm(fmt.Sprintf("是否升级到 %s", info.LatestVersion))
		if confirmErr != nil {
			return fmt.Errorf("读取升级确认失败: %w", confirmErr)
		}
		if !confirmed {
			reporter.Info("已取消升级")
			return nil
		}
	}

	reporter.Info("开始升级...")
	if err := upgrader.Apply(info); err != nil {
		return fmt.Errorf("升级失败: %w", err)
	}

	// 替换完成后当前进程仍运行旧版本，必须明确告知用户何时生效，
	// 避免用户在当前会话中反复执行命令却看不到版本变化
	reporter.Success("升级完成，重新运行 flk 后生效")
	return nil
}
