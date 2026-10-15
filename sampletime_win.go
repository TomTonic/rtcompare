//go:build windows

package rtcompare

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TimeStamp is an opaque reading of the system's monotonic clock, as returned
// by [SampleTime]. It exists only to be handed to [DiffTimeStamps]; its
// contents are deliberately not accessible and differ between platforms.
//
// Readings are only comparable with each other within one run of a program on
// one machine. They carry no meaning across process restarts, reboots or
// computers, and must not be stored or transmitted. The zero value is not a
// valid reading.
//
// Because the type is opaque, code that uses only [SampleTime] and
// [DiffTimeStamps] behaves identically on every platform. TimeStamp values
// are small and may be copied freely.
//
// On Windows a reading is a QueryPerformanceCounter tick count, or, in the
// unlikely case that the counter's frequency cannot be read, nanoseconds of
// the monotonic clock of package time since process start.
type TimeStamp struct {
	ticks int64
}

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
// call fails, in which case SampleTime falls back to the monotonic clock of
// package time rather than failing at package initialisation. The call does
// not fail on any Windows since XP.
func getFrequency() int64 {
	var freq int64
	if r1, _, _ := procFreq.Call(uintptr(unsafe.Pointer(&freq))); r1 == 0 {
		return 0
	}
	return freq
}

// SampleTime returns a [TimeStamp] taken from the finest-grained monotonic
// clock available on the running system.
//
// It takes no parameters and cannot fail. Use it to bracket a piece of work
// and pass both readings to [DiffTimeStamps] to obtain the elapsed time:
//
//	start := rtcompare.SampleTime()
//	work()
//	elapsed := rtcompare.DiffTimeStamps(start, rtcompare.SampleTime())
//
// This is the primitive underneath [Collect]; call it directly only when you
// need to do your own timing. A single reading cannot be resolved below
// [GetSampleTimePrecision], so for work shorter than a few multiples of that
// value, time a batch of repetitions instead of one call. The call itself
// costs time too, which is part of what a measurement of very short work
// contains.
//
// On Windows the reading comes from QueryPerformanceCounter, which does not
// suffer from the coarse 15.6 ms granularity of the system time.
func SampleTime() TimeStamp {
	if qpcFrequency == 0 {
		return TimeStamp{ticks: int64(time.Since(processStart))}
	}
	var qpc int64
	_, _, _ = procCounter.Call(uintptr(unsafe.Pointer(&qpc)))
	return TimeStamp{ticks: qpc}
}

// DiffTimeStamps returns the time from tEarlier to tLater in nanoseconds.
//
// Both parameters must come from [SampleTime] in the same run of the program.
// If tLater was in fact taken before tEarlier, the result is negative; zero
// means both readings fell into the same tick of the clock, not that no time
// passed. The result is exact to the resolution reported by
// [GetSampleTimePrecision] and no finer.
//
// Use it right after the second [SampleTime] call, as shown there. On Windows
// it has constant runtime but contains an integer division; on other
// platforms it is a subtraction of two time values with no fixed cost
// guarantee.
func DiffTimeStamps(tEarlier, tLater TimeStamp) int64 {
	if qpcFrequency == 0 {
		return tLater.ticks - tEarlier.ticks
	}
	return (tLater.ticks - tEarlier.ticks) * 1_000_000_000 / qpcFrequency
}
