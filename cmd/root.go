package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "bk-plugin-runtime-go",
	Short: "Run a BlueKing plugin runtime process",
}

func Execute() {
	if len(os.Args) == 1 {
		os.Args = append(os.Args, "server")
	}
	if err := rootCmd.Execute(); err != nil {
		panic(err)
	}
}
