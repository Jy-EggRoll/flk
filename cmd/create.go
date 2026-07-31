package cmd

import "github.com/spf13/cobra"

var (
	createForce  bool
	createSmart  bool
	createDevice string
)

// createCmd 仅作为创建类命令的分组父节点
// 用户未指定 symlink、hardlink 或 copy 时展示帮助，不执行业务，也不会触发欢迎语或存储初始化
var createCmd = &cobra.Command{
	Use:     "create",
	Aliases: []string{"cr"},
	Short:   "创建链接",
	Long:    "创建链接",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() {
	rootCmd.AddCommand(createCmd)
}
