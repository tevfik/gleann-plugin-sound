//go:build windows

package main

// acquireDictateLock is a no-op on Windows.
func acquireDictateLock() (func(), error) { return func() {}, nil }
