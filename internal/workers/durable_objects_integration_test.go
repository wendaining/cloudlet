//go:build integration

package workers_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/wendaining/cloudlet/internal/durableobjects"
	"github.com/wendaining/cloudlet/internal/workers"
)

const sqliteWorker = `import { DurableObject } from "cloudflare:workers";

export class ObjectStore extends DurableObject {
  constructor(ctx, env) {
    super(ctx, env);
    this.sql = ctx.storage.sql;
    ctx.blockConcurrencyWhile(async () => {
      this.sql.exec("CREATE TABLE IF NOT EXISTS state (key TEXT PRIMARY KEY, value TEXT NOT NULL)");
    });
  }

  setValue(value) {
    this.sql.exec("INSERT OR REPLACE INTO state (key, value) VALUES (?, ?)", "value", value);
  }

  getValue() {
    return this.sql.exec("SELECT value FROM state WHERE key = ?", "value").toArray()[0]?.value ?? null;
  }
}

export default {
  async fetch(request, env) {
    const name = new URL(request.url).pathname.slice(1);
    if (name === "ready") return new Response("ready");

    const id = env.STORE.idFromName(name);
    const object = env.STORE.get(id);
    if (request.method === "PUT") await object.setValue(await request.text());
    return Response.json({ id: id.toString(), value: await object.getValue() });
  }
};`

func TestWorkerdPersistsDurableObjectSQLite(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	entry := "dist/index.mjs"
	namespace := durableobjects.Namespace{ID: uuid.New(), ClassName: "ObjectStore", Bindings: []string{"STORE"}}
	prepared, err := workers.PrepareWithDurableObjects(writeBundle(t, entry, []byte(sqliteWorker)), entry, compatibilityDate,
		[]durableobjects.Namespace{namespace})
	if err != nil {
		t.Fatal(err)
	}

	volume := "cloudlet-workerd-" + uuid.NewString()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := testcontainers.NewTerminateOptions(cleanupCtx, testcontainers.RemoveVolumes(volume)).Cleanup(); err != nil {
			t.Error(err)
		}
	})

	port := strconv.Itoa(workers.HTTPPort) + "/tcp"
	start := func() (*testcontainers.DockerContainer, string) {
		container := runWorkerd(t, ctx, prepared,
			testcontainers.WithExposedPorts(port),
			testcontainers.WithMounts(testcontainers.VolumeMount(volume, durableobjects.StoragePath)),
			testcontainers.WithWaitStrategy(wait.ForHTTP("/ready").WithPort(port)),
		)
		endpoint, err := container.PortEndpoint(ctx, port, "http")
		if err != nil {
			t.Fatal(err)
		}
		return container, endpoint
	}

	container, endpoint := start()
	client := &http.Client{Timeout: 5 * time.Second}
	call := func(endpoint, method, name, body string) objectValue {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, endpoint+"/"+name, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			detail, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			t.Fatalf("%s %s returned HTTP %d: %s", method, name, resp.StatusCode, detail)
		}
		var result objectValue
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result.ID == "" {
			t.Fatalf("%s %s returned an empty object ID", method, name)
		}
		return result
	}

	alpha := call(endpoint, http.MethodPut, "alpha", "first")
	if beta := call(endpoint, http.MethodGet, "beta", ""); beta.ID == alpha.ID || beta.Value != nil {
		t.Fatalf("second object shares state with first object: %+v, %+v", alpha, beta)
	}
	beta := call(endpoint, http.MethodPut, "beta", "second")
	assertObject(t, call(endpoint, http.MethodGet, "alpha", ""), alpha.ID, "first")
	assertObject(t, call(endpoint, http.MethodGet, "beta", ""), beta.ID, "second")

	// Stop the only writer before attaching its volume to a new process.
	if err := container.Stop(ctx, nil); err != nil {
		t.Fatal(err)
	}
	_, restartedEndpoint := start()
	assertObject(t, call(restartedEndpoint, http.MethodGet, "alpha", ""), alpha.ID, "first")
	assertObject(t, call(restartedEndpoint, http.MethodGet, "beta", ""), beta.ID, "second")
	t.Logf("workerd %s preserved distinct object IDs and SQLite values after restart", workers.RuntimeVersion)
}

type objectValue struct {
	ID    string
	Value *string
}

func assertObject(t *testing.T, result objectValue, id, value string) {
	t.Helper()
	if result.ID != id {
		t.Fatalf("object ID = %q, want %q", result.ID, id)
	}
	if result.Value == nil {
		t.Fatalf("object %s has no value, want %q", id, value)
	}
	if *result.Value != value {
		t.Fatalf("object %s value = %q, want %q", id, *result.Value, value)
	}
}
