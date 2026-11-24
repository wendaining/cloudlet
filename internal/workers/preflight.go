package workers

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

const (
	preflightTimeout = 30 * time.Second
	maxPreflightLog  = 16 << 10
)

// PreflightPhase identifies the runtime stage reached by a Worker module.
type PreflightPhase string

const (
	// PreflightLoad reports a module that workerd could not parse or load.
	PreflightLoad PreflightPhase = "load"
	// PreflightInitialize reports a module that loaded but failed while its
	// top-level code ran.
	PreflightInitialize PreflightPhase = "initialize"
	// PreflightReady reports a module that loaded and initialized.
	PreflightReady PreflightPhase = "ready"
)

// PreflightResult reports the reached phase and the bounded workerd output.
type PreflightResult struct {
	Phase PreflightPhase
	Logs  string
}

// PreflightError reports a failed preflight with its runtime phase.
type PreflightError struct {
	Phase PreflightPhase
	Logs  string
	Err   error
}

func (e *PreflightError) Error() string {
	logs := strings.TrimSpace(e.Logs)
	if logs == "" {
		return fmt.Sprintf("Worker preflight failed during %s: %v", e.Phase, e.Err)
	}
	return fmt.Sprintf("Worker preflight failed during %s: %v\n%s", e.Phase, e.Err, logs)
}

func (e *PreflightError) Unwrap() error {
	return e.Err
}

// Preflight runs the prepared module with the pinned workerd image and checks
// that it loads and initializes. It returns PreflightReady on success and a
// PreflightError carrying PreflightLoad or PreflightInitialize on failure.
func Preflight(ctx context.Context, docker *client.Client, prepared RuntimeConfig) (result PreflightResult, err error) {
	if docker == nil {
		return PreflightResult{}, fmt.Errorf("preflight Worker %q: Docker client is required: %w", prepared.Entry, fs.ErrInvalid)
	}
	if prepared.Entry == "" || len(prepared.Module) == 0 {
		return PreflightResult{}, fmt.Errorf("preflight Worker %q: prepared module is required: %w", prepared.Entry, fs.ErrInvalid)
	}
	if err := validateCompatibilityDate(prepared.CompatibilityDate, time.Now()); err != nil {
		return PreflightResult{}, fmt.Errorf("preflight Worker %q: %w", prepared.Entry, err)
	}

	ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	config := preflightConfig(prepared.CompatibilityDate)
	archive, err := preflightArchive(map[string][]byte{
		"config.capnp": config,
		"wrapper.mjs":  []byte(preflightWrapper),
		"user.mjs":     prepared.Module,
	})
	if err != nil {
		return PreflightResult{}, fmt.Errorf("preflight Worker %q: build container archive: %w", prepared.Entry, err)
	}

	name := "cloudlet-preflight-" + uuid.NewString()
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image: RuntimeImage,
			Cmd:   []string{"test", ConfigPath},
			Tty:   true,
			Labels: map[string]string{
				"io.cloudlet.preflight": "true",
			},
		},
		HostConfig: &container.HostConfig{
			NetworkMode: network.NetworkNone,
			RestartPolicy: container.RestartPolicy{
				Name: container.RestartPolicyDisabled,
			},
			Resources: container.Resources{
				NanoCPUs:  1_000_000_000,
				Memory:    256 << 20,
				PidsLimit: pointer(int64(128)),
			},
		},
	})
	if err != nil {
		return PreflightResult{}, fmt.Errorf("preflight Worker %q: create container %s: %w", prepared.Entry, name, err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cleanupCancel()
		if _, removeErr := docker.ContainerRemove(cleanupCtx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); removeErr != nil && !cerrdefs.IsNotFound(removeErr) {
			err = errors.Join(err, fmt.Errorf("preflight Worker %q: remove container %s: %w", prepared.Entry, created.ID, removeErr))
		}
	}()

	if _, err := docker.CopyToContainer(ctx, created.ID, client.CopyToContainerOptions{
		DestinationPath: "/",
		Content:         bytes.NewReader(archive),
	}); err != nil {
		return PreflightResult{}, fmt.Errorf("preflight Worker %q: copy runtime files into container %s: %w", prepared.Entry, created.ID, err)
	}

	wait := docker.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return PreflightResult{}, fmt.Errorf("preflight Worker %q: start container %s: %w", prepared.Entry, created.ID, err)
	}

	var status int64
	select {
	case result := <-wait.Result:
		status = result.StatusCode
		if result.Error != nil {
			return PreflightResult{}, fmt.Errorf("preflight Worker %q: container %s exited with error: %s", prepared.Entry, created.ID, result.Error.Message)
		}
	case waitErr := <-wait.Error:
		return PreflightResult{}, fmt.Errorf("preflight Worker %q: wait for container %s: %w", prepared.Entry, created.ID, waitErr)
	case <-ctx.Done():
		logs, logErr := readPreflightLogs(context.WithoutCancel(ctx), docker, created.ID)
		if logErr != nil {
			return PreflightResult{}, fmt.Errorf("preflight Worker %q: container %s did not finish and its logs are unavailable: %w", prepared.Entry, created.ID, errors.Join(ctx.Err(), logErr))
		}
		return PreflightResult{Phase: PreflightInitialize, Logs: logs.diagnostics()}, &PreflightError{
			Phase: PreflightInitialize,
			Logs:  logs.diagnostics(),
			Err:   fmt.Errorf("container %s did not finish: %w", created.ID, ctx.Err()),
		}
	}

	logs, err := readPreflightLogs(ctx, docker, created.ID)
	if err != nil {
		return PreflightResult{}, fmt.Errorf("preflight Worker %q: read container %s logs: %w", prepared.Entry, created.ID, err)
	}
	result = logs.result(status)
	if result.Phase != PreflightReady {
		return result, &PreflightError{
			Phase: result.Phase,
			Logs:  result.Logs,
			Err:   fmt.Errorf("workerd exited with status %d", status),
		}
	}
	return result, nil
}

