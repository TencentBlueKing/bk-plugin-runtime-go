package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/version"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print runtime version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version.Version)
		},
	})
}
