package cmd

import (
	"github.com/jy-eggroll/eggokit/updater"
	"github.com/jy-eggroll/eggokit/upgradecli"
	"github.com/spf13/cobra"
)

// upgradeCmd 只是把 flk 的发布目标与本项目的界面接到共享升级命令上。
//
// 命令的 flag 语义、通道选择（stable / dev / alpha）、提示文案与确认时机全部来自
// eggokit/upgradecli：这套流程原本在 flk 与 ggt 各写一份，改一处文案要改好几遍，
// 通道行为也容易只在一边更新。flk 自身剩下的差异只有三项——发布目标（见 upgrade_target.go）、
// 输出界面（pterm）与全局 --yes 开关，因此这里只注入这三项。
var upgradeCmd = upgradecli.NewCommand(upgradecli.Options{
	// Program 只出现在面向用户的文案里（例如“重启 flk 后生效”）
	Program: "flk",
	Target:  flkTarget,
	// 版本与构建时间由发布构建通过链接参数注入，必须是取值函数而不是此刻的字符串，
	// 否则本地开发构建的占位值会被提前固定下来
	Version:   func() string { return Version },
	BuildTime: func() string { return BuildTime },
	// 升级过程的状态与确认一律写 stderr：stdout 只承载业务数据，
	// 与 flk 其它命令的输出约定一致；用命令的流而不是 os.Stderr，测试才能替换输出目标
	Reporter: func(cmd *cobra.Command) updater.Reporter {
		return &ptermReporter{writer: cmd.ErrOrStderr()}
	},
	// --yes/-y 是 flk 的全局持久参数，共享命令不注册同名 flag（会与全局定义冲突），
	// 这里只把全局开关的当前取值转交过去
	AssumeYes: func() bool { return assumeYes },
})

func init() {
	rootCmd.AddCommand(upgradeCmd)
}
