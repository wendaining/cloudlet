package main

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/spf13/cobra"
	"github.com/wendaining/cloudlet/internal/database"
)

func main() {
	root := &cobra.Command{Use: "cloudlet-admin", SilenceUsage: true}
	for _, name := range []string{"migrate", "db-status", "seed-development"} {
		root.AddCommand(&cobra.Command{Use: name, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := database.Open(cmd.Context(), os.Getenv("CLOUDLET_DATABASE_URL"))
			if err != nil {
				return err
			}
			defer store.SQL.Close()
			switch cmd.Name() {
			case "migrate":
				return store.Migrate(cmd.Context())
			case "db-status":
				status, err := store.Status(cmd.Context())
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			case "seed-development":
				if os.Getenv("CLOUDLET_ENV") != "development" {
					return errors.New("开发账号初始化仅允许用于 development 环境")
				}
				if err := store.CheckVersion(cmd.Context()); err != nil {
					return err
				}
				return store.SeedUser(cmd.Context(), os.Getenv("CLOUDLET_DEV_EMAIL"), os.Getenv("CLOUDLET_DEV_PASSWORD"))
			}
			return errors.New("未知管理命令")
		}})
	}
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
