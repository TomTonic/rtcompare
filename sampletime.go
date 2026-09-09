package rtcompare

import (
	"math"
	"sync"
)

// maxIterationsForCalibration caps how many probe pairs calcMinTimeSample will
// take. It is a ceiling for pathological cases, not the expected cost; the
// search normally stops long before it via stableRunForCalibration.
const maxIterationsForCalibration = 10_000_000

// stableRunForCalibration is how many consecutive probes must fail to improve
// the minimum before the search accepts it.
//
// The quantity being searched for has a hard floor, whether that floor is the
// clock's tick or the cost of the two calls around it, so it is reached almost
// immediately and no amount of further probing can go below it. Measured on
// macOS/arm64, the minimum was already final after 1,000 probes and unchanged
// through 10,000,000, while the cost grew from 92 microseconds to 764
// milliseconds.
//
// A fixed smaller constant would be a guess about platforms this was not
// measured on. Stopping after a stable run adapts instead: it spends whatever
// the platform needs and no more. Windows in particular reaches SampleTime
// through a LazyProc call rather than a vDSO, which is a different and more
// expensive path, so a hand-tuned iteration count would be poorly informed.
const stableRunForCalibration = 50_000

var (
	// precision holds the smallest interval measurable via SampleTime() on the
	// runtime system, in nanoseconds. See GetSampleTimePrecision.
	precision     int64 = -1
	precisionOnce sync.Once
)

// GetSampleTimePrecision returns the smallest interval that can actually be
// measured with SampleTime() on this machine, in nanoseconds. It is determined
// empirically on first use and cached for the lifetime of the process.
//
// This is the practical floor of the measurement: no single timing can be
// trusted below it, and it is the quantity that determines how long a batch has
// to run for a given quantization error, which is what CalibrateInnerLoops uses
// it for.
//
// It is deliberately not "the clock's resolution", and the two can differ.
// What the function observes is the smallest gap it can produce between two
// consecutive SampleTime calls, which is bounded from below by whichever is
// larger: the clock's tick, or the cost of the two calls themselves. Which one
// dominates depends on the platform.
//
//   - On macOS/arm64 the timebase runs at 24 MHz, one tick every 41.667 ns, and
//     the observed gaps come out as its multiples, rounded to whole nanoseconds:
//     41, 42, 83, 84, 125, 166, 167, 208. Here the tick dominates and the
//     returned value is the resolution.
//   - On Linux/amd64, where clock_gettime resolves to a nanosecond through the
//     vDSO, the call is expected to be the larger of the two, which would make
//     the returned value closer to that overhead than to any tick. Measured on
//     a GitHub Actions runner under coverage instrumentation, that overhead
//     came to 60 ns, well above the 50 ns an earlier version of this comment
//     assumed without measuring; a shared, virtualized machine plausibly makes
//     the vDSO call itself slower than a quiet dedicated one does. The tests no
//     longer assert a tighter bound than that, because the number this function
//     returns is a property of the machine it runs on, not a constant the test
//     suite can know in advance.
//   - On Windows the timestamp comes from QueryPerformanceCounter, whose
//     frequency comes from the platform's hardware abstraction layer rather
//     than from measuring call overhead, and is effectively always 10 MHz on
//     modern Windows, giving a 100 ns tick. Being a hardware fact rather than a
//     benchmark result, this one is asserted exactly; it is not exercised by
//     this project's own CI, which runs on Linux only.
//
// In every one of those cases the returned number answers the same question and
// is the one worth having: this is as fine as measurement gets here. Note that
// this is unrelated to the coarse Windows system clock of about 15.6 ms that
// GetSystemTimeAsFileTime is subject to; SampleTime does not use it.
func GetSampleTimePrecision() int64 {
	precisionOnce.Do(func() {
		precision = calcMinTimeSample()
	})
	return precision
}

// calcMinTimeSample probes the clock repeatedly and returns the smallest
// positive interval it managed to observe between two consecutive SampleTime
// calls, in nanoseconds.
//
// It stops once the minimum has survived stableRunForCalibration probes without
// improving, and gives up at maxIterationsForCalibration in any case. Zero and
// negative differences are ignored: the former mean the two calls fell inside
// one tick of the clock, which says nothing about its resolution.
func calcMinTimeSample() int64 {
	var minDiff = int64(math.MaxInt64) // initial large value
	sinceImprovement := 0
	for range maxIterationsForCalibration {
		t1 := SampleTime()
		t2 := SampleTime()
		diff := DiffTimeStamps(t1, t2)
		if diff > 0 && diff < minDiff {
			minDiff = diff
			sinceImprovement = 0
			continue
		}
		sinceImprovement++
		if minDiff != int64(math.MaxInt64) && sinceImprovement >= stableRunForCalibration {
			break
		}
	}
	return minDiff
}
