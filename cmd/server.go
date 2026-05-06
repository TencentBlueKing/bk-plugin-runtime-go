package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "server",
		Short: "Start plugin HTTP server",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("server command registered")
		},
	})
}
