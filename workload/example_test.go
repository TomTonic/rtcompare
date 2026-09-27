package workload_test

import (
	"fmt"
	"testing"

	set3 "github.com/TomTonic/Set3"
	"github.com/TomTonic/rtcompare"
	"github.com/TomTonic/rtcompare/workload"
)

// mapCandidate and set3Candidate each own one set of the IDs 0 to target-1 and
// the cursor that belongs to it, and replay the cycle on it. Besides the
// candidate they return a function that settles the set back to its start
// state and one that reports its size.
func mapCandidate(ops []workload.Op, target int) (rtcompare.Candidate, func(), func() int) {
	m := make(map[uint64]struct{}, target)
	for id := range uint64(target) {
		m[id] = struct{}{}
	}
	cur := &workload.Cursor{}
	apply := func(run []workload.Op) {
		for _, op := range run {
			if op.Kind == workload.Insert {
				m[uint64(op.ID)] = struct{}{}
			} else {
				delete(m, uint64(op.ID))
			}
		}
	}
	// One full cycle, untimed, so that the growth to the cycle's peak size
	// happens here rather than in the first measured batches.
	cur.Advance(ops, uint64(len(ops)), apply)
	settle := func() { cur.Settle(ops, apply) }
	return rtcompare.Candidate{Name: "map", Batch: cur.Batch(ops, apply)}, settle, func() int { return len(m) }
}

func set3Candidate(ops []workload.Op, target int) (rtcompare.Candidate, func(), func() int) {
	s := set3.EmptyWithCapacity[uint64](uint32(target))
	for id := range uint64(target) {
		s.Add(id)
	}
	cur := &workload.Cursor{}
	apply := func(run []workload.Op) {
		for _, op := range run {
			if op.Kind == workload.Insert {
				s.Add(uint64(op.ID))
			} else {
				s.Remove(uint64(op.ID))
			}
		}
	}
	cur.Advance(ops, uint64(len(ops)), apply)
	settle := func() { cur.Settle(ops, apply) }
	return rtcompare.Candidate{Name: "Set3", Batch: cur.Batch(ops, apply)}, settle, func() int { return int(s.Size()) }
}

// ExampleCycle compares Go's built-in map with Set3 under a steady mix of
// insertions and deletions. Each set starts with 100,000 elements, and the
// cycle inserts and deletes another 100,000 transient ones in bursts, so both
// sets grow, shrink, rehash and leave tombstones the way they would in a
// program, and are back where they started at the end of every cycle.
func ExampleCycle() {
	const target = 100_000
	ops, err := workload.Cycle(target, workload.Config{Seed: 1, Ratio: 2})
	if err != nil {
		panic(err)
	}
	a, settleA, _ := mapCandidate(ops, target)
	b, settleB, _ := set3Candidate(ops, target)

	report, err := rtcompare.Compare(a, b, rtcompare.CompareOptions{})
	if err != nil {
		panic(err)
	}
	// Bring both sets back to their start state before measuring anything
	// else on them.
	settleA()
	settleB()
	fmt.Println(report)
}

// TestCycleDrivesACompare runs a reduced version of ExampleCycle, which is not
// run by go test since its output varies. It checks that a cycle replayed
// through Cursors works as rtcompare candidates end to end: the comparison
// completes, and settling brings both sets back to their start state.
func TestCycleDrivesACompare(t *testing.T) {
	const target = 5000
	ops, err := workload.Cycle(target, workload.Config{Seed: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	a, settleA, sizeA := mapCandidate(ops, target)
	b, settleB, sizeB := set3Candidate(ops, target)
	if _, err := rtcompare.Compare(a, b, rtcompare.CompareOptions{
		Collect:        rtcompare.CollectOptions{Repeats: 11, InnerLoops: 2000},
		SkipValidation: true,
		Resamples:      300,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	settleA()
	settleB()
	if sizeA() != target || sizeB() != target {
		t.Errorf("after settling, the sets hold %d and %d elements, want %d", sizeA(), sizeB(), target)
	}
}
