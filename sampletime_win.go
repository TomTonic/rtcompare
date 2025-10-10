//go:build windows

package rtcompare

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

type TimeStamp = int64

var (
	modkernel32 = windows.NewLazySystemDLL("kernel32.dll")
	procFreq    = modkernel32.NewProc("QueryPerformanceFrequency")
	procCounter = modkernel32.NewProc("QueryPerformanceCounter")

	qpcFrequency = getFrequency()
	qpcBase      = SampleTime()
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

func SampleTime() TimeStamp {
	var qpc int64
	procCounter.Call(uintptr(unsafe.Pointer(&qpc)))
	return qpc
}

// Retruns the difference between two timestams in nanoseconds with the highest possible precision (which might be more than just one nanosecond).
func DiffTimeStamps(t_earlier, t_later TimeStamp) int64 {
	result := t_later - t_earlier
	result *= int64(1_000_000_000) // ns per sec
	result /= qpcFrequency
	return result
}
