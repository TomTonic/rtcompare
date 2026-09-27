package rtcompare

import (
	"slices"
	"testing"
)

// TestDPRNGShuffleIsASeededPermutation checks the helper for randomizing the
// build order of benchmark fixtures per process. A shuffle has to keep every
// element, has to repeat exactly for the same seed so that a process can be
// reproduced, and has to leave nothing to do for zero, one or a negative
// number of elements.
func TestDPRNGShuffleIsASeededPermutation(t *testing.T) {
	shuffled := func(seed uint64, n int) []int {
		xs := make([]int, n)
		for i := range xs {
			xs[i] = i
		}
		rng := NewDPRNG(seed)
		rng.Shuffle(len(xs), func(i, j int) { xs[i], xs[j] = xs[j], xs[i] })
		return xs
	}
	a, b := shuffled(7, 50), shuffled(7, 50)
	if !slices.Equal(a, b) {
		t.Error("the same seed produced two different orders")
	}
	if slices.Equal(a, shuffled(8, 50)) {
		t.Error("two seeds produced the same order of 50 elements")
	}
	sorted := slices.Clone(a)
	slices.Sort(sorted)
	for i, v := range sorted {
		if v != i {
			t.Fatalf("the shuffle lost or duplicated elements: %v", a)
		}
	}
	for _, n := range []int{-1, 0, 1} {
		rng := NewDPRNG(1)
		rng.Shuffle(n, func(i, j int) { t.Errorf("swap called for n=%d", n) })
	}
}

// TestDPRNGShuffleIsUniform checks that no build order is favoured. Over many
// shuffles of three elements, each of the six orders has to come up about a
// sixth of the time; the bound is five standard errors wide.
func TestDPRNGShuffleIsUniform(t *testing.T) {
	const trials = 60000
	counts := map[[3]int]int{}
	rng := NewDPRNG(42)
	for range trials {
		xs := [3]int{0, 1, 2}
		rng.Shuffle(3, func(i, j int) { xs[i], xs[j] = xs[j], xs[i] })
		counts[xs]++
	}
	if len(counts) != 6 {
		t.Fatalf("expected all 6 orders, got %d", len(counts))
	}
	want := trials / 6.0
	for order, c := range counts {
		if d := float64(c) - want; d > 5*91.3 || d < -5*91.3 { // sd = sqrt(n p (1-p))
			t.Errorf("order %v came up %d times, want about %.0f", order, c, want)
		}
	}
}

// TestPerturbHeapIsSeededAndBounded checks the heap perturbation that gives
// each process of a multi-process comparison its own memory layout. It has to
// be reproducible from its seed, differ between seeds, and stay within the few
// megabytes of filler plus one large block that its documentation promises.
func TestPerturbHeapIsSeededAndBounded(t *testing.T) {
	a, b, c := PerturbHeap(0), PerturbHeap(0), PerturbHeap(1)
	defer a.KeepAlive()
	defer b.KeepAlive()
	defer c.KeepAlive()
	if a.Bytes() != b.Bytes() {
		t.Errorf("the same seed allocated %d and %d bytes", a.Bytes(), b.Bytes())
	}
	if a.Bytes() == c.Bytes() {
		t.Errorf("two seeds allocated the same %d bytes", a.Bytes())
	}
	limit := 2*perturbClassBudget*len(smallSizes()) + perturbLargeMax
	for _, s := range []*Spacers{a, c} {
		if s.Bytes() <= 0 || s.Bytes() > limit {
			t.Errorf("spacers occupy %d bytes, want between 1 and %d", s.Bytes(), limit)
		}
	}
}

// TestSmallSizesCoverTheSizeClasses checks that the filler reaches every
// small allocation size class: sizes have to start at 8 bytes, end at 32 KB,
// rise strictly, stay multiples of 8 and never step by more than an eighth
// plus 8 bytes, which is at least as fine as Go's own class spacing.
func TestSmallSizesCoverTheSizeClasses(t *testing.T) {
	sizes := smallSizes()
	if sizes[0] != 8 || sizes[len(sizes)-1] != 32<<10 {
		t.Errorf("sizes run from %d to %d, want 8 to 32768", sizes[0], sizes[len(sizes)-1])
	}
	for i := 1; i < len(sizes); i++ {
		step := sizes[i] - sizes[i-1]
		if step <= 0 || sizes[i]%8 != 0 || step > sizes[i-1]/8+8 {
			t.Errorf("step from %d to %d", sizes[i-1], sizes[i])
		}
	}
}
