package rtcompare

// DPRNG is a Deterministic Pseudo-Random Number Generator based on the xorshift* algorithm
// (see https://en.wikipedia.org/wiki/Xorshift#xorshift*).
// This randum number generator is deterministic in the sequence of numbers it generates. It has a period of 2^64-1,
// i.e. every single number occurs every 2^64-1 calls and has the same successor and the same predecessor.
// This randum number generator is deterministic its runtime (i.e. it has a constant runtime).
// This randum number generator is not cryptographically secure.
// This randum number generator is not thread-safe.
// This random number generator has a very small memory footprint (16 bytes).
// The initial state must not be zero.
type DPRNG struct {
	State uint64
	Round uint64 // for debugging purposes
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
	return x * 0x2545F4914F6CDD1D
}
