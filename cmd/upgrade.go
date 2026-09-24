package cmd

import (
	"fmt"
	"runtime"

	"github.com/jy-eggroll/flk/internal/updater"
	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/spf13/cobra"
)

var upgradeCmd = &cobra.Command{
	Use:     "upgrade",
	Aliases: []string{"update", "up"},
	Short:   l10n.T("Check for and upgrade to the latest version", nil),
	Long:    l10n.T("Check for and upgrade flk to the latest version", nil),
	RunE:    runUpgrade,
}

func init() {
	upgradeCmd.Flags().Bool("check", false, l10n.T("Only check the version, do not upgrade", nil))
	upgradeCmd.Flags().Bool("force", false, l10n.T("Force upgrade", nil))
	upgradeCmd.Flags().Bool("dev", false, l10n.T("Check for development versions", nil))
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
		return fmt.Errorf("%s: %w", l10n.T("Failed to read the --check argument", nil), err)
	}
	forceUpdate, err := cmd.Flags().GetBool("force")
	if err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to read the --force argument", nil), err)
	}
	checkDev, err := cmd.Flags().GetBool("dev")
	if err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to read the --dev argument", nil), err)
	}

	channel := updater.ChannelStable
	channelLabel := l10n.T("stable", nil)
	if checkDev {
		channel = updater.ChannelDev
		channelLabel = l10n.T("dev", nil)
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
		return fmt.Errorf("%s: %w", l10n.T("Failed to initialize the upgrader", nil), err)
	}

	platform := fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		platform += " (exe)"
	}

	reporter.Info("%s", l10n.T("Checking for updates ({{.Channel}})...", map[string]any{"Channel": channelLabel}))
	reporter.Info("%s", l10n.T("Current platform: {{.Platform}}", map[string]any{"Platform": platform}))

	// 令牌状态提示属于 flk 的运维建议而非升级器职责，因此留在命令层；
	// 只在缺失时提示，配置正确的用户无需被无关信息打扰
	if tokenProvider() == "" {
		reporter.Info("%s", l10n.T("No GitHub token configured; API requests are subject to anonymous rate limits. Set GITHUB_TOKEN to raise the quota", nil))
	}

	info, err := upgrader.Check(Version, BuildTime, channel)
	if err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to check for updates", nil), err)
	}
	if info == nil {
		reporter.Success("%s", l10n.T("Already on the latest version", nil))
		return nil
	}

	reporter.Info("%s", l10n.T("Current version: {{.Version}} (build: {{.Build}})", map[string]any{"Version": info.CurrentVersion, "Build": info.CurrentBuildTime}))
	if info.CurrentComparable {
		reporter.Info("%s", l10n.T("Latest version: {{.Version}}", map[string]any{"Version": info.LatestVersion}))
	} else {
		// 本地版本无法比较时不能宣称"最新"：这里给出的只是通道内的最高版本，
		// 是否比本地构建新需要用户自行判断
		reporter.Info("%s", l10n.T("Highest version in the {{.Channel}} channel: {{.Version}}", map[string]any{"Channel": channelLabel, "Version": info.LatestVersion}))
	}

	if checkOnly {
		return nil
	}

	if !forceUpdate {
		confirmed, confirmErr := reporter.Confirm(l10n.T("Upgrade to {{.Version}}?", map[string]any{"Version": info.LatestVersion}))
		if confirmErr != nil {
			return fmt.Errorf("%s: %w", l10n.T("Failed to read the upgrade confirmation", nil), confirmErr)
		}
		if !confirmed {
			reporter.Info("%s", l10n.T("Upgrade cancelled", nil))
			return nil
		}
	}

	reporter.Info("%s", l10n.T("Starting upgrade...", nil))
	if err := upgrader.Apply(info); err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Upgrade failed", nil), err)
	}

	// 替换完成后当前进程仍运行旧版本，必须明确告知用户何时生效，
	// 避免用户在当前会话中反复执行命令却看不到版本变化
	reporter.Success("%s", l10n.T("Upgrade complete; it takes effect after restarting flk", nil))
	return nil
}
