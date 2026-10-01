//go:build !unix

package main

// privateUmask is a no-op where there is no umask (Windows); the data directory's ACLs apply.
func privateUmask() {}
