// Package containers manages Docker Engine container lifecycles for platform
// execution units. It creates containers with deterministic names and ownership
// labels, starts and observes them, reads the published loopback port, waits for
// HTTP readiness through the actual endpoint within a bounded deadline, and
// stops or removes only containers that belong to the requested environment,
// project, resource and execution generation.
package containers