func readPreflightLogs(ctx context.Context, docker *client.Client, id string) (*preflightLogCapture, error) {
	logs, err := docker.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		return nil, err
	}
	defer logs.Close()
	capture := &preflightLogCapture{}
	if _, err := io.Copy(capture, logs); err != nil {
		return nil, err
	}
	return capture, nil
}

// preflightLogCapture tracks markers across every read while retaining only
// the diagnostic tail. A marker may span two reads or precede discarded logs.
type preflightLogCapture struct {
	phase     PreflightPhase
	window    string
	tail      []byte
	truncated bool
}

func (c *preflightLogCapture) Write(data []byte) (int, error) {
	window := c.window + string(data)
	last := -1
	for _, marker := range []struct {
		text  string
		phase PreflightPhase
	}{
		{"cloudlet-preflight:load:", PreflightLoad},
		{"cloudlet-preflight:initialize:", PreflightInitialize},
		{"cloudlet-preflight:ready", PreflightReady},
	} {
		if index := strings.LastIndex(window, marker.text); index > last {
			last = index
			c.phase = marker.phase
		}
	}
	const overlap = len("cloudlet-preflight:initialize:") - 1
	c.window = window[max(0, len(window)-overlap):]
	if len(c.tail)+len(data) > maxPreflightLog {
		c.truncated = true
	}
	if len(data) >= maxPreflightLog {
		c.tail = append(c.tail[:0], data[len(data)-maxPreflightLog:]...)
	} else {
		overflow := max(0, len(c.tail)+len(data)-maxPreflightLog)
		copy(c.tail, c.tail[overflow:])
		c.tail = append(c.tail[:len(c.tail)-overflow], data...)
	}
	return len(data), nil
}

func (c *preflightLogCapture) result(status int64) PreflightResult {
	phase := c.phase
	if status == 0 {
		phase = PreflightReady
	} else if phase == PreflightReady {
		phase = PreflightInitialize
	} else if phase == "" {
		phase = PreflightLoad
	}
	return PreflightResult{Phase: phase, Logs: c.diagnostics()}
}

func (c *preflightLogCapture) diagnostics() string {
	if c.truncated {
		return "[earlier output truncated]\n" + string(c.tail)
	}
	return string(c.tail)
}

func preflightConfig(compatibilityDate string) []byte {
	return fmt.Appendf(nil, preflightConfigTemplate, compatibilityDate)
}

func preflightArchive(files map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, name := range names {
		content := files[name]
		header := &tar.Header{
			Name: "worker/" + name,
			Mode: 0o444,
			Size: int64(len(content)),
		}
		if err := writer.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := writer.Write(content); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return archive.Bytes(), nil
}

func pointer[T any](value T) *T {
	return &value
}

const preflightWrapper = `export default {
  async test() {
    console.error("cloudlet-preflight:loading");
    try {
      await import("./user.mjs");
    } catch (error) {
      const phase = typeof error?.message === "string" && error.message.startsWith('No such module "')
        ? "load" : "initialize";
      console.error("cloudlet-preflight:" + phase + ":" + (error?.stack ?? error));
      throw error;
    }
    console.error("cloudlet-preflight:ready");
  }
};
`

const preflightConfigTemplate = `using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [(name = "worker", worker = .worker)],
);

const worker :Workerd.Worker = (
  modules = [
    (name = "wrapper.mjs", esModule = embed "wrapper.mjs"),
    (name = "user.mjs", esModule = embed "user.mjs"),
  ],
  compatibilityDate = "%s",
);
`
