//go:build !windows

package rtcompare

import "time"

//import "golang.org/x/sys/unix"

type TimeStamp = time.Time

// Now returns current TimeStamp with best possible precision.
//
// Now returns time offset from a specific time.
// The values aren't comparable between computer restarts or between computers.
func SampleTime() TimeStamp {
	//return time.Now().UnixNano()
	return time.Now()
	/*
		var ts unix.Timespec
		unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
		nanos := ts.Nano()
		return nanos
	*/
}

// Retruns the difference between two timestams in nanoseconds with the highest possible precision (which might be more than just a nanosecond).
func DiffTimeStamps(t_earlier, t_later TimeStamp) int64 {
	result := t_later.Sub(t_earlier)
	return result.Nanoseconds()
}
