//go:build integration

package capacity_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/database"
)

func TestCapacityReservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	data, err := os.ReadFile(filepath.Join("..", "..", "configs", "images.json"))
	if err != nil {
		t.Fatal(err)
	}
	var images struct {
		Postgres string `json:"postgres"`
	}
	if err := json.Unmarshal(data, &images); err != nil {
		t.Fatal(err)
	}
	container, err := postgres.Run(ctx, images.Postgres,
		postgres.WithDatabase("cloudlet"), postgres.WithUsername("cloudlet"),
		postgres.WithPassword("capacity-integration-password"),
		testcontainers.WithCmd("postgres"), postgres.BasicWaitStrategies())
	if container != nil {
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
			defer cleanupCancel()
			if err := container.Terminate(cleanupCtx); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	connection, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	databaseStore, err := database.Open(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { databaseStore.SQL.Close() })
	for range 2 {
		if err := databaseStore.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	const email = "capacity@cloudlet.local"
	const password = "capacity-integration-password"
	if err := databaseStore.SeedUser(ctx, email, password); err != nil {
		t.Fatal(err)
	}
	owner, err := databaseStore.Authenticate(ctx, email, password)
	if err != nil {
		t.Fatal(err)
	}
	store, err := capacity.NewStore(databaseStore.DB)
	if err != nil {
		t.Fatal(err)
	}

	projectA := createProject(t, ctx, databaseStore, owner.ID, "容量项目 A")
	projectB := createProject(t, ctx, databaseStore, owner.ID, "容量项目 B")
	hostID := "host-" + uuid.NewString()
	hostCapacity := capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4}
	setHost(t, ctx, store, hostID, hostCapacity)
	for _, projectID := range []uuid.UUID{projectA, projectB} {
		setProjectQuota(t, ctx, store, projectID, hostCapacity)
	}

	execution1 := uuid.New()
	request1 := capacity.Request{
		ExecutionID: execution1,
		HostID:      hostID,
		ProjectID:   projectA,
		Resources:   capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
	}
	reservation1, created, err := store.Reserve(ctx, request1)
	if err != nil || !created {
		t.Fatalf("首次预留返回 %+v, %t, %v", reservation1, created, err)
	}
	if reservation1.State != capacity.Active || reservation1.ReleasedAt != nil ||
		reservation1.HostID != hostID || reservation1.ProjectID != projectA ||
		reservation1.Resources != request1.Resources || reservation1.CreatedAt.IsZero() {
		t.Fatalf("首次预留内容错误: %+v", reservation1)
	}
	repeated, created, err := store.Reserve(ctx, request1)
	if err != nil || created || !sameReservation(repeated, reservation1) {
		t.Fatalf("同一标识重复申请返回 %+v, %t, %v", repeated, created, err)
	}
	for name, different := range map[string]capacity.Request{
		"resources": func() capacity.Request {
			request := request1
			request.Resources.CPUMillis = 2000
			return request
		}(),
		"project": func() capacity.Request {
			request := request1
			request.ProjectID = projectB
			return request
		}(),
		"host": func() capacity.Request {
			request := request1
			request.HostID = "other-" + hostID
			return request
		}(),
	} {
		t.Run("idempotency "+name, func(t *testing.T) {
			if _, _, err := store.Reserve(ctx, different); !errors.Is(err, capacity.ErrIdempotencyConflict) {
				t.Fatalf("不同请求复用执行标识返回 %v，要求 ErrIdempotencyConflict", err)
			}
		})
	}

	assertUsage(t, ctx, store, hostID, projectA, capacity.Usage{
		HostID:       hostID,
		HostLimit:    hostCapacity,
		HostUsed:     request1.Resources,
		ProjectID:    projectA,
		ProjectLimit: hostCapacity,
		ProjectUsed:  request1.Resources,
	})
	assertProjectUsage(t, ctx, store, projectA, capacity.ProjectUsage{
		ProjectID: projectA,
		Limit:     hostCapacity,
		Used:      request1.Resources,
		Remaining: capacity.Resources{CPUMillis: 3000, MemoryBytes: 3 << 30, Instances: 3},
	})

	// The host budget is shared by all projects.
	request2 := capacity.Request{
		ExecutionID: uuid.New(), HostID: hostID, ProjectID: projectA,
		Resources: capacity.Resources{CPUMillis: 2000, MemoryBytes: 2 << 30, Instances: 1},
	}
	if _, created, err := store.Reserve(ctx, request2); err != nil || !created {
		t.Fatalf("项目 A 第二次预留返回 %t, %v", created, err)
	}
	request3 := capacity.Request{
		ExecutionID: uuid.New(), HostID: hostID, ProjectID: projectB,
		Resources: capacity.Resources{CPUMillis: 2000, MemoryBytes: 2 << 30, Instances: 1},
	}
	if _, _, err := store.Reserve(ctx, request3); !errors.Is(err, capacity.ErrInsufficientCapacity) {
		t.Fatalf("项目 B 超过共享主机预算返回 %v，要求 ErrInsufficientCapacity", err)
	}
	request3.Resources = capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1}
	if _, created, err := store.Reserve(ctx, request3); err != nil || !created {
		t.Fatalf("项目 B 使用剩余主机容量返回 %t, %v", created, err)
	}
	request4 := capacity.Request{
		ExecutionID: uuid.New(), HostID: hostID, ProjectID: projectB,
		Resources: capacity.Resources{CPUMillis: 1, MemoryBytes: 1, Instances: 1},
	}
	if _, _, err := store.Reserve(ctx, request4); !errors.Is(err, capacity.ErrInsufficientCapacity) {
		t.Fatalf("主机容量用尽后申请返回 %v，要求 ErrInsufficientCapacity", err)
	}

	released, err := store.Release(ctx, projectA, execution1)
	if err != nil || released.State != capacity.Released || released.ReleasedAt == nil {
		t.Fatalf("释放预留返回 %+v, %v", released, err)
	}
	releasedAgain, err := store.Release(ctx, projectA, execution1)
	if err != nil || !sameReservation(releasedAgain, released) {
		t.Fatalf("重复释放返回 %+v, %v", releasedAgain, err)
	}
	if _, _, err := store.Reserve(ctx, request1); !errors.Is(err, capacity.ErrExecutionReleased) {
		t.Fatalf("已释放执行标识重新申请返回 %v，要求 ErrExecutionReleased", err)
	}
	if _, err := store.Release(ctx, projectB, execution1); !errors.Is(err, capacity.ErrReservationNotFound) {
		t.Fatalf("跨项目释放返回 %v，要求 ErrReservationNotFound", err)
	}
	if _, err := store.Release(ctx, projectA, uuid.New()); !errors.Is(err, capacity.ErrReservationNotFound) {
		t.Fatalf("释放未知执行返回 %v，要求 ErrReservationNotFound", err)
	}
	persisted, err := store.GetReservation(ctx, projectA, execution1)
	if err != nil || !sameReservation(persisted, released) {
		t.Fatalf("读取已释放预留返回 %+v, %v", persisted, err)
	}

	// Releasing the first reservation makes room for another project.
	request5 := capacity.Request{
		ExecutionID: uuid.New(), HostID: hostID, ProjectID: projectB,
		Resources: capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
	}
	if _, created, err := store.Reserve(ctx, request5); err != nil || !created {
		t.Fatalf("释放后重新申请返回 %t, %v", created, err)
	}
	assertUsage(t, ctx, store, hostID, projectA, capacity.Usage{
		HostID:       hostID,
		HostLimit:    hostCapacity,
		HostUsed:     capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 3},
		ProjectID:    projectA,
		ProjectLimit: hostCapacity,
		ProjectUsed:  request2.Resources,
	})
	assertProjectUsage(t, ctx, store, projectA, capacity.ProjectUsage{
		ProjectID: projectA,
		Limit:     hostCapacity,
		Used:      request2.Resources,
		Remaining: capacity.Resources{CPUMillis: 2000, MemoryBytes: 2 << 30, Instances: 3},
	})

	if _, err := store.SetProjectQuota(ctx, projectA, capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1}); !errors.Is(err, capacity.ErrQuotaInUse) {
		t.Fatalf("项目配额低于当前占用返回 %v，要求 ErrQuotaInUse", err)
	}
	if _, err := store.SetHostCapacity(ctx, hostID, capacity.Resources{CPUMillis: 3000, MemoryBytes: 3 << 30, Instances: 2}); !errors.Is(err, capacity.ErrQuotaInUse) {
		t.Fatalf("主机容量低于当前占用返回 %v，要求 ErrQuotaInUse", err)
	}

	reopened, err := database.Open(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.SQL.Close() })
	reopenedStore, err := capacity.NewStore(reopened.DB)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err = reopenedStore.GetReservation(ctx, projectA, execution1)
	if err != nil || !sameReservation(persisted, released) {
		t.Fatalf("重新连接后读取已释放预留返回 %+v, %v", persisted, err)
	}
	persistedUsage, err := reopenedStore.Usage(ctx, hostID, projectB)
	if err != nil || persistedUsage.HostUsed != (capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 3}) {
		t.Fatalf("重新连接后容量占用返回 %+v, %v", persistedUsage, err)
	}

	t.Run("project quota across hosts", func(t *testing.T) {
		testProjectQuotaAcrossHosts(t, ctx, store, databaseStore, owner.ID)
	})
	t.Run("concurrent last capacity", func(t *testing.T) {
		testConcurrentLastCapacity(t, ctx, store, databaseStore, owner.ID)
	})
	t.Run("concurrent duplicate execution", func(t *testing.T) {
		testConcurrentDuplicate(t, ctx, store, databaseStore, owner.ID)
	})
	t.Run("unconfigured budgets", func(t *testing.T) {
		testUnconfiguredBudgets(t, ctx, store, databaseStore, owner.ID)
	})
	t.Run("container binding", func(t *testing.T) {
		testContainerBinding(t, ctx, store, databaseStore, owner.ID)
	})
	t.Run("concurrent ownership", func(t *testing.T) {
		testConcurrentOwnership(t, ctx, store, databaseStore, owner.ID)
	})
	t.Run("repeatable read connection default", func(t *testing.T) {
		testRepeatableReadDefault(t, ctx, connection, databaseStore, owner.ID)
	})
}

