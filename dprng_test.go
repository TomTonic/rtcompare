package rtcompare

import (
	"math"
	"testing"

	set3 "github.com/TomTonic/Set3"
	"github.com/stretchr/testify/assert"
)

func TestPrngSeqLength(t *testing.T) {
	state := DPRNG{State: 0x1234567890ABCDEF}
	limit := uint32(30_000_000)
	set := set3.EmptyWithCapacity[uint64](limit * 7 / 5)
	counter := uint32(0)
	for set.Size() < limit {
		set.Add(state.Uint64())
		counter++
	}
	assert.True(t, counter == limit, "sequence < limit")
}

func TestPrngDeterminism(t *testing.T) {
	state1 := DPRNG{State: 0x1234567890ABCDEF}
	state2 := DPRNG{State: 0x1234567890ABCDEF} // create two differnet instances with the same seed
	limit := 30_000_000
	for i := range limit {
		v1 := state1.Uint64()
		v2 := state2.Uint64()
		assert.True(t, v1 == v2, "out of sync: values not equal in round %d", i)
	}
	_ = state2.Uint64() // skip one value to get both prng out of sync
	for i := range limit {
		v1 := state1.Uint64()
		v2 := state2.Uint64()
		assert.False(t, v1 == v2, "in: values equal in round %d", i)
	}
	_ = state1.Uint64() // get both prng back in sync
	for i := range limit {
		v1 := state1.Uint64()
		v2 := state2.Uint64()
		assert.True(t, v1 == v2, "out of sync: values not equal in round %d", i)
	}
}

func TestFloat64Range(t *testing.T) {
	rng := DPRNG{State: 12345}
	for i := 0; i < 10000; i++ {
		x := rng.Float64()
		if x < 0.0 || x >= 1.0 || math.IsNaN(x) || math.IsInf(x, 0) {
			t.Errorf("Float64 out of range: %f", x)
		}
	}
}

func TestFloat64Determinism(t *testing.T) {
	rng1 := DPRNG{State: 42}
	rng2 := DPRNG{State: 42}

	for i := 0; i < 1000; i++ {
		x1 := rng1.Float64()
		x2 := rng2.Float64()
		if x1 != x2 {
			t.Errorf("Mismatch at iteration %d: %f vs %f", i, x1, x2)
		}
	}
}

func TestFloat64Distribution(t *testing.T) {
	rng := DPRNG{State: 999}
	N := 1_000_000
	var sum float64

	for i := 0; i < N; i++ {
		sum += rng.Float64()
	}
	mean := sum / float64(N)
	if math.Abs(mean-0.5) > 0.01 {
		t.Errorf("Mean too far from 0.5: got %.5f", mean)
	}
}

func TestFloat64Precision(t *testing.T) {
	rng := DPRNG{State: 777}
	seen := make(map[float64]bool)
	for i := 0; i < 100000; i++ {
		x := rng.Float64()
		if seen[x] {
			t.Errorf("Duplicate value detected: %f", x)
			break
		}
		seen[x] = true
	}
}
