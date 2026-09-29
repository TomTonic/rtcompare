//go:build windows

package rtcompare

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// timeStamp is a reading of QueryPerformanceCounter, or of the monotonic
// clock in nanoseconds where that is unavailable. Readings are only
// comparable within one run of a program.
type timeStamp = int64

var (
	modkernel32 = windows.NewLazySystemDLL("kernel32.dll")
	procFreq    = modkernel32.NewProc("QueryPerformanceFrequency")
	procCounter = modkernel32.NewProc("QueryPerformanceCounter")

	// qpcFrequency is QueryPerformanceCounter's frequency in ticks per
	// second, or zero if it could not be read.
	qpcFrequency = getFrequency()

	// processStart anchors the fallback readings.
	processStart = time.Now()
)

// getFrequency returns QueryPerformanceCounter's frequency, or zero if the
// call fails, in which case sampleTime falls back to the monotonic clock of
// package time rather than failing at package initialisation. The call does
// not fail on any Windows since XP.
func getFrequency() int64 {
	var freq int64
	if r1, _, _ := procFreq.Call(uintptr(unsafe.Pointer(&freq))); r1 == 0 {
		return 0
	}
	return freq
}

// sampleTime reads the clock with the finest resolution available here.
func sampleTime() timeStamp {
	if qpcFrequency == 0 {
		return int64(time.Since(processStart))
	}
	var qpc int64
	_, _, _ = procCounter.Call(uintptr(unsafe.Pointer(&qpc)))
	return qpc
}

// diffTimeStamps returns the time from earlier to later in nanoseconds,
// negative if later is in fact earlier. It has constant runtime but
// contains an integer division.
func diffTimeStamps(earlier, later timeStamp) int64 {
	if qpcFrequency == 0 {
		return later - earlier
	}
	return (later - earlier) * 1_000_000_000 / qpcFrequency
}