func testContainerBinding(t *testing.T, ctx context.Context, store *capacity.Store, databaseStore *database.Store, ownerID string) {
	t.Helper()
	projectID := createProject(t, ctx, databaseStore, ownerID, "容器绑定项目")
	hostID := "binding-" + uuid.NewString()
	limits := capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4}
	setHost(t, ctx, store, hostID, limits)
	setProjectQuota(t, ctx, store, projectID, limits)

	binding := capacity.Binding{
		Environment: "integration",
		Project:     projectID.String(),
		Resource:    "resource-1",
		Generation:  1,
		Image:       "nginx:1.29.3-alpine",
	}
	executionID := uuid.New()
	request := capacity.Request{
		ExecutionID: executionID,
		HostID:      hostID,
		ProjectID:   projectID,
		Resources:   capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
		Binding:     binding,
	}
	reservation, created, err := store.Reserve(ctx, request)
	if err != nil || !created || reservation.Binding != binding || reservation.ContainerID != "" {
		t.Fatalf("首次绑定预留返回 %+v, %t, %v", reservation, created, err)
	}
	repeated, created, err := store.Reserve(ctx, request)
	if err != nil || created || !sameReservation(repeated, reservation) {
		t.Fatalf("重复绑定预留返回 %+v, %t, %v", repeated, created, err)
	}

	differentBinding := request
	differentBinding.Binding.Resource = "resource-2"
	if _, _, err := store.Reserve(ctx, differentBinding); !errors.Is(err, capacity.ErrIdempotencyConflict) {
		t.Fatalf("同一执行更换归属返回 %v，要求 ErrIdempotencyConflict", err)
	}
	otherExecution := request
	otherExecution.ExecutionID = uuid.New()
	if _, _, err := store.Reserve(ctx, otherExecution); !errors.Is(err, capacity.ErrOwnershipConflict) {
		t.Fatalf("不同执行复用归属返回 %v，要求 ErrOwnershipConflict", err)
	}

	const containerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	updated, err := store.SetContainerID(ctx, projectID, executionID, containerID)
	if err != nil || updated.ContainerID != containerID || updated.Binding != binding {
		t.Fatalf("记录容器标识返回 %+v, %v", updated, err)
	}
	if again, err := store.SetContainerID(ctx, projectID, executionID, containerID); err != nil || !sameReservation(again, updated) {
		t.Fatalf("重复记录容器标识返回 %+v, %v", again, err)
	}
	if _, err := store.SetContainerID(ctx, projectID, uuid.New(), containerID); !errors.Is(err, capacity.ErrReservationNotFound) {
		t.Fatalf("未知执行记录容器标识返回 %v，要求 ErrReservationNotFound", err)
	}
	if _, err := store.SetContainerID(ctx, uuid.New(), executionID, containerID); !errors.Is(err, capacity.ErrReservationNotFound) {
		t.Fatalf("错误项目记录容器标识返回 %v，要求 ErrReservationNotFound", err)
	}
	if _, err := store.SetContainerID(ctx, projectID, executionID, ""); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("空容器标识返回 %v，要求 fs.ErrInvalid", err)
	}

	// A reservation created without a binding can be bound on a later retry.
	legacyID := uuid.New()
	legacyRequest := capacity.Request{
		ExecutionID: legacyID,
		HostID:      hostID,
		ProjectID:   projectID,
		Resources:   capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
	}
	if _, created, err := store.Reserve(ctx, legacyRequest); err != nil || !created {
		t.Fatalf("无绑定预留返回 %t, %v", created, err)
	}
	if _, err := store.SetContainerID(ctx, projectID, legacyID, containerID); !errors.Is(err, capacity.ErrBindingNotFound) {
		t.Fatalf("无绑定预留记录容器返回 %v，要求 ErrBindingNotFound", err)
	}
	legacyRequest.Binding = capacity.Binding{
		Environment: "integration",
		Project:     projectID.String(),
		Resource:    "resource-2",
		Generation:  2,
		Image:       "nginx:1.29.3-alpine",
	}
	if bound, created, err := store.Reserve(ctx, legacyRequest); err != nil || created || bound.Binding != legacyRequest.Binding {
		t.Fatalf("补充绑定返回 %+v, %t, %v", bound, created, err)
	}

	if _, err := store.Release(ctx, projectID, executionID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetContainerID(ctx, projectID, executionID, containerID); !errors.Is(err, capacity.ErrExecutionReleased) {
		t.Fatalf("已释放预留记录容器返回 %v，要求 ErrExecutionReleased", err)
	}
	if released, err := store.GetReservation(ctx, projectID, executionID); err != nil ||
		released.State != capacity.Released || released.Binding != binding || released.ContainerID != containerID {
		t.Fatalf("释放后保留绑定返回 %+v, %v", released, err)
	}
	reused := request
	reused.ExecutionID = uuid.New()
	if _, created, err := store.Reserve(ctx, reused); err != nil || !created {
		t.Fatalf("释放后复用归属返回 %t, %v", created, err)
	}
	if _, err := store.Release(ctx, projectID, legacyID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release(ctx, projectID, reused.ExecutionID); err != nil {
		t.Fatal(err)
	}
}

func testConcurrentOwnership(t *testing.T, ctx context.Context, store *capacity.Store, databaseStore *database.Store, ownerID string) {
	t.Helper()
	projectID := createProject(t, ctx, databaseStore, ownerID, "并发归属项目")
	hostID := "ownership-" + uuid.NewString()
	limits := capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4}
	setHost(t, ctx, store, hostID, limits)
	setProjectQuota(t, ctx, store, projectID, limits)
	binding := capacity.Binding{
		Environment: "integration",
		Project:     projectID.String(),
		Resource:    "concurrent-resource",
		Generation:  1,
		Image:       "nginx:1.29.3-alpine",
	}

	const attempts = 8
	type result struct {
		reservation capacity.Reservation
		created     bool
		err         error
	}
	results := make([]result, attempts)
	start := make(chan struct{})
	var pending sync.WaitGroup
	for index := range results {
		pending.Go(func() {
			<-start
			results[index].reservation, results[index].created, results[index].err = store.Reserve(ctx, capacity.Request{
				ExecutionID: uuid.New(),
				HostID:      hostID,
				ProjectID:   projectID,
				Resources:   capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
				Binding:     binding,
			})
		})
	}
	close(start)
	pending.Wait()

	winner := -1
	for index, result := range results {
		if result.err == nil {
			if !result.created || winner >= 0 {
				t.Fatalf("并发归属申请产生多个成功结果: 索引 %d 与 %d", winner, index)
			}
			winner = index
			continue
		}
		if !errors.Is(result.err, capacity.ErrOwnershipConflict) {
			t.Fatalf("并发归属申请 %d 返回 %v，要求 ErrOwnershipConflict", index, result.err)
		}
	}
	if winner < 0 {
		t.Fatal("并发归属申请没有成功结果")
	}
	usage, err := store.Usage(ctx, hostID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if usage.HostUsed != (capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1}) {
		t.Fatalf("并发归属申请后主机占用错误: %+v", usage.HostUsed)
	}
	if _, err := store.Release(ctx, projectID, results[winner].reservation.ExecutionID); err != nil {
		t.Fatal(err)
	}
}

