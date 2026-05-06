package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "syncapigw",
		Short: "Synchronize APIGW resources",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("syncapigw command is not active in phase 1")
		},
	})
}
