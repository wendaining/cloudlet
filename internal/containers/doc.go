// Package containers manages Docker Engine container lifecycles for platform
// execution units. It prepares images from immutable digest references, creates
// containers with deterministic names and ownership labels, starts and observes
// them, and stops or removes only containers that belong to the requested
// environment, project, resource and execution generation.
//
// AccessPublished publishes the container port on the host loopback interface
// for local development. AccessNetwork joins an existing user-defined bridge
// network and exposes the container port only to peers on that network. The
// adapter observes the actual published loopback port or the internal network
// endpoint, including a stable DNS alias for network peers.
package containers
