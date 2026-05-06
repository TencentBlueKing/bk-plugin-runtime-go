package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "collectstatics",
		Short: "Compatibility no-op for old beego-runtime deployments",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("collectstatics is a no-op in bk-plugin-runtime-go")
		},
	})
}
