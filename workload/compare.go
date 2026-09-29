package workload

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/TomTonic/rtcompare"
)

// Structure describes one data structure under test for [Compare]: how to
// create an empty one, and how to apply operations to it.
type Structure[S any] struct {
	// Name labels the structure in the results.
	Name string

	// New returns an empty structure, created the way the program would create
	// it. Whether it is given a capacity hint is part of what the build
	// comparison measures, so give it one exactly when the program would.
	New func() S

	// Apply performs the operations of run on s, in order. Keep the loop over
	// run in this function, where the compiler can inline the structure's
	// methods. With Config.Lookups set, run also holds Lookup and LookupMiss
	// operations, so switch over all four kinds rather than treating
	// everything but an insertion as a deletion.
	Apply func(s S, run []Op)
}

// check rejects a structure that cannot be used.
func (s Structure[S]) check(position string) error {
	if s.New == nil || s.Apply == nil {
		return fmt.Errorf("workload: structure %s (%q) needs both New and Apply", position, s.Name)
	}
	return nil
}

// Options configures [Compare]. The zero value is usable and selects the
// documented defaults.
type Options struct {
	// Config configures the two streams, see [Config].
	Config Config

	// SteadyState are the options of the steady-state comparison, passed to
	// [rtcompare.Compare] unchanged.
	SteadyState rtcompare.CompareOptions

	// Build are the options of the build comparison. Each of its samples is a
	// whole build, which takes milliseconds rather than the microseconds of a
	// calibrated batch, so a zero Collect.Repeats selects [BuildRepeats] and a
	// zero ValidationRuns selects [BuildValidationRuns] rather than
	// rtcompare's defaults, and Collect.GCBetween is always set, since every
	// build leaves a whole structure of garbage behind that would otherwise be
	// collected in the middle of the next candidate's build.
	Build rtcompare.CompareOptions

	// SkipBuild omits the build comparison. The steady-state result alone
	// ignores what growing to size costs, and so favours structures whose
	// growth is expensive; set it only when that cost is known not to matter.
	SkipBuild bool
}

// BuildRepeats and BuildValidationRuns are the build comparison's defaults for
// Repeats and ValidationRuns. They are smaller than rtcompare's, because a
// sample is a whole build: with them a build comparison performs about 1,300
// builds, some ten seconds for 100,000 elements, where rtcompare's defaults
// would take several minutes.
const (
	BuildRepeats        = 31
	BuildValidationRuns = 10
)

// Result holds the two answers [Compare] gives, which are answers to two
// different questions and are deliberately not combined into one number.
type Result struct {
	// Target is the number of elements at rest, and CycleOps and BuildOps the
	// lengths of the two streams.
	Target             int
	CycleOps, BuildOps int

	// SteadyState compares the cost of one insertion or deletion in a
	// structure that has been in use for a while, averaged over the cycle. Its
	// per-operation figures are nanoseconds per insertion or deletion.
	SteadyState rtcompare.Report

	// Build compares the cost of building a structure from empty to Target
	// elements, with the transient insertions and deletions a real build sees,
	// including creation, every resize and the garbage it leaves. Its
	// per-operation figures are nanoseconds per whole build. It is the zero
	// Report when Options.SkipBuild was set.
	Build rtcompare.Report
}

// String renders both answers, each under the question it answers.
func (r Result) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "steady state, per insertion or deletion in a structure of %d elements (cycle of %d operations, first pass untimed):\n", r.Target, r.CycleOps)
	b.WriteString(indent(r.SteadyState.String()))
	if r.Build.SamplesA != nil {
		fmt.Fprintf(&b, "\n\nbuild from empty to %d elements, per whole build (%d operations):\n", r.Target, r.BuildOps)
		b.WriteString(indent(r.Build.String()))
	}
	return b.String()
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}

// Compare compares two data structures under a realistic mix of insertions
// and deletions, and answers two questions separately: what an operation costs
// once a structure is in use, and what it costs to build one.
//
// Parameters: target is the number of elements the structures hold at rest,
// at least one; a and b are the two structures, see [Structure]; opt
// configures the streams and both comparisons, see [Options].
//
// It returns both reports, or an error if a structure lacks New or Apply, if
// the configuration is invalid (see [Build] and [Cycle]), or if a comparison
// fails.
//
// Use it for any comparison of mutable data structures. It exists because
// doing this by hand has several traps, and each of them quietly changes the
// answer:
//
//   - The steady-state comparison builds one structure of each kind to target
//     elements with a [Build] stream, applied to the two alternately in chunks
//     so that neither is built last, and then replays a [Cycle] on each
//     through a [Replay]. The first pass of the cycle is untimed: it is where
//     the structure grows to the cycle's peak size, a one-time cost that would
//     otherwise be spread over the measurement in proportions that depend on
//     how long the run was.
//   - That growth is not dropped, it is measured where it belongs. The build
//     comparison times whole builds, each from a structure fresh from New to
//     target elements, so creation, capacity hints, every resize and the
//     garbage are included. A structure that cannot be presized and has to
//     grow pays for it here, and one that grows cheaply shows it here.
//
// The two answers can disagree, and then that is the result: one structure
// can be faster to use and slower to build. Read both.
//
// Both answers describe this one process. For structures of more than a few
// megabytes, or full of pointers, where the structures lie in memory moves the
// result by several points and differently in the next process; run the
// comparison through [Suite] with the multiproc package there.
//
// The cost is that of two [rtcompare.Compare] calls, the build one dominated
// by the builds it performs, about 1,300 at the defaults; see
// [BuildRepeats].
//
//	res, err := workload.Compare(100_000,
//		workload.Structure[map[uint64]struct{}]{Name: "map", New: newMap, Apply: applyToMap},
//		workload.Structure[*set3.Set3[uint64]]{Name: "Set3", New: newSet3, Apply: applyToSet3},
//		workload.Options{})
//	if err != nil { ... }
//	fmt.Println(res)
func Compare[SA, SB any](target int, a Structure[SA], b Structure[SB], opt Options) (Result, error) {
	return compare(target, a, b, opt, false)
}

