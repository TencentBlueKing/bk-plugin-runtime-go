package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"gorm.io/gorm"

	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/blueappsadapter"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
	"github.com/TencentBlueKing/blueapps-go/pkg/infras/database"
)

func init() {
	rootCmd.AddCommand(newMigrateBeegoSchedulesCommand(nil))
}

func newMigrateBeegoSchedulesCommand(dbProvider func(*cobra.Command, string) (*gorm.DB, error)) *cobra.Command {
	var (
		cfgFile   string
		batchSize int
	)
	if dbProvider == nil {
		dbProvider = func(cmd *cobra.Command, cfgFile string) (*gorm.DB, error) {
			ctx := cmd.Context()
			if _, err := blueappsadapter.LoadAndInit(ctx, cfgFile); err != nil {
				return nil, err
			}
			return database.Client(ctx), nil
		}
	}

	cmd := &cobra.Command{
		Use:   "migrate-beego-schedules",
		Short: "Explicitly migrate beego-runtime schedule rows to the new runtime",
		Long: `Copy the legacy beego-runtime schedule table into the new GORM schedules
table. This command is never run by server or worker startup.

Stop the legacy web and worker processes before running it. Re-running the
command is safe: existing trace IDs in schedules are skipped and never
overwritten.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := dbProvider(cmd, cfgFile)
			if err != nil {
				return fmt.Errorf("initialize database: %w", err)
			}
			report, err := store.MigrateLegacySchedules(cmd.Context(), db, store.LegacyScheduleMigrationOptions{
				BatchSize: batchSize,
			})
			if err != nil {
				return err
			}
			if !report.LegacyTableFound {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "legacy schedule table not found; nothing to migrate")
				return err
			}
			_, err = fmt.Fprintf(
				cmd.OutOrStdout(),
				"legacy schedule migration complete: scanned=%d migrated=%d skipped=%d resumable=%d\n",
				report.Scanned,
				report.Migrated,
				report.Skipped,
				report.Resumable,
			)
			return err
		},
	}
	cmd.Flags().StringVar(&cfgFile, "conf", "", "Path to the blueapps configuration file")
	cmd.Flags().IntVar(&batchSize, "batch-size", 500, "Number of legacy rows migrated per transaction")
	return cmd
}
