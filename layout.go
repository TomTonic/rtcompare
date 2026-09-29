package rtcompare

// This file holds PerturbHeap, which gives each process a different heap layout
// so that the layout becomes random scatter between processes rather than a
// bias that every process repeats.

import (
	"runtime"
	"sync/atomic"

	"github.com/TomTonic/rtcompare/prng"
)

// Spacers holds the allocations [PerturbHeap] made. Its only purpose is to stay
// reachable: while it is, the memory it occupies is not handed to anything
// else.
type Spacers struct {
	noscan [][]byte
	scan   [][]*byte
	large  []byte
}

// Bytes returns how much memory the spacers occupy, for reporting.
func (s *Spacers) Bytes() int {
	total := len(s.large)
	for _, b := range s.noscan {
		total += len(b)
	}
	for _, p := range s.scan {
		total += 8 * len(p)
	}
	return total
}

// KeepAlive keeps the spacers reachable up to the point of the call. Call it
// after the last measurement, e.g. with defer right after PerturbHeap.
func (s *Spacers) KeepAlive() { runtime.KeepAlive(s) }

// perturbClassBudget bounds the spacers per allocation size, and so the total,
// which comes to a few megabytes across all sizes.
const perturbClassBudget = 64 << 10

// perturbLargeMax bounds the one large spacer block. Large objects get their
// own pages, so this block moves where the next large allocations begin.
const perturbLargeMax = 16 << 20

// PerturbHeap allocates a pseudo-random amount of filler in every small
// allocation size and one large block, so that data allocated afterwards lands
// at addresses and in cache sets that differ from one seed to the next.
//
// Parameters: seed selects the perturbation; zero is a valid seed like any
// other. Give each process a different one, and record it, so that a process
// can be repeated exactly.
//
// It returns the spacers, which must stay reachable until the measurement is
// over: `defer rtcompare.PerturbHeap(seed).KeepAlive()` does that. If they
// were collected, their memory could be handed to the code under test and the
// perturbation would partly undo itself.
//
// The multiproc package calls it in every child process before the suite
// runs, so that code using multiproc never needs to. Call it yourself only
// when running processes by other means, at the start of a process, before
// building the data the candidates work on. The problem it addresses is that a Go program's heap layout is
// deterministic: the same allocations in the same order give much the same
// addresses in every run. For data larger than the caches or full of pointers,
// that layout moves a measured difference by several points, and a program
// that repeats its layout repeats its bias in every process, which pooling
// across processes cannot remove (issue #109). Perturbing the heap per process
// turns the layout into a random effect, which [Combine] can average over.
// It does not replace varying the order in which the fixtures are built: in
// the reproduction in cmd/rtcompare-aa, whichever of two identical lists was
// built second stayed about 3% faster whatever the perturbation, so the build
// order has to alternate between processes as well; see the multiproc package. The idea follows Curtsinger and Berger, "STABILIZER:
// Statistically Sound Performance Evaluation", ASPLOS 2013, which randomizes
// code, stack and heap layout for the same reason.
//
// It only moves the heap. Code layout, stack addresses and the size of the
// environment, which also shift performance (Mytkowicz et al., "Producing Wrong
// Data Without Doing Anything Obviously Wrong!", ASPLOS 2009), are untouched.
//
// The filler costs a few megabytes of spacers across the size classes, both
// with and without pointers, plus a large block of up to 16 MB whose pages are
// mostly never touched, and about a millisecond.
func PerturbHeap(seed uint64) *Spacers {
	rng := prng.NewDPRNG(seed)
	sizes := smallSizes()
	rng.Shuffle(len(sizes), func(i, j int) { sizes[i], sizes[j] = sizes[j], sizes[i] })

	s := &Spacers{}
	for _, size := range sizes {
		limit := uint32(perturbClassBudget / size)
		// Objects with and without pointers live in different spans, so both
		// kinds are shifted.
		for range rng.Uint32N(limit + 1) {
			s.noscan = append(s.noscan, make([]byte, size))
		}
		for range rng.Uint32N(limit + 1) {
			s.scan = append(s.scan, make([]*byte, size/8))
		}
	}
	s.large = make([]byte, rng.Uint32N(perturbLargeMax))

	// The filler is not the program's data, and Compare's warning about a
	// large live heap must not count it: every multiproc child perturbs its
	// heap, and the filler alone would otherwise trip the warning there for
	// data of any size. The count is returned once the filler is collected.
	n := int64(s.Bytes())
	spacerBytes.Add(n)
	runtime.AddCleanup(s, func(n int64) { spacerBytes.Add(-n) }, n)
	return s
}

// spacerBytes is how much memory the spacers of PerturbHeap still alive
// occupy, for liveHeap to leave out.
var spacerBytes atomic.Int64

// smallSizes returns allocation sizes from 8 bytes to 32 KB spaced about an
// eighth apart, which is roughly how Go spaces its size classes, so that every
// class receives some filler without depending on the runtime's exact table.
func smallSizes() []int {
	const largest = 32 << 10
	var sizes []int
	for size := 8; size < largest; size += max(8, size/8&^7) {
		sizes = append(sizes, size)
	}
	return append(sizes, largest)
}

// mixSeed spreads a seed over all 64 bits with one round of splitmix64, for
// deriving one seed from another, and never returns zero, which the bootstrap
// takes to mean cryptographic randomness.
func mixSeed(seed uint64) uint64 {
	z := seed + 0x9E3779B97F4A7C15
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	z ^= z >> 31
	if z == 0 {
		return 1
	}
	return z
}