func testRepeatableReadDefault(t *testing.T, ctx context.Context, connection string, databaseStore *database.Store, ownerID string) {
	t.Helper()
	if err := databaseStore.DB.Exec("ALTER DATABASE cloudlet SET default_transaction_isolation = 'repeatable read'").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := databaseStore.DB.Exec("ALTER DATABASE cloudlet RESET default_transaction_isolation").Error; err != nil {
			t.Error(err)
		}
	})

	repeatableStore, err := database.Open(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer repeatableStore.SQL.Close()
	var isolation string
	if err := repeatableStore.DB.Raw("SHOW default_transaction_isolation").Scan(&isolation).Error; err != nil {
		t.Fatal(err)
	}
	if isolation != "repeatable read" {
		t.Fatalf("连接默认隔离级别为 %q，预期 repeatable read", isolation)
	}
	capacityStore, err := capacity.NewStore(repeatableStore.DB)
	if err != nil {
		t.Fatal(err)
	}
	testConcurrentLastCapacity(t, ctx, capacityStore, repeatableStore, ownerID)
}

func testProjectQuotaAcrossHosts(t *testing.T, ctx context.Context, store *capacity.Store, databaseStore *database.Store, ownerID string) {
	t.Helper()
	projectID := createProject(t, ctx, databaseStore, ownerID, "跨主机配额项目")
	hostA := "host-a-" + uuid.NewString()
	hostB := "host-b-" + uuid.NewString()
	limits := capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4}
	setHost(t, ctx, store, hostA, limits)
	setHost(t, ctx, store, hostB, limits)
	setProjectQuota(t, ctx, store, projectID, limits)

	if _, created, err := store.Reserve(ctx, capacity.Request{
		ExecutionID: uuid.New(), HostID: hostA, ProjectID: projectID,
		Resources: capacity.Resources{CPUMillis: 3000, MemoryBytes: 3 << 30, Instances: 3},
	}); err != nil || !created {
		t.Fatalf("主机 A 预留返回 %t, %v", created, err)
	}
	if _, _, err := store.Reserve(ctx, capacity.Request{
		ExecutionID: uuid.New(), HostID: hostB, ProjectID: projectID,
		Resources: capacity.Resources{CPUMillis: 2000, MemoryBytes: 2 << 30, Instances: 2},
	}); !errors.Is(err, capacity.ErrInsufficientCapacity) {
		t.Fatalf("跨主机超过项目配额返回 %v，要求 ErrInsufficientCapacity", err)
	}
	if _, created, err := store.Reserve(ctx, capacity.Request{
		ExecutionID: uuid.New(), HostID: hostB, ProjectID: projectID,
		Resources: capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
	}); err != nil || !created {
		t.Fatalf("主机 B 使用剩余项目配额返回 %t, %v", created, err)
	}
	usage, err := store.Usage(ctx, hostB, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if usage.HostUsed != (capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1}) ||
		usage.ProjectUsed != (capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4}) {
		t.Fatalf("跨主机项目占用错误: %+v", usage)
	}
	if _, err := store.SetProjectQuota(ctx, projectID, capacity.Resources{CPUMillis: 3000, MemoryBytes: 3 << 30, Instances: 3}); !errors.Is(err, capacity.ErrQuotaInUse) {
		t.Fatalf("跨主机降低项目配额返回 %v，要求 ErrQuotaInUse", err)
	}
}

