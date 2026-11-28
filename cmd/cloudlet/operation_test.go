package main

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/pkg/client"
)

func TestWaitForOperationReachesTerminalOutcome(t *testing.T) {
	for _, test := range []struct {
		outcome string
		code    int
	}{{"succeeded", 0}, {"failed", 2}} {
		t.Run(test.outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				start := time.Now()
				calls := 0
				result, err := waitForOperation(ctx, time.Second, func(context.Context) (apigen.Operation, error) {
					calls++
					if calls == 1 {
						return apigen.Operation{Outcome: "in_progress"}, nil
					}
					return apigen.Operation{Outcome: test.outcome}, nil
				})
				if err != nil || result.Outcome != test.outcome || calls != 2 || time.Since(start) != time.Second {
					t.Fatalf("wait outcome=%q err=%v calls=%d elapsed=%s", result.Outcome, err, calls, time.Since(start))
				}
				assertOperationExitCode(t, operationOutcomeError(result.Outcome, "id"), test.code)
			})
		})
	}
}

func TestWaitForOperationStopsAtDeadlineOrCancellation(t *testing.T) {
	for _, test := range []struct {
		name   string
		cancel bool
		code   int
		cause  error
	}{{"deadline", false, 124, context.DeadlineExceeded}, {"cancel", true, 130, context.Canceled}} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
				defer cancel()
				if test.cancel {
					time.AfterFunc(100*time.Millisecond, cancel)
				}
				result, err := waitForOperation(ctx, time.Second, func(request context.Context) (apigen.Operation, error) {
					<-request.Done()
					return apigen.Operation{}, request.Err()
				})
				if result.Outcome != "" || !errors.Is(err, test.cause) {
					t.Fatalf("wait outcome=%q err=%v", result.Outcome, err)
				}
				assertOperationExitCode(t, err, test.code)
			})
		})
	}
}

func TestWaitForOperationStopsOnAPIError(t *testing.T) {
	queryError := &client.APIError{StatusCode: 401}
	result, err := waitForOperation(t.Context(), time.Second, func(context.Context) (apigen.Operation, error) {
		return apigen.Operation{}, queryError
	})
	if result.Outcome != "" || !errors.Is(err, queryError) {
		t.Fatalf("wait outcome=%q err=%v", result.Outcome, err)
	}
	assertOperationExitCode(t, operationQueryError(err), 4)
	assertOperationExitCode(t, operationQueryError(client.ErrOperationNotFound), 5)
}

func TestOperationWaitRequiresPositiveTimeout(t *testing.T) {
	for _, flags := range [][]string{nil, {"--timeout", "0s"}, {"--timeout", "-1s"}} {
		cmd := newOperationCommand()
		cmd.SetArgs(append([]string{"wait", "--project", "project", "--id", "operation"}, flags...))
		if err := cmd.ExecuteContext(t.Context()); err == nil {
			t.Fatalf("wait accepted timeout flags %v", flags)
		}
	}
}

func assertOperationExitCode(t *testing.T, err error, want int) {
	t.Helper()
	if want == 0 && err == nil {
		return
	}
	var commandErr *commandError
	if !errors.As(err, &commandErr) || commandErr.code != want {
		t.Fatalf("expected exit code %d, got %v", want, err)
	}
}