// compare is Compare with a choice of which structure's build starts: A's, or
// B's when bFirst is set, which is how [Suite] alternates the build order
// between processes.
func compare[SA, SB any](target int, a Structure[SA], b Structure[SB], opt Options, bFirst bool) (Result, error) {
	if err := a.check("A"); err != nil {
		return Result{}, err
	}
	if err := b.check("B"); err != nil {
		return Result{}, err
	}
	cycle, err := Cycle(target, opt.Config)
	if err != nil {
		return Result{}, err
	}
	build, err := Build(target, opt.Config)
	if err != nil {
		return Result{}, err
	}
	res := Result{Target: target, CycleOps: len(cycle), BuildOps: len(build)}

	res.SteadyState, err = compareSteadyState(a, b, build, cycle, opt.SteadyState, bFirst)
	if err != nil {
		return res, fmt.Errorf("workload: steady-state comparison: %w", err)
	}
	if !opt.SkipBuild {
		res.Build, err = rtcompare.Compare(buildCandidate(a, build), buildCandidate(b, build), buildOptions(opt.Build))
		if err != nil {
			return res, fmt.Errorf("workload: build comparison: %w", err)
		}
	}
	return res, nil
}

// compareSteadyState builds one structure of each kind, alternately, and
// compares the cycle replayed on each.
func compareSteadyState[SA, SB any](a Structure[SA], b Structure[SB], build, cycle []Op, opt rtcompare.CompareOptions, bFirst bool) (rtcompare.Report, error) {
	sa, sb := a.New(), b.New()
	applyA := func(run []Op) { a.Apply(sa, run) }
	applyB := func(run []Op) { b.Apply(sb, run) }
	if bFirst {
		buildAlternately(build, applyB, applyA)
	} else {
		buildAlternately(build, applyA, applyB)
	}
	ra, rb := NewReplay(cycle, applyA), NewReplay(cycle, applyB)
	ca, cb := ra.Candidate(a.Name), rb.Candidate(b.Name)
	if opt.Collect.InnerLoops == 0 {
		n, err := calibrateBoth(ca, cb, opt.Collect)
		if err != nil {
			return rtcompare.Report{}, err
		}
		opt.Collect.InnerLoops = n
		// Calibration tried different batch sizes on the two, so their
		// cursors now stand at different points of the cycle, and every later
		// batch would replay a different stretch of it on each. Back to the
		// start, both of them, outside any measurement; from here on they
		// run the same number of batches of the same size and stay in step.
		ra.Settle()
		rb.Settle()
	}
	return rtcompare.Compare(ca, cb, opt)
}

// calibrateBoth sizes the batches for both candidates as rtcompare.Compare
// would, the larger of the two sizes, so that compareSteadyState can bring the
// replays back into step before the comparison starts.
func calibrateBoth(a, b rtcompare.Candidate, co rtcompare.CollectOptions) (uint64, error) {
	opt := rtcompare.CalibrationOptions{
		MaxQuantizationError: co.MaxQuantizationError,
		MaxInnerLoops:        co.MaxInnerLoops,
		GCBetween:            co.GCBetween,
		DisableGC:            co.DisableGC,
	}
	calA, err := rtcompare.CalibrateInnerLoops(a, opt)
	if err != nil {
		return 0, fmt.Errorf("calibrating %s: %w", a.Name, err)
	}
	calB, err := rtcompare.CalibrateInnerLoops(b, opt)
	if err != nil {
		return 0, fmt.Errorf("calibrating %s: %w", b.Name, err)
	}
	return max(calA.InnerLoops, calB.InnerLoops), nil
}

// alternateChunk is how many operations of the build stream go to one
// structure before the other gets its turn.
const alternateChunk = 256

// buildAlternately applies the stream to both structures in alternating
// chunks, each chunk to first and then to second. Built one after the other,
// the structure built second would be the one in the caches and in fresher
// memory when the measurement starts, which has been measured to make an
// identical structure a few percent faster. Chunks shrink that head start to
// one chunk, which is second's; [Suite] alternates which structure is second
// between processes, so that pooling averages the rest out.
func buildAlternately(ops []Op, first, second func([]Op)) {
	for start := 0; start < len(ops); start += alternateChunk {
		run := ops[start:min(start+alternateChunk, len(ops))]
		first(run)
		second(run)
	}
}

// buildCandidate times whole builds: each operation of its batch creates a
// structure and applies the entire build stream to it.
func buildCandidate[S any](s Structure[S], build []Op) rtcompare.Candidate {
	return rtcompare.Candidate{Name: s.Name, Batch: func(n uint64) {
		for range n {
			fresh := s.New()
			s.Apply(fresh, build)
			runtime.KeepAlive(fresh)
		}
	}}
}

// buildOptions fills in the build comparison's own defaults.
func buildOptions(opt rtcompare.CompareOptions) rtcompare.CompareOptions {
	if opt.Collect.Repeats == 0 {
		opt.Collect.Repeats = BuildRepeats
	}
	if opt.ValidationRuns == 0 {
		opt.ValidationRuns = BuildValidationRuns
	}
	opt.Collect.GCBetween = true
	return opt
}
