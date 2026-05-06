package cmd

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/TencentBlueKing/blueapps-go/pkg/infras/database"
	log "github.com/TencentBlueKing/blueapps-go/pkg/logging"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/blueappsadapter"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/server"
	"github.com/TencentBlueKing/bk-plugin-runtime-go/internal/store"
)

func init() {
	var cfgFile string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Start plugin HTTP server",
		Run: func(cmd *cobra.Command, args []string) {
			ctx := context.Background()
			cfg, err := blueappsadapter.LoadAndInit(ctx, cfgFile)
			if err != nil {
				log.Fatalf("init runtime: %s", err)
			}
			scheduleStore := store.NewGormStore(database.Client(ctx))
			if err := scheduleStore.AutoMigrate(ctx); err != nil {
				log.Fatalf("migrate plugin schedules: %s", err)
			}
			srv := &http.Server{
				Addr:    ":" + strconv.Itoa(cfg.Service.Server.Port),
				Handler: server.NewRouter(server.Config{Store: scheduleStore}),
			}
			go func() {
				if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					log.Fatalf("start server: %s", err)
				}
			}()
			quit := make(chan os.Signal, 1)
			signal.Notify(quit, os.Interrupt)
			<-quit
			shutdownCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.Service.Server.GraceTimeout)*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				log.Fatalf("shutdown server: %s", err)
			}
		},
	}
	cmd.Flags().StringVar(&cfgFile, "conf", "", "config file")
	rootCmd.AddCommand(cmd)
}
