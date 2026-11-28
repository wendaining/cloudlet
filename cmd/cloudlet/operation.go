package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/pkg/client"
)

const (
	exitSuccess             = 0
	exitError               = 1
	exitOperationFailed     = 2
	exitOperationInProgress = 3
	exitUnauthenticated     = 4
	exitOperationNotFound   = 5
	exitWaitTimeout         = 124
	exitWaitCanceled        = 130
)

type commandError struct {
	code int
	err  error
}

func (e *commandError) Error() string { return e.err.Error() }
func (e *commandError) Unwrap() error { return e.err }

func newOperationCommand() *cobra.Command {
	operation := &cobra.Command{Use: "operation", Short: "Query an operation"}
	operation.AddCommand(newOperationQuery(false), newOperationQuery(true))
	return operation
}

func newOperationQuery(wait bool) *cobra.Command {
	var endpoint, cookie, projectID, operationID, output string
	var timeout time.Duration
	command := &cobra.Command{
		Use: "get", Short: "Get an operation result", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output != "table" && output != "json" {
				return fmt.Errorf("unsupported output format %q (expected table or json)", output)
			}
			if wait && timeout <= 0 {
				return errors.New("--timeout must be positive")
			}
			api, err := client.New(endpoint)
			if err != nil {
				return err
			}
			if err := api.SetSessionCookie(cookie); err != nil {
				return err
			}
			query := func(ctx context.Context) (apigen.Operation, error) {
				return api.ProjectOperation(ctx, projectID, operationID)
			}
			ctx := cmd.Context()
			var result apigen.Operation
			if wait {
				signals, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
				defer stop()
				deadline, cancel := context.WithTimeout(signals, timeout)
				defer cancel()
				result, err = waitForOperation(deadline, time.Second, query)
			} else {
				result, err = query(ctx)
			}
			if err != nil {
				return operationQueryError(err)
			}
			if err := writeOperation(cmd.OutOrStdout(), result, output); err != nil {
				return err
			}
			return operationOutcomeError(result.Outcome, operationID)
		},
	}
	if wait {
		command.Use = "wait"
		command.Short = "Wait for an operation to reach a terminal outcome"
		command.Flags().DurationVar(&timeout, "timeout", 0, "maximum time to wait (e.g. 30s)")
		_ = command.MarkFlagRequired("timeout")
	}
	command.Flags().StringVar(&endpoint, "endpoint", "http://127.0.0.1:8080", "management API endpoint")
	command.Flags().StringVar(&cookie, "cookie", os.Getenv("CLOUDLET_COOKIE"), "authenticated Cookie header (or CLOUDLET_COOKIE)")
	command.Flags().StringVar(&projectID, "project", "", "project ID")
	command.Flags().StringVar(&operationID, "id", "", "operation ID")
	command.Flags().StringVar(&output, "output", "table", "output format: table or json")
	_ = command.MarkFlagRequired("project")
	_ = command.MarkFlagRequired("id")
	return command
}

func waitForOperation(ctx context.Context, interval time.Duration, query func(context.Context) (apigen.Operation, error)) (apigen.Operation, error) {
	for {
		if err := ctx.Err(); err != nil {
			return apigen.Operation{}, operationWaitError(err)
		}
		result, err := query(ctx)
		if ctx.Err() != nil {
			return apigen.Operation{}, operationWaitError(ctx.Err())
		}
		if err != nil || result.Outcome != "in_progress" {
			return result, err
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return apigen.Operation{}, operationWaitError(ctx.Err())
		case <-timer.C:
		}
	}
}

func operationWaitError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &commandError{exitWaitTimeout, fmt.Errorf("operation wait timed out: %w", err)}
	}
	return &commandError{exitWaitCanceled, fmt.Errorf("operation wait canceled: %w", err)}
}

func operationQueryError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 401:
			return &commandError{exitUnauthenticated, err}
		case 404:
			return &commandError{exitOperationNotFound, err}
		}
	}
	if errors.Is(err, client.ErrOperationNotFound) {
		return &commandError{exitOperationNotFound, err}
	}
	return err
}

func writeOperation(out io.Writer, result apigen.Operation, output string) error {
	if output == "json" {
		return json.NewEncoder(out).Encode(result)
	}
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "OPERATION ID\tACTION\tPHASE\tOUTCOME\tCREATED AT\tUPDATED AT"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n", result.Id, result.Action, result.Phase, result.Outcome, result.CreatedAt.Format(time.RFC3339), result.UpdatedAt.Format(time.RFC3339)); err != nil {
		return err
	}
	return writer.Flush()
}

func operationOutcomeError(outcome, operationID string) error {
	switch outcome {
	case "succeeded":
		return nil
	case "failed":
		return &commandError{exitOperationFailed, fmt.Errorf("operation %s failed", operationID)}
	case "in_progress":
		return &commandError{exitOperationInProgress, fmt.Errorf("operation %s is in progress", operationID)}
	default:
		return fmt.Errorf("unknown operation outcome %q", outcome)
	}
}
