package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	createForce  bool
	createDevice string
)

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "创建链接",
	Long: "创建链接（Long）",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("create called")
	},
}

func init() {
	rootCmd.AddCommand(createCmd)
}
