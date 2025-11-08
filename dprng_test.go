package rtcompare

import (
	"fmt"
	"math"
	"testing"

	set3 "github.com/TomTonic/Set3"
	"github.com/stretchr/testify/assert"
)

func TestNewDPRNG_NoSeed_GeneratesNonZero(t *testing.T) {
	prng := NewDPRNG()
	if prng.State == 0 {
		t.Errorf("Expected non-zero state when no seed is provided, got 0")
	}
}

func TestNewDPRNG_ZeroSeed_GeneratesNonZero(t *testing.T) {
	prng := NewDPRNG(0)
	if prng.State == 0 {
		t.Errorf("Expected non-zero state when seed is 0, got 0")
	}
}

func TestNewDPRNG_WithValidSeed(t *testing.T) {
	seed := uint64(42)
	prng := NewDPRNG(seed)
	if prng.State != seed {
		t.Errorf("Expected state %d, got %d", seed, prng.State)
	}
}

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
				v := rng.UInt32N(n)
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

// TestUInt32N_CompareToModulo compares UInt32N against the reference
// distribution computed by taking the low 32 bits of the raw Uint64 stream
// and reducing by modulo. Both sequences are started with the same seed and
// consume one RNG value per sample to stay aligned.
func TestUInt32N_CompareToModulo(t *testing.T) {
	cases := []struct {
		name string
		n    uint32
	}{
		{"p3", 3},
		{"p5", 5},
		{"p7", 7},
		{"p11", 11},
		{"2^8", 256},
		{"2^8-1", 255},
		{"2^8+1", 257},
		{"prime~256", 251},
		{"2^10", 1024},
		{"2^10-1", 1023},
		{"2^10+1", 1025},
		{"prime~1024", 1031},
	}

	const samples = 50_000
	const maxRelThreshold = 0.05 // 2%
	const iterations = 512

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.n == 0 {
				t.Fatalf("invalid n: 0")
			}
			k := int(c.n)
			resultsObs := make([]float64, 0, iterations)
			resultsRef := make([]float64, 0, iterations)
			seed := uint64(0x1234567890ABCDEF)
			rngObs := NewDPRNG(seed)
			rngRef := NewDPRNG(seed)

			for range iterations {
				countsObs := make([]uint32, k)
				countsRef := make([]uint32, k)

				for range samples {
					v := rngObs.UInt32N(c.n)
					u := rngRef.Uint64()
					ref := uint32(u&0xFFFFFFFF) % c.n

					countsObs[int(v)]++
					countsRef[int(ref)]++
				}

				dObs := float64(dMaxUint32(countsObs))
				dRef := float64(dMaxUint32(countsRef))

				resultsObs = append(resultsObs, dObs)
				resultsRef = append(resultsRef, dRef)
			}
			confidenceForThresholdObsBetter := BootstrapConfidence(resultsObs, resultsRef, []float64{maxRelThreshold}, 1_000, uint64(5))
			confidenceForThresholdRefBetter := BootstrapConfidence(resultsRef, resultsObs, []float64{maxRelThreshold}, 1_000, uint64(5))

			if confidenceForThresholdObsBetter[maxRelThreshold] != confidenceForThresholdRefBetter[maxRelThreshold] {
				t.Errorf("confidenceForThresholdObsBetter and confidenceForThresholdRefBetter differ: confidence %.4f vs %.4f for rel.threshold %.2f",
					confidenceForThresholdObsBetter[maxRelThreshold],
					confidenceForThresholdRefBetter[maxRelThreshold],
					maxRelThreshold)
			}
		})
	}
}

func minMax[T ~int | ~int8 | ~int16 | ~int32 | ~int64 |
	~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
	~float32 | ~float64](vals ...T) (min, max T) {
	var zero T
	if len(vals) == 0 {
		return zero, zero
	}
	min, max = vals[0], vals[0]
	for _, v := range vals[1:] {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	return min, max
}

func dMaxUint32(s []uint32) uint32 {
	min, max := minMax(s...)
	return max - min
}
