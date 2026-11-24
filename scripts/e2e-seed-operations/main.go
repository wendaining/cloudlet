package main

// This development-only helper seeds deterministic Operation records for the
// Playwright suite. It requires CLOUDLET_DATABASE_URL and an existing project.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/operations"
)

func main() {
	if len(os.Args) != 2 {
		fail("usage: e2e-seed-operations PROJECT_ID")
	}
	projectID, err := uuid.Parse(os.Args[1])
	if err != nil {
		fail("invalid project ID: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := database.Open(ctx, os.Getenv("CLOUDLET_DATABASE_URL"))
	if err != nil {
		fail("open test database: %v", err)
	}
	defer db.SQL.Close()
	store, err := operations.NewStore(db.DB)
	if err != nil {
		fail("open operation store: %v", err)
	}
	records := make([]operations.Operation, 0, 3)
	for _, action := range []string{"e2e_deploy_in_progress", "e2e_deploy_succeeded", "e2e_deploy_failed"} {
		record, _, err := store.Register(ctx, operations.Registration{
			ProjectID: projectID, Action: action, IdempotencyKey: "e2e-" + action,
			RequestFingerprint: "e2e-genuine-operation",
		})
		if err != nil {
			fail("register %s: %v", action, err)
		}
		if action == "e2e_deploy_succeeded" {
			record, err = store.Advance(ctx, projectID, record.ID, operations.Received, operations.Progress{Phase: operations.Preparing, Outcome: operations.InProgress})
			if err == nil {
				record, err = store.Advance(ctx, projectID, record.ID, operations.Preparing, operations.Progress{Phase: operations.Preparing, Outcome: operations.Succeeded})
			}
		} else if action == "e2e_deploy_failed" {
			record, err = store.Advance(ctx, projectID, record.ID, operations.Received, operations.Progress{Phase: operations.Received, Outcome: operations.Failed})
		}
		if err != nil {
			fail("advance %s: %v", action, err)
		}
		records = append(records, record)
	}
	if err := json.NewEncoder(os.Stdout).Encode(records); err != nil {
		fail("encode seeded records: %v", err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