func testConcurrentLastCapacity(t *testing.T, ctx context.Context, store *capacity.Store, databaseStore *database.Store, ownerID string) {
	t.Helper()
	projectA := createProject(t, ctx, databaseStore, ownerID, "并发项目 A")
	projectB := createProject(t, ctx, databaseStore, ownerID, "并发项目 B")
	hostID := "concurrent-" + uuid.NewString()
	hostCapacity := capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4}
	setHost(t, ctx, store, hostID, hostCapacity)
	for _, projectID := range []uuid.UUID{projectA, projectB} {
		setProjectQuota(t, ctx, store, projectID, hostCapacity)
	}
	pre := capacity.Request{
		ExecutionID: uuid.New(), HostID: hostID, ProjectID: projectA,
		Resources: capacity.Resources{CPUMillis: 3000, MemoryBytes: 3 << 30, Instances: 3},
	}
	if _, created, err := store.Reserve(ctx, pre); err != nil || !created {
		t.Fatalf("预占容量返回 %t, %v", created, err)
	}

	const attempts = 16
	type result struct {
		reservation capacity.Reservation
		created     bool
		err         error
	}
	results := make([]result, attempts)
	start := make(chan struct{})
	var pending sync.WaitGroup
	for index := range results {
		pending.Go(func() {
			<-start
			projectID := projectA
			if index%2 == 1 {
				projectID = projectB
			}
			results[index].reservation, results[index].created, results[index].err = store.Reserve(ctx, capacity.Request{
				ExecutionID: uuid.New(),
				HostID:      hostID,
				ProjectID:   projectID,
				Resources:   capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
			})
		})
	}
	close(start)
	pending.Wait()

	winner := -1
	for index, result := range results {
		switch {
		case result.err == nil:
			if !result.created || winner >= 0 {
				t.Fatalf("并发申请产生多个成功结果: 索引 %d 与 %d", winner, index)
			}
			winner = index
		case errors.Is(result.err, capacity.ErrInsufficientCapacity):
		default:
			t.Fatalf("并发申请返回 %v", result.err)
		}
	}
	if winner < 0 {
		t.Fatal("并发申请没有成功结果")
	}
	usage, err := store.Usage(ctx, hostID, projectA)
	if err != nil {
		t.Fatal(err)
	}
	if usage.HostUsed != (capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4}) {
		t.Fatalf("并发申请后主机占用错误: %+v", usage.HostUsed)
	}

	// The next request succeeds only after the winner releases its capacity.
	next := capacity.Request{
		ExecutionID: uuid.New(), HostID: hostID, ProjectID: projectB,
		Resources: capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
	}
	if _, _, err := store.Reserve(ctx, next); !errors.Is(err, capacity.ErrInsufficientCapacity) {
		t.Fatalf("释放前申请返回 %v，要求 ErrInsufficientCapacity", err)
	}
	if _, err := store.Release(ctx, results[winner].reservation.ProjectID, results[winner].reservation.ExecutionID); err != nil {
		t.Fatal(err)
	}
	next.ProjectID = projectA
	if results[winner].reservation.ProjectID == projectA {
		next.ProjectID = projectB
	}
	if _, created, err := store.Reserve(ctx, next); err != nil || !created {
		t.Fatalf("释放后重新申请返回 %t, %v", created, err)
	}
}

