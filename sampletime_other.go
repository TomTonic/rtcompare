//go:build !windows

package rtcompare

import "time"

// timeStamp is a reading of the monotonic clock. Readings are only comparable
// within one run of a program.
type timeStamp = time.Time

// sampleTime reads the clock with the finest resolution available here.
func sampleTime() timeStamp {
	return time.Now()
}

// diffTimeStamps returns the time from earlier to later in nanoseconds,
// negative if later is in fact earlier.
func diffTimeStamps(earlier, later timeStamp) int64 {
	return later.Sub(earlier).Nanoseconds()
}
