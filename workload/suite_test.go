package workload

import (
	"slices"
	"testing"
	"time"

	"github.com/TomTonic/rtcompare"
	"github.com/TomTonic/rtcompare/multiproc"
)

// mapStructure is a set as a Go map, created with or without a capacity hint,
// and optionally logging every run of operations it is handed.
func mapStructure(name string, hint int, log *[][2]uint32) Structure[map[uint64]struct{}] {
	return Structure[map[uint64]struct{}]{
		Name: name,
		New:  func() map[uint64]struct{} { return make(map[uint64]struct{}, hint) },
		Apply: func(m map[uint64]struct{}, run []Op) {
			if log != nil && len(run) > 0 {
				*log = append(*log, [2]uint32{run[0].ID, uint32(len(run))})
			}
			for _, op := range run {
				if op.Kind == Insert {
					m[op.Key] = struct{}{}
				} else {
					delete(m, op.Key)
				}
			}
		},
	}
}

// quickSuite keeps both comparisons of a Suite to a few milliseconds.
var quickSuite = Options{
	SteadyState: rtcompare.CompareOptions{
		Collect:        rtcompare.CollectOptions{Repeats: 11, InnerLoops: 500, WarmupDuration: time.Millisecond},
		SkipValidation: true,
		Resamples:      300,
	},
	Build: rtcompare.CompareOptions{
		Collect:        rtcompare.CollectOptions{Repeats: 11, InnerLoops: 1, WarmupDuration: time.Millisecond},
		SkipValidation: true,
		Resamples:      300,
	},
}

// TestSuitePoolsBothAnswersAcrossProcesses checks the one-call path for data
// structures too large for one process to judge: the workload comparison runs
// in real child processes, restricted to this test, and both of its answers,
// steady state and build, come back pooled under their own names.
func TestSuitePoolsBothAnswersAcrossProcesses(t *testing.T) {
	res := multiproc.RunTestSuite(t, multiproc.Options{MinProcesses: 3, MaxProcesses: 4, AbsPrecision: 1},
		Suite("sets", 2000, mapStructure("hinted", 2000, nil), mapStructure("unhinted", 0, nil), quickSuite))
	var names []string
	for _, c := range res.Comparisons {
		names = append(names, c.Name)
		if c.Pooled.Processes != 4 {
			t.Errorf("%s pooled over %d processes, want 4", c.Name, c.Pooled.Processes)
		}
	}
	if want := []string{"sets" + SteadyStateSuffix, "sets" + BuildSuffix}; !slices.Equal(names, want) {
		t.Errorf("recorded %q, want %q", names, want)
	}
}

// TestSuiteAlternatesBuildOrder checks the protection against the head start
// of the structure built last: in a multiproc run, A's build has to come
// first in even processes and B's in odd ones, without the caller arranging
// it, so that pooling averages the head start out.
func TestSuiteAlternatesBuildOrder(t *testing.T) {
	opt := quickSuite
	opt.SkipBuild = true
	for _, c := range []struct {
		index int
		first string
	}{{0, "A"}, {1, "B"}, {2, "A"}, {3, "B"}} {
		var order []string
		logging := func(label string) Structure[map[uint64]struct{}] {
			s := mapStructure(label, 0, nil)
			apply := s.Apply
			s.Apply = func(m map[uint64]struct{}, run []Op) {
				order = append(order, label)
				apply(m, run)
			}
			return s
		}
		if err := Suite("x", 1000, logging("A"), logging("B"), opt)(&multiproc.Process{Index: c.index}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if order[0] != c.first {
			t.Errorf("process %d built %s first, want %s", c.index, order[0], c.first)
		}
	}
}

// TestSteadyStateReplaysInStep checks that the two structures of a
// steady-state comparison are measured on the same stretches of the cycle.
// Calibration tries different batch sizes on each, which would leave their
// positions in the cycle apart for the whole comparison; after it, both have
// to replay exactly the same runs of operations, batch for batch.
func TestSteadyStateReplaysInStep(t *testing.T) {
	const target = 2000
	build, err := Build(target, Config{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cycle, err := Cycle(target, Config{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var logA, logB [][2]uint32
	opt := rtcompare.CompareOptions{
		Collect:        rtcompare.CollectOptions{Repeats: 11, WarmupDuration: time.Millisecond, MaxQuantizationError: 0.01},
		SkipValidation: true,
		Resamples:      300,
	}
	// Different costs, so that calibration sizes the two differently.
	if _, err := compareSteadyState(mapStructure("a", target, &logA), mapStructure("b", 0, &logB), build, cycle, opt, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	const tail = 24
	if len(logA) < tail || len(logB) < tail {
		t.Fatalf("too few runs to compare: %d and %d", len(logA), len(logB))
	}
	if a, b := logA[len(logA)-tail:], logB[len(logB)-tail:]; !slices.Equal(a, b) {
		t.Errorf("the structures replayed different stretches of the cycle:\nA %v\nB %v", a, b)
	}
}
