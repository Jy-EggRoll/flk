package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var hardlinkCmd = &cobra.Command{
	Use:   "hardlink",
	Short: "创建硬链接",
	Long:  "创建硬链接",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("hardlink called")
	},
}

func init() {
	createCmd.AddCommand(hardlinkCmd)
}
