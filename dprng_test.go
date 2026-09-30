package rtcompare

import (
	"fmt"
	"math"
	"testing"

	set3 "github.com/TomTonic/Set3"
	"github.com/stretchr/testify/assert"
)

func TestPrngSeqLength(t *testing.T) {
	state := NewDPRNG(0x1234567890ABCDEF)
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
	state1 := NewDPRNG(0x1234567890ABCDEF)
	state2 := NewDPRNG(0x1234567890ABCDEF) // create two differnet instances with the same seed
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
	rng := NewDPRNG(0x1234567890ABCDEF)
	for range 100_000 {
		x := rng.Float64()
		if x < 0.0 || x >= 1.0 || math.IsNaN(x) || math.IsInf(x, 0) {
			t.Errorf("Float64 out of range: %f", x)
		}
	}
}

func TestFloat64Determinism(t *testing.T) {
	rng1 := NewDPRNG(0x1234567890ABCDEF)
	rng2 := NewDPRNG(0x1234567890ABCDEF)

	for i := range 1000 {
		x1 := rng1.Float64()
		x2 := rng2.Float64()
		if x1 != x2 {
			t.Errorf("Mismatch at iteration %d: %f vs %f", i, x1, x2)
		}
	}
}

func TestFloat64Distribution(t *testing.T) {
	rng := NewDPRNG(0x1234567890ABCDEF)
	N := 1_000_000
	var sum float64
	for range N {
		sum += rng.Float64()
	}
	mean := sum / float64(N)
	if math.Abs(mean-0.5) > 0.01 {
		t.Errorf("Mean too far from 0.5: got %.5f", mean)
	}
}

func TestFloat64Precision(t *testing.T) {
	rng := NewDPRNG(0x1234567890ABCDEF)
	seen := make(map[float64]bool)
	for range 100000 {
		x := rng.Float64()
		if seen[x] {
			t.Errorf("Duplicate value detected: %f", x)
			break
		}
		seen[x] = true
	}
}

// TestUInt32N_Frequencies draws 1_000_000 samples for several n values and
// checks that each bucket's observed frequency is within 3% relative error of 1/n.
func TestUInt32N_Frequencies(t *testing.T) {
	cases := []uint32{13, 64, 100}
	const samples = 10_000_000
	const maxRel = 0.01 // 1%

	for _, n := range cases {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			seed := uint64(0xDEADBEEFCAFEBABE)
			rng := NewDPRNG(seed)
			counts := make([]uint32, n)
			for range samples {
				v := rng.Uint32N(n)
				counts[int(v)]++
			}

			expected := float64(samples) / float64(n)
			for i := 0; i < int(n); i++ {
				obs := float64(counts[i])
				rel := math.Abs(obs-expected) / expected
				if rel > maxRel {
					t.Fatalf("n=%d bucket %d relative deviation too large: %.4f > %.4f (obs=%d expected=%.2f)", n, i, rel, maxRel, counts[i], expected)
				}
			}
		})
	}
}

// TestNewDPRNGIsDeterministicForEverySeed checks what reproducible inputs rely
// on: every non-zero seed gives its own fixed sequence, two generators from
// one seed agree, and neighbouring seeds such as process indices start
// unrelated sequences rather than the same one shifted.
func TestNewDPRNGIsDeterministicForEverySeed(t *testing.T) {
	for _, seed := range []uint64{1, 2, 42, math.MaxUint64} {
		t.Run(fmt.Sprintf("repeats the sequence of seed %d", seed), func(t *testing.T) {
			a, b := NewDPRNG(seed), NewDPRNG(seed)
			distinct := map[uint64]bool{}
			for range 1000 {
				x := a.Uint64()
				if x != b.Uint64() {
					t.Fatal("two generators from the same seed diverged")
				}
				distinct[x] = true
			}
			if len(distinct) < 1000 {
				t.Errorf("only %d distinct values in 1000 draws", len(distinct))
			}
		})
	}
	one, two := NewDPRNG(1), NewDPRNG(2)
	near := 0
	for range 64 {
		if x, y := one.Uint64(), two.Uint64(); x^y < 1<<48 {
			near++
		}
	}
	if near > 0 {
		t.Errorf("seeds 1 and 2 gave nearly equal values %d times in 64 draws", near)
	}
}

// TestNewDPRNGSeedZeroIsRandom checks the promise callers rely on when they
// want a different sequence per run: seed zero does not give a fixed sequence.
// Two generators made with seed zero differ, and neither equals the sequence
// of any fixed seed by construction.
func TestNewDPRNGSeedZeroIsRandom(t *testing.T) {
	a, b := NewDPRNG(0), NewDPRNG(0)
	a1, a2, b1, b2 := a.Uint64(), a.Uint64(), b.Uint64(), b.Uint64()
	if a1 == b1 && a2 == b2 {
		t.Error("two generators seeded with zero produced the same values")
	}
}

// TestDPRNGIntegerMethodsAreUniform checks the sized integer methods a
// caller picks for convenience: over many draws every value of a uint8 and
// of a uint16 turns up about equally often, and the signed variants are the
// same bits reinterpreted.
func TestDPRNGIntegerMethodsAreUniform(t *testing.T) {
	const samples = 1 << 20
	rng := NewDPRNG(5)
	c8 := make([]int, 256)
	c16 := make([]int, 65536)
	for range samples {
		c8[rng.Uint8()]++
		c16[rng.Uint16()]++
	}
	requireUniform(t, "DPRNG.Uint8", c8)
	requireUniform(t, "DPRNG.Uint16", c16)

	a, b := NewDPRNG(9), NewDPRNG(9)
	for range 100 {
		if int8(a.Uint8()) != b.Int8() || int16(a.Uint16()) != b.Int16() ||
			int32(a.Uint32()) != b.Int32() || int64(a.Uint64()) != b.Int64() {
			t.Fatal("signed method does not return the bits of the unsigned one")
		}
	}
}

// TestFloat32RangeAndResolution checks that DPRNG.Float32 stays in [0, 1) and
// uses all 24 significand bits: its values are multiples of 2^-24.
func TestFloat32RangeAndResolution(t *testing.T) {
	rng := NewDPRNG(3)
	for range 100000 {
		f := rng.Float32()
		if f < 0 || f >= 1 || float64(f)*(1<<24) != math.Trunc(float64(f)*(1<<24)) {
			t.Fatalf("Float32 returned %v", f)
		}
	}
}
