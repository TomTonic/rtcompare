// Package prng provides the two random number generators rtcompare uses: a
// fast deterministic one, [DPRNG], for reproducible inputs, seeds and
// resampling, and a buffered cryptographic one, [CPRNG], where unpredictability
// matters more than speed or reproducibility.
package prng

import (
	"math/bits"
)

// DPRNG is a deterministic pseudo-random number generator, xorshift64* with
// Vigna's multiplier (see https://en.wikipedia.org/wiki/Xorshift#xorshift*).
//
// The same seed always gives the same sequence, which is what reproducible
// benchmark inputs, build orders and bootstrap replicates need. Its period is
// 2^64-1, every call takes the same time, and it occupies 8 bytes. It is not
// cryptographically secure, and an instance must not be shared between
// goroutines without synchronisation; give each goroutine its own.
//
// Create one with [NewDPRNG]. The zero value is not usable: its state is zero,
// which xorshift never leaves.
type DPRNG struct {
	state uint64
}

// scrambler is Vigna's multiplier for the 12/25/27 xorshift.
const scrambler = uint64(0x2545F4914F6CDD1D)

// NewDPRNG returns a generator seeded with seed.
//
// Parameters: seed is any value, zero included; every seed gives its own fixed
// sequence. The seed is spread over all 64 bits with one round of splitmix64
// before use, so that small or consecutive seeds, such as process indices,
// start unrelated sequences rather than neighbouring states of the same one.
//
// Use it wherever a sequence has to be repeatable. For a sequence that should
// differ from run to run, seed it from a random source, e.g.
// NewDPRNG(rand.Uint64()) with math/rand/v2.
func NewDPRNG(seed uint64) DPRNG {
	z := seed + 0x9E3779B97F4A7C15
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	z ^= z >> 31
	if z == 0 {
		z = 1
	}
	return DPRNG{state: z}
}

// Uint64 returns the next pseudo-random number in the sequence. It takes the
// same time on every call and is small enough to be inlined.
func (thisState *DPRNG) Uint64() uint64 {
	x := thisState.state
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	thisState.state = x
	return x * scrambler
}

// Float64 returns a pseudo-random float64 in the range [0.0, 1.0) like Go’s math/rand.Float64().
// It has a deterministic (i.e. constant) runtime and a high probability to be inlined by the compiler.
// The generated float64 values are uniformly distributed in the range [0.0, 1.0) with the effective precision of 53 bits (IEEE 754 compliant).
func (thisState *DPRNG) Float64() float64 {
	u64 := thisState.Uint64()
	return float64(u64>>11) * (1.0 / (1 << 53)) // use the top 53 bits for a float64 in [0.0, 1.0)
}

// Uint32N returns a pseudo-random uint32 in the range [0, n) like Go’s math/rand.Intn().
// Use this function for generating random indices or sizes for slices or arrays, for example.
// This code avoids modulo arithmetics by implementing Lemire's fast alternative to the modulo reduction
// method (see https://lemire.me/blog/2016/06/27/a-fast-alternative-to-the-modulo-reduction/).
// It has a deterministic (i.e. constant) runtime and a high probability to be inlined by the compiler.
// Note: This implementation may introduce a slight bias if n is not a power of two.
func (thisState *DPRNG) Uint32N(n uint32) uint32 {
	u64 := thisState.Uint64()
	hi, _ := bits.Mul64(u64, uint64(n))
	// we only need the high 64 bits, which is equivalent to (u64 * n) >> 64
	// since n is a uint32 (at most 2^32 - 1), hi is at most 2^32 - 1 and fits in 32 bits
	return uint32(hi)
}

// Shuffle puts n elements in a pseudo-random order by calling swap(i, j) for
// the pairs a Fisher-Yates shuffle exchanges, like math/rand's Shuffle.
//
// Parameters: n is the number of elements, at most 2^32; swap exchanges the
// elements at two indices. Zero or one element leaves nothing to do, and a
// negative n does nothing either.
//
// Its intended use is the build order of benchmark fixtures. Whatever is
// allocated last lands in different memory, and is the last data the caches
// saw, so a fixed order hands one candidate the same advantage in every
// process. Shuffling the order per process, with a seed per process, turns that
// into scatter that pooling across processes can average out; see
// rtcompare.Combine.
//
//	rng := prng.NewDPRNG(seed)
//	builders := []func(){buildA, buildB}
//	rng.Shuffle(len(builders), func(i, j int) { builders[i], builders[j] = builders[j], builders[i] })
//	for _, build := range builders { build() }
//
// The bias of Uint32N for n that are not powers of two carries over, and it is
// far below anything a benchmark could notice.
func (thisState *DPRNG) Shuffle(n int, swap func(i, j int)) {
	for i := n - 1; i > 0; i-- {
		j := int(thisState.Uint32N(uint32(i + 1)))
		swap(i, j)
	}
}
