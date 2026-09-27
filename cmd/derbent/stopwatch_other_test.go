//go:build !windows

package main

import "time"

// stopwatch starts timing and returns a function that reads the time since.
func stopwatch() func() time.Duration {
	start := time.Now()
	return func() time.Duration { return time.Since(start) }
}
