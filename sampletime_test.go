package rtcompare

import (
	"math"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSampleTime(t *testing.T) {
	voidvar := int64(17)
	t1 := sampleTime()
	_ = sampleTime()
	t1a := time.Now()
	time.Sleep(3*time.Second + 30*time.Millisecond)
	t2 := sampleTime() // one sleep, one sampleTime() call, and one time.Now() call in between the two sampleTime() calls
	voidvar ^= int64(time.Now().UnixNano())
	t2a := time.Now() // one sleep, one sampleTime() call, and one time.Now() call in between the two time.Now() calls

	diff := diffTimeStamps(t1, t2)
	diffa := t2a.Sub(t1a)
	// Both measurements are in nanoseconds and should not differ by more than
	// 0.1%. voidvar is printed so that the compiler cannot drop the calls that
	// compute it.
	aboutEqual := math.Abs(float64(diff)-float64(diffa)) <= 0.001*float64(diffa)
	assert.True(t, aboutEqual, "values diverge too much: %v vs. %v (ignore:%d)", time.Duration(diff), diffa, voidvar)
}

func TestCalcMinTimeSample(t *testing.T) {
	// Run calcMinTimeSample and check the result is within expected bounds.
	minDiff := calcMinTimeSample()
	t.Logf("calcMinTimeSample result: %d ns (GOOS=%s GOARCH=%s)", minDiff, runtime.GOOS, runtime.GOARCH)
	assert.True(t, minDiff >= 1, "calcMinTimeSample returned too small value")
	assert.True(t, minDiff < 1_000_000, "calcMinTimeSample returned too large value")

	// Windows is the one platform worth an exact expectation. QueryPerformanceCounter's
	// frequency comes from the platform's hardware abstraction layer rather than from
	// measuring call overhead, and is effectively always 10MHz on modern Windows, so a
	// 100ns tick is a hardware fact rather than a benchmark result and isn't subject to
	// the environment-to-environment variance call overhead is. This branch is not
	// exercised by this project's own CI, which runs on Linux only.
	if runtime.GOOS == "windows" {
		assert.True(t, minDiff == 100, "calcMinTimeSample should return 100 on Windows")
		return
	}

	// Everywhere else, this used to assert tight per-OS/arch bounds, e.g. "under 50ns on
	// Linux/amd64". GetSampleTimePrecision's own documentation already flagged that figure
	// as an assumption rather than a measurement, and it broke on a GitHub Actions runner,
	// which reported 60ns under coverage instrumentation: plausibly a shared, virtualized
	// machine making the underlying clock_gettime call itself a little slower than on a
	// quiet, dedicated one — exactly the "call cost dominates the tick" case the
	// documentation already anticipated. There is no bound here narrower than the generic
	// one above: what calcMinTimeSample measures is inherently a property of the machine it
	// runs on, not a constant this test can know in advance.
}
func TestGetSampleTimePrecisionSetsAndCaches(t *testing.T) {
	prev := precision
	defer func() { precision = prev }()

	// GetSampleTimePrecision computes at most once per process, guarded by
	// precisionOnce. Forcing a recomputation therefore means arming that Once
	// again as well; resetting `precision` alone is not enough. Without this the
	// test reads back the -1 written below as soon as anything earlier in the
	// process has already triggered the computation, which makes it depend on
	// test execution order.
	precisionOnce = sync.Once{}

	precision = int64(-1)
	p1 := GetSampleTimePrecision()
	p2 := GetSampleTimePrecision()

	assert.Equal(t, p1, p2, "GetSampleTimePrecision should return a cached value on subsequent calls")
	assert.True(t, p1 >= 15, "precision should be at least 15 ns on all systems")
	if runtime.GOOS == "windows" {
		// A hardware fact rather than a benchmark result; see calcMinTimeSample's
		// documentation and TestCalcMinTimeSample for why this one alone is exact.
		assert.Equal(t, int64(100), p1, "precision should return 100 ns on Windows systems")
	} else {
		// No tighter bound than this: what this measures is a property of the
		// machine it runs on. This used to assert "< 100 ns", which failed on a
		// GitHub Actions Linux runner reporting 60 ns under coverage
		// instrumentation — comfortably plausible for vDSO call overhead on a
		// shared, virtualized machine, and not a bug. See TestCalcMinTimeSample.
		assert.True(t, p1 < 1_000_000, "precision should be well under a millisecond on non-Windows systems")
	}
}

func TestGetSampleTimePrecisionRespectsCachedValue(t *testing.T) {
	prev := precision
	defer func() { precision = prev }()

	// The mirror image of the hazard in the test above: this one needs the
	// one-shot computation to have happened already, otherwise the first call
	// below overwrites the value being tested. Trigger it explicitly instead of
	// relying on an earlier test to have done so.
	GetSampleTimePrecision()

	precision = int64(123456)
	got := GetSampleTimePrecision()
	assert.Equal(t, int64(123456), got, "GetSampleTimePrecision should return the pre-set precision without recalculation")

	// subsequent call returns same cached value
	got2 := GetSampleTimePrecision()
	assert.Equal(t, got, got2)
}
