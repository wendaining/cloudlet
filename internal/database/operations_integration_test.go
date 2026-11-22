//go:build integration

package database_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/operations"
	"gorm.io/gorm"
)

func testOperations(t *testing.T, ctx context.Context, databaseStore *database.Store, connection string, projectID uuid.UUID, ownerID string) {
	t.Helper()
	store, err := operations.NewStore(databaseStore.DB)
	if err != nil {
		t.Fatal(err)
	}
	request := operations.Registration{
		ProjectID: projectID, Action: "worker.deploy",
		IdempotencyKey: "worker-release-1", RequestFingerprint: "release-1",
	}

	type registrationResult struct {
		op      operations.Operation
		created bool
		err     error
	}
	results := make([]registrationResult, 16)
	var pending sync.WaitGroup
	start := make(chan struct{})
	for i := range results {
		pending.Go(func() {
			<-start
			results[i].op, results[i].created, results[i].err = store.Register(ctx, request)
		})
	}
	close(start)
	pending.Wait()

	original := results[0].op
	createdCount := 0
	for _, result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.op != original {
			t.Fatalf("并发登记返回不同操作: %+v, %+v", original, result.op)
		}
		if result.created {
			createdCount++
		}
	}
	if createdCount != 1 || original.ID == uuid.Nil || original.Registration != request ||
		original.Progress != (operations.Progress{Phase: operations.Received, Outcome: operations.InProgress}) ||
		original.CreatedAt.IsZero() || !original.UpdatedAt.Equal(original.CreatedAt) {
		t.Fatalf("登记结果错误，创建次数 %d，操作 %+v", createdCount, original)
	}
	var rowCount int64
	if err := databaseStore.DB.Model(&operations.Operation{}).Where("project_id = ?", projectID).Count(&rowCount).Error; err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("并发登记产生了 %d 条记录，预期 1 条", rowCount)
	}

	different := request
	different.RequestFingerprint = "release-2"
	if _, _, err := store.Register(ctx, different); !errors.Is(err, operations.ErrIdempotencyConflict) {
		t.Fatalf("不同请求指纹登记返回 %v", err)
	}

	otherProject, err := databaseStore.CreateProject(ctx, ownerID, "另一个操作项目")
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := uuid.Parse(otherProject.ID)
	if err != nil {
		t.Fatal(err)
	}
	preparing := operations.Progress{Phase: operations.Preparing, Outcome: operations.InProgress}
	if _, err := store.Get(ctx, otherID, original.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("跨项目查询返回 %v", err)
	}
	if _, err := store.Advance(ctx, otherID, original.ID, operations.Received, preparing); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("跨项目推进返回 %v", err)
	}
	t.Run("idempotency scope", func(t *testing.T) {
		inAnotherProject := different
		inAnotherProject.ProjectID = otherID
		forAnotherAction := different
		forAnotherAction.Action = "worker.stop"
		for _, scopedRequest := range []operations.Registration{inAnotherProject, forAnotherAction} {
			op, created, err := store.Register(ctx, scopedRequest)
			if err != nil || !created || op.ID == original.ID {
				t.Fatalf("登记独立作用域操作返回 %+v, %t, %v", op, created, err)
			}
		}
	})

	advanced, err := store.Advance(ctx, projectID, original.ID, operations.Received, preparing)
	if err != nil || advanced.ID != original.ID || advanced.Progress != preparing ||
		advanced.Registration != request || !advanced.UpdatedAt.After(original.UpdatedAt) {
		t.Fatalf("推进操作返回 %+v, %v", advanced, err)
	}
	if _, err := store.Advance(ctx, projectID, original.ID, operations.Received, preparing); !errors.Is(err, operations.ErrStateConflict) {
		t.Fatalf("过期阶段推进返回 %v", err)
	}
	type transitionResult struct {
		op  operations.Operation
		err error
	}
	terminalResults := make([]transitionResult, 2)
	start = make(chan struct{})
	for i, outcome := range []operations.Outcome{operations.Succeeded, operations.Failed} {
		pending.Go(func() {
			<-start
			terminalResults[i].op, terminalResults[i].err = store.Advance(ctx, projectID, original.ID, operations.Preparing,
				operations.Progress{Phase: operations.Preparing, Outcome: outcome})
		})
	}
	close(start)
	pending.Wait()
	var terminal operations.Operation
	winners := 0
	for _, result := range terminalResults {
		if result.err == nil {
			winners++
			terminal = result.op
		} else if !errors.Is(result.err, operations.ErrStateConflict) {
			t.Fatal(result.err)
		}
	}
	if winners != 1 || terminal.ID != original.ID || terminal.Phase != operations.Preparing ||
		(terminal.Outcome != operations.Succeeded && terminal.Outcome != operations.Failed) {
		t.Fatalf("并发终态写入返回 %d 个成功结果，操作 %+v", winners, terminal)
	}
	finalizing := operations.Progress{Phase: operations.Finalizing, Outcome: operations.InProgress}
	if _, err := store.Advance(ctx, projectID, original.ID, operations.Preparing, finalizing); !errors.Is(err, operations.ErrStateConflict) {
		t.Fatalf("终态后的阶段推进返回 %v", err)
	}
	replayed, created, err := store.Register(ctx, request)
	if err != nil || created || replayed != terminal {
		t.Fatalf("终态操作重试返回 %+v, %t, %v", replayed, created, err)
	}

	reopened, err := database.Open(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.SQL.Close(); err != nil {
			t.Error(err)
		}
	})
	reconnected, err := operations.NewStore(reopened.DB)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reconnected.Get(ctx, projectID, original.ID)
	if err != nil || persisted != terminal {
		t.Fatalf("重新连接后查询操作返回 %+v, %v", persisted, err)
	}
	visible, err := databaseStore.ProjectOperations(ctx, ownerID, projectID.String())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, operation := range visible {
		if operation.ID == terminal.ID {
			if operation.Progress != terminal.Progress {
				t.Fatalf("项目内操作查询的终态错误: %+v", operation)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("项目内操作查询未返回终态操作: %+v", visible)
	}
	if visible, err := databaseStore.ProjectOperations(ctx, ownerID, uuid.Nil.String()); err != nil || len(visible) != 0 {
		t.Fatalf("未知项目操作查询返回 %+v, %v", visible, err)
	}
}
