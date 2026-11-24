// Package containers manages Docker Engine container lifecycles for platform
// execution units. It prepares images from immutable digest references, creates
// containers with deterministic names and ownership labels, starts and observes
// them, reads the published loopback port, and stops or removes only containers
// that belong to the requested environment, project, resource and execution
// generation. Platform-prepared regular files are copied as read-only files
// before startup. Their archive digest is recorded with the container so the
// execution service rejects adoption with different code or configuration.
package containers
