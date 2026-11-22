// Package containers manages Docker Engine container lifecycles for platform
// execution units. It creates containers with ownership labels, starts and
// observes them, reads the published loopback port, and stops or removes only
// containers that belong to the requested environment, project, resource and
// execution generation.
package containers
