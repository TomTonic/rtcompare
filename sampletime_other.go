//go:build !windows

package rtcompare

import "time"

//import "golang.org/x/sys/unix"

// A relative TimeStamp with the highest possible precision on the current runtime system.
// The values aren't comparable between computer restarts or between computers.
// They are only comparable on the same computer between two calls to SampleTime() within the same runtime of a program.
type TimeStamp = time.Time

// SampleTime returns a timestamp with the highest possible precision on the current runtime system.
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

// Retruns the difference between two timestams in nanoseconds with the highest possible precision (which might be more than just one nanosecond).
// The function assumes that t_later is later than t_earlier and will return a negative value if this is not the case.
// Please note that the call to this function does NOT have constant runtime on other systems but Windows.
func DiffTimeStamps(t_earlier, t_later TimeStamp) int64 {
	result := t_later.Sub(t_earlier)
	return result.Nanoseconds()
}
