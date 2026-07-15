package cmd

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMigrateBeegoSchedulesCommandRunsOnlyWhenInvokedAndIsRepeatable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE schedule (
		trace_i_d TEXT PRIMARY KEY,
		plugin_version TEXT NOT NULL,
		state INTEGER NOT NULL,
		invoke_count INTEGER NOT NULL,
		inputs TEXT NOT NULL,
		context_inputs TEXT NOT NULL,
		context_store TEXT NOT NULL,
		outputs TEXT NOT NULL,
		error TEXT NULL,
		create_at DATETIME NOT NULL,
		finished BOOLEAN NOT NULL,
		finish_at DATETIME NULL
	)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO schedule (
		trace_i_d, plugin_version, state, invoke_count, inputs,
		context_inputs, context_store, outputs, create_at, finished
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, ?)`,
		"legacy-command", "1.0.0", 2, 1, `{}`, `{}`, `{}`, `{}`, false,
	).Error)

	provider := func(cmd *cobra.Command, cfgFile string) (*gorm.DB, error) {
		return db, nil
	}
	cmd := newMigrateBeegoSchedulesCommand(provider)
	require.False(t, db.Migrator().HasTable("schedules"), "constructing the command must not run the migration")
	var output bytes.Buffer
	cmd.SetOut(&output)
	require.NoError(t, cmd.Execute())
	require.Contains(t, output.String(), "scanned=1 migrated=1 skipped=0 resumable=1")

	output.Reset()
	cmd = newMigrateBeegoSchedulesCommand(provider)
	cmd.SetOut(&output)
	require.NoError(t, cmd.Execute())
	require.Contains(t, output.String(), "scanned=1 migrated=0 skipped=1 resumable=1")
}
