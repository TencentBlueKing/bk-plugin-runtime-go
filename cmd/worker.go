package cmd

import (
	"context"
	"os"
	"os/signal"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/blueappsadapter"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/scheduler"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
	"github.com/TencentBlueKing/blueapps-go/pkg/infras/database"
	log "github.com/TencentBlueKing/blueapps-go/pkg/logging"
)

func init() {
	var cfgFile string
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Start plugin schedule worker",
		Run: func(cmd *cobra.Command, args []string) {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()
			if _, err := blueappsadapter.LoadAndInit(ctx, cfgFile); err != nil {
				log.Fatalf("init runtime: %s", err)
			}
			scheduleStore := store.NewGormStore(database.Client(ctx))
			worker := scheduler.NewWorker(scheduler.Config{
				Store:    scheduleStore,
				WorkerID: uuid.NewString(),
				Interval: time.Second,
				Logger:   logrus.NewEntry(logrus.StandardLogger()),
			})
			if err := worker.Run(ctx); err != nil && err != context.Canceled {
				log.Fatalf("worker stopped: %s", err)
			}
		},
	}
	cmd.Flags().StringVar(&cfgFile, "conf", "", "config file")
	rootCmd.AddCommand(cmd)
}
