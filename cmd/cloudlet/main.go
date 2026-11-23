package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/wendaining/cloudlet/internal/version"
	"github.com/wendaining/cloudlet/pkg/client"
)

func main() {
	root := &cobra.Command{Use: "cloudlet", Short: "Cloudlet command line client", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newOperationCommand())
	root.AddCommand(&cobra.Command{
		Use: "version", Short: "Print client build information", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(version.Current())
		},
	})
	var endpoint string
	health := &cobra.Command{
		Use: "health", Short: "Check management API health", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			api, err := client.New(endpoint)
			if err != nil {
				return err
			}
			status, err := api.Health(cmd.Context())
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"status": status})
		},
	}
	health.Flags().StringVar(&endpoint, "endpoint", "http://127.0.0.1:8080", "management API endpoint")
	root.AddCommand(health)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		var commandErr *commandError
		if errors.As(err, &commandErr) {
			os.Exit(commandErr.code)
		}
		os.Exit(exitError)
	}
}
