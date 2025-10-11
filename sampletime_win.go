//go:build windows

package rtcompare

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A relative TimeStamp with the highest possible precision on the current runtime system.
// The values aren't comparable between computer restarts or between computers.
// They are only comparable on the same computer between two calls to SampleTime() within the same runtime of a program.
type TimeStamp = int64

var (
	modkernel32 = windows.NewLazySystemDLL("kernel32.dll")
	procFreq    = modkernel32.NewProc("QueryPerformanceFrequency")
	procCounter = modkernel32.NewProc("QueryPerformanceCounter")

	qpcFrequency = getFrequency()
)

// getFrequency returns frequency in ticks per second.
func getFrequency() int64 {
	var freq int64
	r1, _, err := procFreq.Call(uintptr(unsafe.Pointer(&freq)))
	if r1 == 0 {
		panic(fmt.Sprintf("call failed: %v", err))
	}
	return freq
}

// SampleTime returns a timestamp with the highest possible precision on the current runtime system.
func SampleTime() TimeStamp {
	var qpc int64
	procCounter.Call(uintptr(unsafe.Pointer(&qpc)))
	return qpc
}

// Retruns the difference between two timestams in nanoseconds with the highest possible precision (which might be more than just one nanosecond).
// The function assumes that t_later is later than t_earlier and will return a negative value if this is not the case.
// Please note that the call to this function has constant runtime but contains an integer division operation on Windows.
func DiffTimeStamps(t_earlier, t_later TimeStamp) int64 {
	result := t_later - t_earlier
	result *= int64(1_000_000_000) // ns per sec
	result /= qpcFrequency
	return result
}
