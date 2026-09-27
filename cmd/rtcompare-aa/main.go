// Command rtcompare-aa compares two identical, separately allocated data
// structures with each other, so that the true difference is zero by
// construction and everything rtcompare reports is an artefact of the
// measurement.
//
// It exists to reproduce, and to re-check fixes for, two effects that only show
// up once the working set outgrows the caches:
//
//   - a systematic head start for whichever candidate ran alone last before the
//     measurement (issue #111), which -mode prefix isolates and -mode compare
//     shows end to end;
//   - scatter between processes far beyond the interval a single process
//     reports (issue #109), for which the command is meant to be run several
//     times.
//
// Pick -n so that one instance is roughly 0.5 to 1 times the size of the
// machine's last-level cache; a node is 64 bytes, so the default of 262,144
// nodes is 16 MB. -n 4096 fits in the caches and serves as the control, where
// nothing should be reported.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/TomTonic/rtcompare"
)

// node is one cache line: a value, a pointer to follow and padding.
type node struct {
	val  uint64
	next *node
	_    [6]uint64
}

// fixture is a random pointer chase: nodes allocated in random order with
// random successors, and a fixed sequence of probes over them.
type fixture struct {
	nodes []*node
	probe []int32
}

// sink keeps the chases observable to the compiler.
var sink uint64

// build allocates both fixtures from the same seed, so that they hold the same
// content, in the order the flag asks for. Whatever is allocated last is also
// what the caches hold when the measurement starts, which is one of the head
// starts this command is about.
func build(n int, order string) (a, b *fixture, err error) {
	fa, fb := newFixture(n), newFixture(n)
	perm, next, probe := layout(n)
	switch order {
	case "ab":
		fa.fill(perm, next, probe)
		fb.fill(perm, next, probe)
	case "ba":
		fb.fill(perm, next, probe)
		fa.fill(perm, next, probe)
	case "mixed":
		fillMixed(fa, fb, perm, next, probe)
	default:
		return nil, nil, fmt.Errorf("unknown build order %q, want ab, ba or mixed", order)
	}
	return fa, fb, nil
}

func newFixture(n int) *fixture {
	return &fixture{nodes: make([]*node, n), probe: make([]int32, 1<<20)}
}

// layout draws the shared content of both fixtures: the order nodes are
// allocated in, each node's successor and the probe sequence.
func layout(n int) (perm, next []int, probe []int32) {
	rng := rtcompare.NewDPRNG(1)
	perm = make([]int, n)
	for i := range perm {
		perm[i] = i
	}
	for i := n - 1; i > 0; i-- {
		j := int(rng.Uint32N(uint32(i + 1)))
		perm[i], perm[j] = perm[j], perm[i]
	}
	next = make([]int, n)
	for i := range next {
		next[i] = int(rng.Uint32N(uint32(n)))
	}
	probe = make([]int32, 1<<20)
	even := uint32(n &^ 1) // probe^1 has to stay in range
	for i := range probe {
		probe[i] = int32(rng.Uint32N(even))
	}
	return perm, next, probe
}

func (f *fixture) fill(perm, next []int, probe []int32) {
	for _, i := range perm {
		f.nodes[i] = &node{val: uint64(i)}
	}
	f.link(next, probe)
}

func (f *fixture) link(next []int, probe []int32) {
	for i, j := range next {
		f.nodes[i].next = f.nodes[j]
	}
	copy(f.probe, probe)
}

// fillMixed allocates the two fixtures node by node in turn, so that neither
// was built last.
func fillMixed(a, b *fixture, perm, next []int, probe []int32) {
	for _, i := range perm {
		a.nodes[i] = &node{val: uint64(i)}
		b.nodes[i] = &node{val: uint64(i)}
	}
	a.link(next, probe)
	b.link(next, probe)
}

// candidate loads a node per operation and follows two pointers from it. Each
// operation depends on the one before, like a lookup whose branches depend on
// the data it loads, so every cache miss is paid in full. Every candidate keeps
// its own cursor into the probe sequence.
func candidate(name string, f *fixture) rtcompare.Candidate {
	j := 0
	return rtcompare.Candidate{Name: name, Batch: func(n uint64) {
		var acc uint64
		for range n {
			x := f.nodes[int(f.probe[j])^int(acc&1)]
			acc += x.val + x.next.val + x.next.next.val
			if j++; j == len(f.probe) {
				j = 0
			}
		}
		sink += acc
	}}
}

type config struct {
	n          int
	mode       string
	order      string
	repeats    int
	validation int
	warmup     int
	warmupDur  time.Duration
	skipVal    bool
}

