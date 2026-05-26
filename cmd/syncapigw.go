package cmd

import (
	"context"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/apigwsync"
)

func init() {
	var opts apigwsync.SyncOptions
	cmd := &cobra.Command{
		Use:   "syncapigw",
		Short: "Synchronize APIGW resources for the plugin runtime",
		Long: `Render <definition>.yaml together with the runtime-owned resources.yaml
(meta/detail/invoke/schedule/callback/plugin_api_dispatch/plugin_api) and push
the result to the BlueKing API Gateway. This is the Go counterpart of the
Python framework's "sync_apigateway_if_changed" management command.

Required environment variables (or matching --flag):

  BK_APIGW_NAME       Gateway name (defaults to BKPAAS_APP_ID)
  BK_API_URL_TMPL     APIGW endpoint template (e.g. http://bkapi.example.com/api/{api_name})
  BKPAAS_APP_ID       App code (used as APIGW credential when BK_APP_CODE is unset)
  BKPAAS_APP_SECRET   App secret (used as APIGW credential when BK_APP_SECRET is unset)
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Logger = logrus.StandardLogger()
			return apigwsync.Sync(context.Background(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.APIName, "name", "", "APIGW gateway name (defaults to env BK_APIGW_NAME / BKPAAS_APP_ID)")
	cmd.Flags().StringVar(&opts.DefinitionPath, "definition", "definition.yaml", "Path to definition.yaml (rendered with pongo2)")
	cmd.Flags().StringVar(&opts.ReleaseVersion, "release-version", "", "Resource version to create (defaults to env BK_APIGW_RELEASE_VERSION or v<UTC timestamp>)")
	cmd.Flags().StringVar(&opts.ReleaseComment, "release-comment", "auto-sync from bk-plugin-runtime-go", "Release comment attached to the resource version")
	cmd.Flags().BoolVar(&opts.SkipRelease, "skip-release", false, "Create the resource version but do not release it")
	cmd.Flags().BoolVar(&opts.DeleteUnknownResources, "delete-unknown", false, "Drop APIGW resources not declared in the embedded resources.yaml")

	rootCmd.AddCommand(cmd)
}
