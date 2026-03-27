package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	createForce  bool
	createSmart  bool
	createDevice string
)

var createCmd = &cobra.Command{
	Use:     "create",
	Aliases: []string{"cr"},
	Short:   "创建链接",
	Long:    "创建链接",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("create called")
	},
}

func init() {
	rootCmd.AddCommand(createCmd)
}
