package rtcompare

import (
	"math/bits"
	"math/rand"
)

// DPRNG is a Deterministic Pseudo-Random Number Generator based on the xorshift* algorithm
// (see https://en.wikipedia.org/wiki/Xorshift#xorshift*).
// This random number generator is by design deterministic in the sequence of numbers it generates. It has a period of 2^64-1,
// i.e. every single number occurs every 2^64-1 calls and has the same successor and the same predecessor.
// This random number generator is deterministic in its runtime (i.e., it has a constant runtime).
// This random number generator is not cryptographically secure.
// This random number generator is thread-safe as long as each goroutine uses its own instance.
// This random number generator has a very small memory footprint (24 bytes).
// The initial state must not be zero.
type DPRNG struct {
	State     uint64
	Scrambler uint64
	Round     uint64 // for debugging purposes
}

const vigna = uint64(0x2545F4914F6CDD1D) // Vigna's default scrambler constant optimized for our 12/25/27 xorshift

// NewDPRNG creates a new Deterministic Pseudo-Random Number Generator instance.
// If no seed is provided, it initializes the state with a random non-zero value.
// If the provided seed is zero, it initializes the state with a random non-zero value.
// Otherwise, it uses the provided seed value.
// If a second uint64 value is provided, it is used as the scrambler constant. The scrambler
// constant creates a permutation (different sequence) of the generated numbers. If no scrambler
// constant is provided, Vigna's default scrambler constant is used.
// The only requirement for the scrambler constant is that it must be an odd number to ensure
// maximal period, but this is automatically enforced by the code. You can use [GenerateScrambler]
// to generate good quality scrambler constants.
func NewDPRNG(seed ...uint64) DPRNG {
	result := DPRNG{}
	if len(seed) == 0 {
		result.State = uint64(rand.Uint64()&0xFFFFFFFFFFFFFFFE + 1) // initialize with a random number != 0
		result.Scrambler = vigna
	} else {
		result.State = seed[0]
		if result.State == 0 {
			result.State = uint64(rand.Uint64()&0xFFFFFFFFFFFFFFFE + 1) // initialize with a random number != 0
		}
		if len(seed) > 1 {
			result.Scrambler = seed[1] | 1 // ensure scrambler is odd
		} else {
			result.Scrambler = vigna
		}
	}
	return result
}

// GenerateScrambler generates reasonable scrambler constants for the DPRNG.
// The generated scrambler constant is always an odd number with a good bit density.
// This ensures maximal period and good mixing properties.
// You can use the returned scrambler constant when creating a new DPRNG
// instance to get a different permutation (i.e., sequence) of generated numbers.
func GenerateScrambler() uint64 {
	cprng := NewCPRNG(256)
	for {
		candidate := cprng.Uint64() | 1 // ensure scrambler is odd
		if bits.OnesCount64(candidate) < 28 {
			continue
		}
		if bits.OnesCount64(candidate) > 36 {
			continue
		}
		upperHalf := candidate >> 32
		if bits.OnesCount64(upperHalf) < 13 {
			continue
		}
		if bits.OnesCount64(upperHalf) > 19 {
			continue
		}
		return candidate
	}
}

// This function returns the next pseudo-random number in the sequence.
// It has a deterministic (i.e. constant) runtime and a high probability to be inlined by the compiler.
func (thisState *DPRNG) Uint64() uint64 {
	x := thisState.State
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	thisState.State = x
	thisState.Round++
	return x * thisState.Scrambler
}

// Float64 returns a pseudo-random float64 in the range [0.0, 1.0) like Go’s math/rand.Float64().
// It has a deterministic (i.e. constant) runtime and a high probability to be inlined by the compiler.
// The generated float64 values are uniformly distributed in the range [0.0, 1.0) with the effective precision of 53 bits (IEEE 754 compliant).
func (thisState *DPRNG) Float64() float64 {
	u64 := thisState.Uint64()
	return float64(u64>>11) * (1.0 / (1 << 53)) // use the top 53 bits for a float64 in [0.0, 1.0)
}

// UInt32N returns a pseudo-random uint32 in the range [0, n) like Go’s math/rand.Intn().
// Use this function for generating random indices or sizes for slices or arrays, for example.
// This code avoids modulo arithmetics by implementing Lemire's fast alternative to the modulo reduction
// method (see https://lemire.me/blog/2016/06/27/a-fast-alternative-to-the-modulo-reduction/).
// It has a deterministic (i.e. constant) runtime and a high probability to be inlined by the compiler.
// Note: This implementation may introduce a slight bias if n is not a power of two.
func (thisState *DPRNG) UInt32N(n uint32) uint32 {
	u64 := thisState.Uint64()
	hi, _ := bits.Mul64(u64, uint64(n))
	// we only need the high 64 bits, which is equivalent to (u64 * n) >> 64
	// as n <= 2^32, hi is guaranteed to fit into 32 bits
	return uint32(hi)
}