func testConcurrentDuplicate(t *testing.T, ctx context.Context, store *capacity.Store, databaseStore *database.Store, ownerID string) {
	t.Helper()
	projectID := createProject(t, ctx, databaseStore, ownerID, "幂等项目")
	hostID := "duplicate-" + uuid.NewString()
	hostCapacity := capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4}
	setHost(t, ctx, store, hostID, hostCapacity)
	setProjectQuota(t, ctx, store, projectID, hostCapacity)

	request := capacity.Request{
		ExecutionID: uuid.New(), HostID: hostID, ProjectID: projectID,
		Resources: capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
	}
	const attempts = 16
	type result struct {
		reservation capacity.Reservation
		created     bool
		err         error
	}
	results := make([]result, attempts)
	start := make(chan struct{})
	var pending sync.WaitGroup
	for index := range results {
		pending.Go(func() {
			<-start
			results[index].reservation, results[index].created, results[index].err = store.Reserve(ctx, request)
		})
	}
	close(start)
	pending.Wait()

	createdCount := 0
	for _, result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.created {
			createdCount++
		}
		if !sameReservation(result.reservation, results[0].reservation) {
			t.Fatalf("并发重复申请返回不同预留: %+v 与 %+v", results[0].reservation, result.reservation)
		}
	}
	if createdCount != 1 {
		t.Fatalf("并发重复申请创建了 %d 条预留，预期 1 条", createdCount)
	}
	usage, err := store.Usage(ctx, hostID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if usage.HostUsed != request.Resources || usage.ProjectUsed != request.Resources {
		t.Fatalf("并发重复申请后的占用错误: %+v", usage)
	}
}

