package cmd

import (
	"github.com/jy-eggroll/flk/pkg/l10n"
	"github.com/spf13/cobra"
)

/*
serveCmd 是 serve 相关子命令的父命令，本身不做事
设计为"serve 子命令树"的根节点，后续 subcommand 扩展（如 config）挂在此命令下
*/
var serveCmd = &cobra.Command{
	Use:     "serve",
	Aliases: []string{"server"},
	Short:   l10n.T("Open the web service", nil),
	Long:    l10n.T("Open the web service with a visual management UI.\nUse the serve config subcommand to view the config file in a browser.", nil),
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.PersistentFlags().IntP("port", "p", 8999, l10n.T("Port to listen on", nil))
	serveCmd.PersistentFlags().String("host", "127.0.0.1", l10n.T("Host to bind", nil))
}