func main() {
	var c config
	flag.IntVar(&c.n, "n", 1<<18, "nodes per instance (64 bytes each)")
	flag.StringVar(&c.mode, "mode", "compare", "compare: Compare in both role assignments; prefix: Collect after validating none, A, B, or A then B")
	flag.StringVar(&c.order, "build", "ab", "allocation order of the two fixtures: ab, ba or mixed")
	flag.IntVar(&c.repeats, "repeats", 0, "CollectOptions.Repeats (0: default)")
	flag.IntVar(&c.validation, "validation", 0, "CompareOptions.ValidationRuns (0: default)")
	flag.IntVar(&c.warmup, "warmup", 0, "CollectOptions.Warmup (0: default)")
	flag.DurationVar(&c.warmupDur, "warmupdur", 0, "CollectOptions.WarmupDuration (0: default)")
	flag.BoolVar(&c.skipVal, "skipvalidation", false, "CompareOptions.SkipValidation")
	flag.Parse()

	if err := run(c); err != nil {
		fmt.Fprintln(os.Stderr, "rtcompare-aa:", err)
		os.Exit(1)
	}

	// sink is only ever written, so that the compiler cannot drop the chases;
	// in a package main the linter can see that nothing reads it. This read
	// tells it what the writes already told the compiler, as in
	// cmd/rtcompare-example.
	_ = sink
}

func run(c config) error {
	fa, fb, err := build(c.n, c.order)
	if err != nil {
		return err
	}
	switch c.mode {
	case "compare":
		return compare(c, fa, fb)
	case "prefix":
		return prefix(c, fa, fb)
	default:
		return fmt.Errorf("unknown mode %q, want compare or prefix", c.mode)
	}
}

func collectOptions(c config) rtcompare.CollectOptions {
	return rtcompare.CollectOptions{MaxQuantizationError: 1e-4, Repeats: c.repeats, Warmup: c.warmup, WarmupDuration: c.warmupDur}
}

// compare runs Compare with each fixture in each role. Delta is 1 - A/B, so a
// negative delta means A measured slower.
func compare(c config, fa, fb *fixture) error {
	opt := rtcompare.CompareOptions{
		Collect:        collectOptions(c),
		ValidationRuns: c.validation,
		SkipValidation: c.skipVal,
	}
	for _, roles := range []struct {
		name string
		a, b *fixture
	}{{"A=first B=second", fa, fb}, {"A=second B=first", fb, fa}} {
		rep, err := rtcompare.Compare(candidate("A", roles.a), candidate("B", roles.b), opt)
		if err != nil {
			return err
		}
		fmt.Printf("n=%d build=%s %s: A %.2f ns, B %.2f ns, delta %+.2f%% [%+.2f, %+.2f] floor %.2f%% resolved=%v\n",
			c.n, c.order, roles.name, rep.NsPerOpA, rep.NsPerOpB,
			100*rep.Estimate.Delta, 100*rep.Estimate.Low, 100*rep.Estimate.High, 100*rep.NoiseFloor, rep.Resolved)
		fmt.Printf("    drift A %+.1f%% (p %.3f), B %+.1f%% (p %.3f), B/A %+.1f%% (p %.3f)\n",
			100*rep.DriftA.RelativeShift, rep.DriftA.PValue, 100*rep.DriftB.RelativeShift, rep.DriftB.PValue,
			100*rep.DriftRatio.RelativeShift, rep.DriftRatio.PValue)
		for _, w := range rep.Warnings {
			fmt.Println("    warning:", w)
		}
	}
	return nil
}

// prefix runs Collect at a fixed, calibrated batch size after validating no
// candidate, only A, only B, or A and then B, which is what Compare did before
// v0.7.0, and prints the ratio of the medians B/A. 1.00 is the truth. The
// validations are deliberately run one candidate at a time, as a caller who
// does not use ValidatePair would, so that the remaining ratio shows what
// Collect's warm-up washes out on its own.
func prefix(c config, fa, fb *fixture) error {
	co := collectOptions(c)
	cal, err := rtcompare.CalibrateInnerLoops(candidate("A", fa), rtcompare.CalibrationOptions{MaxQuantizationError: co.MaxQuantizationError})
	if err != nil {
		return err
	}
	co.InnerLoops = cal.InnerLoops
	vo := rtcompare.ValidationOptions{Collect: co, Runs: c.validation}
	for _, pre := range []string{"", "A", "B", "AB"} {
		a, b := candidate("A", fa), candidate("B", fb)
		for _, who := range pre {
			switch who {
			case 'A':
				_, err = rtcompare.ValidateHarness(a, vo)
			case 'B':
				_, err = rtcompare.ValidateHarness(b, vo)
			}
			if err != nil {
				return err
			}
		}
		sa, sb, err := rtcompare.Collect(a, b, co)
		if err != nil {
			return err
		}
		label := pre
		if label == "" {
			label = "none"
		}
		fmt.Printf("n=%d build=%s validated %-4s then Collect (InnerLoops %d): median B/A = %.3f\n",
			c.n, c.order, label, co.InnerLoops, rtcompare.Median(sb)/rtcompare.Median(sa))
	}
	return nil
}
