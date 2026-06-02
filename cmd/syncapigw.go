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
(callback / invoke / plugin_api / openapi / plugin_api_dispatch) and push the
result to the BlueKing API Gateway. This is the Go counterpart of the Python
framework's "sync_apigateway_if_changed" management command and follows the
same environment-variable conventions.

Environment variables (PaaS-injected unless noted):

  BKPAAS_BK_PLUGIN_APIGW_NAME   Gateway name (preferred, falls back to BK_APIGW_NAME / BKPAAS_APP_ID)
  BKPAAS_APP_ID / _SECRET       App credentials used to call the manager API
  BK_APIGW_MANAGER_URL_TMPL     Manager endpoint template (aliases: BK_APIGW_MANAGER_URL_TEMPL, BK_API_URL_TMPL)
  BKPAAS_ENVIRONMENT            Drives BK_APIGW_STAGE_NAME (stag -> stag, otherwise prod)
  BKPAAS_DEFAULT_PREALLOCATED_URLS
                                JSON map providing the stage backend host / sub-path
  BK_APIGW_MAINTAINERS          Comma-separated maintainer list (default "admin")
  BK_APIGW_IS_PUBLIC            "true" / "false" (default true)
  BK_APIGW_IS_OFFICIAL          "true" -> api_type=1, else api_type=10 (default 10)
  BK_APIGW_RELEASE_VERSION      Defaults to 1.0.0; UTC timestamp suffix appended on every deploy (1.0.0+20260527181542). Set with build metadata (e.g. 1.2.3+abc123) to pin a deterministic value.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Logger = logrus.StandardLogger()
			return apigwsync.Sync(context.Background(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.APIName, "name", "", "APIGW gateway name (defaults to env BKPAAS_BK_PLUGIN_APIGW_NAME / BK_APIGW_NAME / BKPAAS_APP_ID)")
	cmd.Flags().StringVar(&opts.DefinitionPath, "definition", "definition.yaml", "Path to a custom definition.yaml. When the file exists it fully overrides the runtime-bundled default template; otherwise the embedded default is used.")
	cmd.Flags().StringVar(&opts.ReleaseVersion, "release-version", "", "Resource version (SemVer). Defaults to ${BK_APIGW_RELEASE_VERSION:-1.0.0}+<UTC timestamp>; supply BK_APIGW_RELEASE_VERSION with build metadata (e.g. 1.2.3+abc123) to pin a deterministic value.")
	cmd.Flags().StringVar(&opts.ReleaseComment, "release-comment", "", "Release comment (default: auto release by bk-plugin-runtime-go(stage=...))")
	cmd.Flags().BoolVar(&opts.SkipRelease, "skip-release", false, "Create the resource version but do not release it")
	cmd.Flags().BoolVar(&opts.DeleteUnknownResources, "delete-unknown", false, "Drop APIGW resources not declared in the embedded resources.yaml")

	rootCmd.AddCommand(cmd)
}
