package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "worker",
		Short: "Start plugin schedule worker",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("worker command registered")
		},
	})
}
