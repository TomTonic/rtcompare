package rtcompare

import (
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSampleTime(t *testing.T) {
	voidvar := int64(17)
	t1 := SampleTime()
	_ = SampleTime()
	t1a := time.Now()
	time.Sleep(3*time.Second + 30*time.Millisecond)
	t2 := SampleTime() // one sleep, one SampleTime() call, and one time.Now() call in between the two SampleTime() calls
	voidvar ^= int64(time.Now().UnixNano())
	t2a := time.Now() // one sleep, one SampleTime() call, and one time.Now() call in between the two time.Now() calls

	diff := DiffTimeStamps(t1, t2)
	diffa := t2a.Sub(t1a)
	aboutEqual := FloatsEqualWithTolerance(float64(diff), float64(diffa), 0.1)                                       // both measurements are in nanoseconds. the values should not differ more than 0.1%
	assert.True(t, aboutEqual, "values diverge to much: %v vs. %v (ignore:%d)", time.Duration(diff), diffa, voidvar) // use voidvar to avoid compiler omtimization to remove voidvar and the according function calls to calculate it
}

func TestCalcMinTimeSample(t *testing.T) {
	// Run calcMinTimeSample and check the result is within expected bounds
	minDiff := calcMinTimeSample()
	t.Logf("calcMinTimeSample result: %d ns", minDiff)
	assert.True(t, minDiff >= 1, "calcMinTimeSample returned too small value")
	assert.True(t, minDiff < 1_000_000, "calcMinTimeSample returned too large value")
	if runtime.GOOS == "windows" {
		assert.True(t, minDiff == 100, "calcMinTimeSample should return 100 on Windows")
		return
	} else {
		if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
			// On some Linux/amd64 systems, the minimum time sample can be as low as 20ns
			assert.True(t, minDiff < 50, "calcMinTimeSample should return less than 50 on Linux/amd64")
			return
		} else if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
			// On some Linux/arm64 systems, the minimum time sample can be as low as 60ns
			assert.True(t, minDiff < 70, "calcMinTimeSample should return less than 70 on Linux/arm64")
			return
		}
		assert.True(t, minDiff < 100, "calcMinTimeSample should return less than 100 on non-Windows")
	}
}
func TestGetSampleTimePrecisionSetsAndCaches(t *testing.T) {
	prev := precision
	defer func() { precision = prev }()

	precision = int64(-1)
	p1 := GetSampleTimePrecision()
	p2 := GetSampleTimePrecision()

	assert.Equal(t, p1, p2, "GetSampleTimePrecision should return a cached value on subsequent calls")
	assert.True(t, p1 >= 15, "precision should be at least 15 ns on all systems")
	if runtime.GOOS == "windows" {
		assert.Equal(t, int64(100), p1, "precision should return 100 ns on Windows systems")
	} else {
		assert.True(t, p1 < 100, "precision should be less than 100 ns on non-Windows systems")
	}
}

func TestGetSampleTimePrecisionRespectsCachedValue(t *testing.T) {
	prev := precision
	defer func() { precision = prev }()

	precision = int64(123456)
	got := GetSampleTimePrecision()
	assert.Equal(t, int64(123456), got, "GetSampleTimePrecision should return the pre-set precision without recalculation")

	// subsequent call returns same cached value
	got2 := GetSampleTimePrecision()
	assert.Equal(t, got, got2)
}