func sameReservation(left, right capacity.Reservation) bool {
	if left.ExecutionID != right.ExecutionID || left.HostID != right.HostID ||
		left.ProjectID != right.ProjectID || left.Resources != right.Resources ||
		left.Binding != right.Binding || left.ContainerID != right.ContainerID ||
		left.State != right.State || !left.CreatedAt.Equal(right.CreatedAt) {
		return false
	}
	if (left.ReleasedAt == nil) != (right.ReleasedAt == nil) {
		return false
	}
	return left.ReleasedAt == nil || left.ReleasedAt.Equal(*right.ReleasedAt)
}

func setHost(t *testing.T, ctx context.Context, store *capacity.Store, hostID string, resources capacity.Resources) {
	t.Helper()
	if _, err := store.SetHostCapacity(ctx, hostID, resources); err != nil {
		t.Fatal(err)
	}
}

func setProjectQuota(t *testing.T, ctx context.Context, store *capacity.Store, projectID uuid.UUID, resources capacity.Resources) {
	t.Helper()
	if _, err := store.SetProjectQuota(ctx, projectID, resources); err != nil {
		t.Fatal(err)
	}
}

func createProject(t *testing.T, ctx context.Context, store *database.Store, ownerID, name string) uuid.UUID {
	t.Helper()
	project, err := store.CreateProject(ctx, ownerID, name)
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := uuid.Parse(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	return projectID
}

func assertUsage(t *testing.T, ctx context.Context, store *capacity.Store, hostID string, projectID uuid.UUID, expected capacity.Usage) {
	t.Helper()
	actual, err := store.Usage(ctx, hostID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("容量占用返回 %+v，预期 %+v", actual, expected)
	}
}

func assertProjectUsage(t *testing.T, ctx context.Context, store *capacity.Store, projectID uuid.UUID, expected capacity.ProjectUsage) {
	t.Helper()
	actual, err := store.ProjectUsage(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("项目容量占用返回 %+v，预期 %+v", actual, expected)
	}
}

func testUnconfiguredBudgets(t *testing.T, ctx context.Context, store *capacity.Store, databaseStore *database.Store, ownerID string) {
	t.Helper()
	projectID := createProject(t, ctx, databaseStore, ownerID, "未配置预算项目")
	request := capacity.Request{
		ExecutionID: uuid.New(), HostID: "missing-" + uuid.NewString(), ProjectID: projectID,
		Resources: capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
	}
	if _, _, err := store.Reserve(ctx, request); !errors.Is(err, capacity.ErrHostNotFound) {
		t.Fatalf("未配置主机容量返回 %v，要求 ErrHostNotFound", err)
	}
	setHost(t, ctx, store, request.HostID, capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4})
	if _, _, err := store.Reserve(ctx, request); !errors.Is(err, capacity.ErrProjectQuotaNotFound) {
		t.Fatalf("未配置项目配额返回 %v，要求 ErrProjectQuotaNotFound", err)
	}
	if _, err := store.Usage(ctx, request.HostID, projectID); !errors.Is(err, capacity.ErrProjectQuotaNotFound) {
		t.Fatalf("未配置项目配额查询占用返回 %v，要求 ErrProjectQuotaNotFound", err)
	}
	if _, err := store.ProjectUsage(ctx, projectID); !errors.Is(err, capacity.ErrProjectQuotaNotFound) {
		t.Fatalf("未配置项目配额查询项目占用返回 %v，要求 ErrProjectQuotaNotFound", err)
	}
	if _, err := store.SetProjectQuota(ctx, uuid.New(), capacity.Resources{CPUMillis: 1, MemoryBytes: 1, Instances: 1}); !errors.Is(err, capacity.ErrProjectNotFound) {
		t.Fatalf("未知项目配置配额返回 %v，要求 ErrProjectNotFound", err)
	}
}
