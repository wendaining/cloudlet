package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/wendaining/cloudlet/pkg/client"
)

const (
	exitSuccess             = 0
	exitError               = 1
	exitOperationFailed     = 2
	exitOperationInProgress = 3
	exitUnauthenticated     = 4
	exitOperationNotFound   = 5
)

type commandError struct {
	code int
	err  error
}

func (e *commandError) Error() string { return e.err.Error() }
func (e *commandError) Unwrap() error { return e.err }

func newOperationCommand() *cobra.Command {
	var endpoint, cookie, projectID, operationID, output string
	operation := &cobra.Command{Use: "operation", Short: "Query an operation"}
	get := &cobra.Command{
		Use: "get", Short: "Get an operation result", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			api, err := client.New(endpoint)
			if err != nil {
				return err
			}
			if err := api.SetSessionCookie(cookie); err != nil {
				return err
			}
			result, err := api.ProjectOperation(cmd.Context(), projectID, operationID)
			if err != nil {
				var apiErr *client.APIError
				if errors.As(err, &apiErr) && apiErr.StatusCode == 401 {
					return &commandError{exitUnauthenticated, err}
				}
				if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
					return &commandError{exitOperationNotFound, err}
				}
				if errors.Is(err, client.ErrOperationNotFound) {
					return &commandError{exitOperationNotFound, err}
				}
				return err
			}
			switch output {
			case "json":
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
					return err
				}
			case "table":
				writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				if _, err := fmt.Fprintln(writer, "OPERATION ID	ACTION	PHASE	OUTCOME	CREATED AT	UPDATED AT"); err != nil {
					return err
				}
				if _, err := fmt.Fprintf(writer, "%s	%s	%s	%s	%s	%s\n", result.Id, result.Action, result.Phase, result.Outcome, result.CreatedAt.Format(time.RFC3339), result.UpdatedAt.Format(time.RFC3339)); err != nil {
					return err
				}
				if err := writer.Flush(); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported output format %q (expected table or json)", output)
			}
			switch result.Outcome {
			case "succeeded":
				return nil
			case "failed":
				return &commandError{exitOperationFailed, fmt.Errorf("operation %s failed", operationID)}
			case "in_progress":
				return &commandError{exitOperationInProgress, fmt.Errorf("operation %s is in progress", operationID)}
			default:
				return fmt.Errorf("unknown operation outcome %q", result.Outcome)
			}
		},
	}
	get.Flags().StringVar(&endpoint, "endpoint", "http://127.0.0.1:8080", "management API endpoint")
	get.Flags().StringVar(&cookie, "cookie", os.Getenv("CLOUDLET_COOKIE"), "authenticated Cookie header (or CLOUDLET_COOKIE)")
	get.Flags().StringVar(&projectID, "project", "", "project ID")
	get.Flags().StringVar(&operationID, "id", "", "operation ID")
	get.Flags().StringVar(&output, "output", "table", "output format: table or json")
	_ = get.MarkFlagRequired("project")
	_ = get.MarkFlagRequired("id")
	operation.AddCommand(get)
	return operation
}
