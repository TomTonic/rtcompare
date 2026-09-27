package workload

import (
	"strings"
	"testing"

	"github.com/TomTonic/rtcompare"
)

// countingSet is a set that counts how many operations it was given, and
// fails the test on an invalid one, for checking what Compare and Replay do
// to the structures they are handed.
type countingSet struct {
	t       *testing.T
	present map[uint32]bool
	applied int
}

func newCountingSet(t *testing.T) *countingSet {
	return &countingSet{t: t, present: map[uint32]bool{}}
}

func (s *countingSet) apply(run []Op) {
	for _, op := range run {
		if (op.Kind == Insert) == s.present[op.ID] {
			s.t.Fatalf("invalid %s of element %d", op.Kind, op.ID)
		}
		s.present[op.ID] = op.Kind == Insert
		if op.Kind == Delete {
			delete(s.present, op.ID)
		}
	}
	s.applied += len(run)
}

// TestReplayPrimesOnceBeforeTheFirstBatch checks the automatic handling of a
// cycle's first pass, in which a structure grows to its peak size once and
// never again. Replay's candidate has to play one full cycle in its Setup,
// outside the measured region, before the first batch and never again; the
// batches have to continue the cycle validly from there, and Settle has to
// bring the structure back to its start state.
func TestReplayPrimesOnceBeforeTheFirstBatch(t *testing.T) {
	const target = 200
	ops, err := Cycle(target, Config{Seed: 4})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := newCountingSet(t)
	s.apply(opsInserting(ids(target)))
	s.applied = 0
	r := NewReplay(ops, s.apply)
	c := r.Candidate("set")
	if c.Name != "set" || c.Setup == nil {
		t.Fatalf("candidate lacks its name or its priming Setup: %+v", c)
	}
	c.Setup()
	if s.applied != len(ops) {
		t.Errorf("priming applied %d operations, want one full cycle of %d", s.applied, len(ops))
	}
	c.Batch(7)
	c.Setup()
	c.Batch(uint64(len(ops)))
	if want := 2*len(ops) + 7; s.applied != want {
		t.Errorf("after priming and two batches %d operations were applied, want %d", s.applied, want)
	}
	r.Settle()
	if len(s.present) != target {
		t.Errorf("%d elements present after Settle, want %d", len(s.present), target)
	}
}

// opsInserting returns a stream that inserts the given IDs.
func opsInserting(xs []uint32) []Op {
	ops := make([]Op, len(xs))
	for i, id := range xs {
		ops[i] = Op{ID: id, Kind: Insert}
	}
	return ops
}

// TestBuildAlternatelyGivesNeitherStructureTheLastWord checks how Compare
// builds the two structures of the steady-state comparison: in alternating
// chunks, both receiving the whole stream in order, so that neither is the one
// built last. Built one after the other, the second has been measured to be a
// few percent faster for that alone.
func TestBuildAlternatelyGivesNeitherStructureTheLastWord(t *testing.T) {
	ops := opsInserting(ids(3*alternateChunk + 10))
	var order []string
	var gotA, gotB []Op
	buildAlternately(ops,
		func(run []Op) { order = append(order, "A"); gotA = append(gotA, run...) },
		func(run []Op) { order = append(order, "B"); gotB = append(gotB, run...) })
	if got := strings.Join(order, ""); got != "ABABABAB" {
		t.Errorf("chunk order: got %s, want ABABABAB", got)
	}
	if len(gotA) != len(ops) || len(gotB) != len(ops) || gotA[len(ops)-1] != ops[len(ops)-1] {
		t.Errorf("each structure should receive the whole stream in order: %d and %d of %d", len(gotA), len(gotB), len(ops))
	}
}

// TestBuildCandidateTimesWholeBuilds checks the unit of the build comparison:
// one operation is one complete build of a fresh structure, so that creation,
// resizing and everything else growth costs lands in each sample whole rather
// than in a few batches the median would ignore.
func TestBuildCandidateTimesWholeBuilds(t *testing.T) {
	build, err := Build(100, Config{Seed: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var sets []*countingSet
	s := Structure[*countingSet]{
		Name:  "counting",
		New:   func() *countingSet { cs := newCountingSet(t); sets = append(sets, cs); return cs },
		Apply: func(cs *countingSet, run []Op) { cs.apply(run) },
	}
	buildCandidate(s, build).Batch(3)
	if len(sets) != 3 {
		t.Fatalf("expected 3 fresh structures, got %d", len(sets))
	}
	for i, cs := range sets {
		if cs.applied != len(build) || len(cs.present) != 100 {
			t.Errorf("build %d: %d operations applied, %d elements present", i, cs.applied, len(cs.present))
		}
	}
}

// TestBuildOptionsDefaults checks the build comparison's own defaults, which
// keep a comparison of whole builds affordable, and that it always collects
// garbage between batches while leaving explicit settings alone.
func TestBuildOptionsDefaults(t *testing.T) {
	got := buildOptions(rtcompare.CompareOptions{})
	if got.Collect.Repeats != BuildRepeats || got.ValidationRuns != BuildValidationRuns || !got.Collect.GCBetween {
		t.Errorf("defaults not applied: %+v", got)
	}
	got = buildOptions(rtcompare.CompareOptions{Collect: rtcompare.CollectOptions{Repeats: 51}, ValidationRuns: 20})
	if got.Collect.Repeats != 51 || got.ValidationRuns != 20 || !got.Collect.GCBetween {
		t.Errorf("explicit settings not kept: %+v", got)
	}
}

// TestCompareRejectsIncompleteInput checks that Compare fails before
// measuring anything when a structure cannot be used or the streams cannot be
// generated, and names the problem.
func TestCompareRejectsIncompleteInput(t *testing.T) {
	ok := Structure[*countingSet]{Name: "ok", New: func() *countingSet { return newCountingSet(t) }, Apply: func(cs *countingSet, run []Op) { cs.apply(run) }}
	cases := []struct {
		name   string
		a      Structure[*countingSet]
		target int
		want   string
	}{
		{"returns error for a structure without New", Structure[*countingSet]{Name: "x", Apply: ok.Apply}, 10, `A ("x") needs both`},
		{"returns error for a structure without Apply", Structure[*countingSet]{Name: "x", New: ok.New}, 10, "needs both"},
		{"returns error for an empty target", ok, 0, "target"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Compare(c.target, c.a, ok, Options{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want an error mentioning %q", err, c.want)
			}
		})
	}
	if _, err := Compare(10, ok, Structure[*countingSet]{Name: "y"}, Options{}); err == nil || !strings.Contains(err.Error(), `B ("y")`) {
		t.Errorf("got %v, want an error naming structure B", err)
	}
}
