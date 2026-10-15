//go:build !windows

package rtcompare

import "time"

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
type TimeStamp struct {
	t time.Time
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
func SampleTime() TimeStamp {
	return TimeStamp{t: time.Now()}
}

// DiffTimeStamps returns the time from tEarlier to tLater in nanoseconds.
//
// Both parameters must come from [SampleTime] in the same run of the program.
// If tLater was in fact taken before tEarlier, the result is negative; zero
// means both readings fell into the same tick of the clock, not that no time
// passed. The result is exact to the resolution reported by
// [GetSampleTimePrecision] and no finer.
//
// Use it right after the second [SampleTime] call, as shown there. On this
// platform it is a subtraction of two time values and has no fixed cost
// guarantee; on Windows it is an integer multiplication and division.
func DiffTimeStamps(tEarlier, tLater TimeStamp) int64 {
	return tLater.t.Sub(tEarlier.t).Nanoseconds()
}
