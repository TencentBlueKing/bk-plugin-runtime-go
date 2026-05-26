package cmd

import (
	"context"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/apigwsync"
)

func init() {
	var (
		apiName  string
		destPath string
	)
	cmd := &cobra.Command{
		Use:   "fetch-apigw-public-key",
		Short: "Fetch the gateway's RSA public key for JWT verification",
		Long: `Fetch the configured BlueKing API Gateway's public key and persist it to a
local file. The plugin runtime can then use the file to verify X-Bkapi-JWT
tokens. The default destination is ./apigw.pub.

The behaviour mirrors the Python framework's "fetch_apigw_public_key"
management command. It pairs with "syncapigw" and is intended to be invoked
from the PaaS preRelease/postCompile hook.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return apigwsync.FetchPublicKey(context.Background(), apiName, destPath, logrus.StandardLogger())
		},
	}
	cmd.Flags().StringVar(&apiName, "name", "", "APIGW gateway name (defaults to env BK_APIGW_NAME / BKPAAS_APP_ID)")
	cmd.Flags().StringVar(&destPath, "out", "apigw.pub", "Destination file path for the public key")

	rootCmd.AddCommand(cmd)
}
