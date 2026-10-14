//go:build js

package app

// The edge edition runs no background loops: a Worker has no process to keep them in, and EdgeTick does the scheduled
// work of each (see edgeTickJobs). Naming no service here keeps every service's Run loop, and what only the loops
// use, out of panel.wasm. cmd/mistgate-edge never starts Panel.BackgroundJobs.
func backgroundJobs(c Config, _ jobServices) []BackgroundJob { return c.BackgroundStarters }
