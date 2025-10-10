package rtcompare

import (
	"math"
	"runtime"
	"runtime/debug"
)

const iterationsForCallibration = 10_000_000

var (
	mts       = int64(-1)
	mct       = -1.0
	precision = -1.0
)

func minTimeSample() int64 {
	if mts == -1 {
		mts = calcMinTimeSample()
	}
	return mts
}

func medCallTime() float64 {
	if mct == -1.0 {
		mct = calcMedCallTime()
	}
	return mct
}

// Returns the precision of time measurements obtained via SampleTime() on the runtime system in nanoseconds.
func GetSampleTimePrecision() float64 {
	if precision == -1.0 {
		precision = calcPrecision(minTimeSample(), medCallTime())
	}
	return precision
}

// Returns the median runtime of of one call of SampleTime() on the runtime system in nanoseconds.
func GetSampleTimeRuntime() float64 {
	return medCallTime()
}

func calcPrecision(minTimeSample int64, avgCallTime float64) float64 {
	if float64(minTimeSample) < avgCallTime {
		return avgCallTime
	}
	return float64(minTimeSample)
}

func calcMinTimeSample() int64 {
	var minDiff = int64(math.MaxInt64) // initial large value

	for i := 0; i < iterationsForCallibration; i++ {
		t1 := SampleTime()
		t2 := SampleTime()
		diff := DiffTimeStamps(t1, t2)
		if diff > 0 && diff < minDiff {
			minDiff = diff
		}
	}

	return minDiff
}

func calcMedCallTime() float64 {
	var values [iterationsForCallibration + 1]TimeStamp
	runtime.GC()
	debug.SetGCPercent(-1)
	for i := range iterationsForCallibration + 1 {
		values[i] = SampleTime()
	}
	debug.SetGCPercent(100)
	deltas := make([]float64, 0, iterationsForCallibration)
	var zeros uint64
	for i := range iterationsForCallibration {
		di := DiffTimeStamps(values[i], values[i+1])
		if di == 0 {
			zeros++
		} else {
			deltas = append(deltas, float64(di))
		}
	}
	if zeros > 0 {
		// zeros, so two or more calls returned the same value -> the runtime of a call is smaller than the resolution of the clock -> return average
		diff := float64(DiffTimeStamps(values[0], values[iterationsForCallibration]))
		avg := diff / float64(iterationsForCallibration+1)
		return avg
	}
	// the resolution of the clock is better than the runtime of a call
	median := Median(deltas)
	return median
}
