// Package containers manages Docker Engine container lifecycles for platform
// execution units. It prepares images from immutable digest references, creates
// containers with deterministic names and ownership labels, starts and observes
// them, reads the published loopback port, and probes HTTP readiness through
// that endpoint within a bounded deadline. It stops or removes only containers
// that belong to the requested environment, project, resource and execution
// generation. Platform-prepared regular files are copied as read-only files
// before startup. Their archive digest is recorded with the container so the
// execution service rejects adoption with different code or configuration.
//
// AccessPublished publishes the container port on the host loopback interface
// for local development. AccessNetwork joins an existing user-defined bridge
// network and exposes the container port only to peers on that network. The
// adapter observes the actual published loopback port or the internal network
// endpoint, including a stable DNS alias for network peers.
//
// ProbeHostLimits reads Docker Engine information and reports the host support
// scope for the CPU, memory, PIDs and open-file limits applied by the adapter.
// Acceptance tests use bounded workloads to verify the actual trigger and
// cleanup results.
package containers
