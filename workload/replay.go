package workload

import (
	"fmt"
	"slices"

	"github.com/TomTonic/rtcompare"
)

// Cursor is a position in a [Cycle], and belongs to the data structure
// instance the cycle is replayed on.
//
// It exists because the position is a property of the structure, not of the
// batch function: after half a cycle, the structure holds the transient
// elements of that half, and only a replay that continues from there stays
// valid. A new batch closure that started again at zero would insert elements
// that are already present. Keep one Cursor per instance, next to it, and use
// it for every comparison the instance takes part in.
//
// The zero value is at the start of the cycle.
type Cursor struct {
	pos int
}

// Position returns the index of the next operation to be applied.
func (c *Cursor) Position() int { return c.pos }

// Batch returns an [rtcompare.Batch] that applies the next n operations of ops
// on every call, wrapping around at the end of the cycle.
//
// Parameters: ops is a stream from [Cycle], and must be the same on every call
// with this Cursor; apply performs the operations it is handed on the data
// structure. apply receives contiguous runs of the stream, so the loop over
// them sits in the caller's code, where the compiler can inline the data
// structure's methods; a run is split at the end of the cycle, so apply may be
// called more than once per batch.
//
// Use it as a candidate's Batch. One operation is one unit of work: the
// per-operation cost rtcompare reports is the average of the insertions and
// deletions in the stream.
//
// Play one full cycle through Advance before measuring. The first pass is
// where the structure grows to the largest size the cycle reaches, and it is
// far more expensive than every pass after it: for a Go map of 100,000
// elements at Ratio 2, stretches of the first pass cost 35 to 128 ns per
// operation where later passes cost 17 to 20. That growth happens once in a
// program's life and does not belong in a steady-state measurement.
//
//	cur := &workload.Cursor{}
//	candidate := rtcompare.Candidate{Name: "map", Batch: cur.Batch(ops, func(run []workload.Op) {
//		for _, op := range run {
//			if op.Kind == workload.Insert {
//				m[uint64(op.ID)] = struct{}{}
//			} else {
//				delete(m, uint64(op.ID))
//			}
//		}
//	})}
//
// An empty ops makes a batch that does nothing.
func (c *Cursor) Batch(ops []Op, apply func([]Op)) rtcompare.Batch {
	return func(n uint64) { c.Advance(ops, n, apply) }
}

// Advance applies the next n operations of ops through apply, wrapping around
// at the end of the cycle, and moves the cursor past them. It is what the
// function returned by Batch does, for callers who drive the replay
// themselves.
func (c *Cursor) Advance(ops []Op, n uint64, apply func([]Op)) {
	if len(ops) == 0 {
		return
	}
	for n > 0 {
		run := ops[c.pos:]
		if uint64(len(run)) > n {
			run = run[:n]
		}
		apply(run)
		n -= uint64(len(run))
		c.pos += len(run)
		if c.pos == len(ops) {
			c.pos = 0
		}
	}
}

// Settle applies the rest of the cycle, if the cursor is not at its start, so
// that the structure is back in the state the cycle starts from: exactly the
// IDs 0 to target-1 present.
//
// Call it outside the measured region, for instance in a Candidate's Teardown
// or between comparisons, before anything else is measured on the structure.
func (c *Cursor) Settle(ops []Op, apply func([]Op)) {
	if c.pos == 0 || len(ops) == 0 {
		return
	}
	apply(ops[c.pos:])
	c.pos = 0
}

// Check replays ops against a model set that starts as start, and reports the
// first operation that would be invalid: an insertion of a present element, a
// deletion of an absent one, or an unknown Kind. It also reports whether the
// model ends as end, ignoring order. It returns nil if the stream is valid.
//
// Use it in a test of anything that produces or transforms a stream. For a
// [Build] stream over target elements, start is empty and end is 0 to
// target-1; for a [Cycle], both are 0 to target-1.
func Check(ops []Op, start, end []uint32) error {
	present := make(map[uint32]struct{}, len(start))
	for _, id := range start {
		if _, dup := present[id]; dup {
			return fmt.Errorf("workload: start lists element %d twice", id)
		}
		present[id] = struct{}{}
	}
	for i, op := range ops {
		_, has := present[op.ID]
		switch {
		case op.Kind == Insert && has:
			return fmt.Errorf("workload: op %d inserts element %d, which is already present", i, op.ID)
		case op.Kind == Insert:
			present[op.ID] = struct{}{}
		case op.Kind == Delete && !has:
			return fmt.Errorf("workload: op %d deletes element %d, which is not present", i, op.ID)
		case op.Kind == Delete:
			delete(present, op.ID)
		default:
			return fmt.Errorf("workload: op %d has unknown %s", i, op.Kind)
		}
	}
	return compareEnd(present, end)
}

// compareEnd reports the first difference between the model's final contents
// and the expected ones.
func compareEnd(present map[uint32]struct{}, end []uint32) error {
	want := make(map[uint32]struct{}, len(end))
	for _, id := range end {
		want[id] = struct{}{}
		if _, ok := present[id]; !ok {
			return fmt.Errorf("workload: element %d should be present at the end, but is not", id)
		}
	}
	var extra []uint32
	for id := range present {
		if _, ok := want[id]; !ok {
			extra = append(extra, id)
		}
	}
	if len(extra) > 0 {
		slices.Sort(extra)
		return fmt.Errorf("workload: %d elements are present at the end that should not be, the first being %d", len(extra), extra[0])
	}
	return nil
}
