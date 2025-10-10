package rtcompare

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMinTimeSample(t *testing.T) {
	minTimeSample := minTimeSample()
	t.Logf("MinTimeSample of test system: %dns", minTimeSample)
	assert.True(t, minTimeSample >= 1, "MinSampleTime too small")
	assert.True(t, minTimeSample < 1_000_000, "MinSampleTime too big")
}

func TestAvgCallTime(t *testing.T) {
	medCallTime := medCallTime()
	t.Logf("AvgCallTime of test system: %fns", medCallTime)
	assert.True(t, medCallTime >= 1, "AvgCallTime too small")
	assert.True(t, medCallTime < 1_000_000, "AvgCallTime too big")
}

func TestGetPrecision(t *testing.T) {
	p := GetSampleTimePrecision()
	assert.True(t, p >= 1, "Precision too small")
	assert.True(t, p < 1_000_000, "Precision too big")
	assert.True(t, p == float64(minTimeSample()) || p == medCallTime(), "Unexpected value: %f, %f, %f", p, float64(minTimeSample()), medCallTime())
}

func TestSampleTime(t *testing.T) {
	voidvar := int64(17)
	t1 := SampleTime()
	_ = SampleTime()
	t1a := time.Now()
	time.Sleep(3*time.Second + 30*time.Millisecond)
	t2 := SampleTime()
	voidvar ^= int64(time.Now().UnixNano())
	t2a := time.Now()

	diff := DiffTimeStamps(t1, t2)
	diffa := t2a.Sub(t1a)
	aboutEqual := FloatsEqualWithTolerance(float64(diff), float64(diffa), 0.1)
	assert.True(t, aboutEqual, "values diverge to much: %v vs. %v (ignore:%d)", time.Duration(diff), diffa, voidvar)
}
