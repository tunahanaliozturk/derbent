package main

import (
	"syscall"
	"time"
	"unsafe"
)

// On Windows the monotonic clock behind time.Since moves with the system timer interrupt, every 0.5 to
// 15.6 ms, which is coarser than one MCP call. The performance counter, which the testing package also
// uses for ns/op, resolves well under a microsecond.
var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	qpc      = kernel32.NewProc("QueryPerformanceCounter")
	perfFreq = func() int64 {
		var f int64
		_, _, _ = kernel32.NewProc("QueryPerformanceFrequency").Call(uintptr(unsafe.Pointer(&f)))
		return f
	}()
)

func perfCounter() int64 {
	var n int64
	_, _, _ = qpc.Call(uintptr(unsafe.Pointer(&n)))
	return n
}

// stopwatch starts timing and returns a function that reads the time since.
func stopwatch() func() time.Duration {
	start := perfCounter()
	return func() time.Duration {
		d := perfCounter() - start
		// Whole seconds and the remainder separately, so d*1e9 cannot overflow on a long span.
		return time.Duration(d/perfFreq*int64(time.Second) + d%perfFreq*int64(time.Second)/perfFreq)
	}
}
