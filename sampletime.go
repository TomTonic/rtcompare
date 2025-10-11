package rtcompare

import (
	"math"
)

const iterationsForCallibration = 10_000_000

var (
	// precision holds the precision of time measurements obtained via SampleTime() on the runtime system in nanoseconds.
	precision = int64(-1)
)

// Returns the precision of time measurements obtained via SampleTime() on the runtime system in nanoseconds.
// Should return 100ns on Windows systems, and typically between 20ns and 100ns on Linux and MacOS systems.
func GetSampleTimePrecision() int64 {
	if precision == int64(-1) {
		precision = calcMinTimeSample()
	}
	return precision
}

func calcMinTimeSample() int64 {
	var minDiff = int64(math.MaxInt64) // initial large value
	for range iterationsForCallibration {
		t1 := SampleTime()
		t2 := SampleTime()
		diff := DiffTimeStamps(t1, t2)
		if diff > 0 && diff < minDiff {
			minDiff = diff
		}
	}
	return minDiff
}
