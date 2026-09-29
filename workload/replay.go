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
// Prefer [Replay], which also plays the first pass untimed; with a bare Cursor
// that is up to the caller, see [Replay] for why it matters.
//
//	cur := &workload.Cursor{}
//	candidate := rtcompare.Candidate{Name: "map", Batch: cur.Batch(ops, func(run []workload.Op) {
//		for _, op := range run {
//			switch op.Kind {
//			case workload.Insert:
//				m[op.Key] = struct{}{}
//			case workload.Delete:
//				delete(m, op.Key)
//			default: // Lookup and LookupMiss, if Config asks for lookups
//				_, found := m[op.Key]
//				hits += found
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
// deletion of an absent one, a Lookup of an absent one, a LookupMiss of a
// present one, or an unknown Kind. It also reports whether the
// model ends as end, ignoring order. It returns nil if the stream is valid.
//
// Use it in a test of anything that produces or transforms a stream. For a
// [Build] stream over target elements, start is empty and end is 0 to
// target-1; for a [Cycle], both are 0 to target-1.
func Check(ops []Op, start, end []uint32) error {
	// Sized for the largest the model can get, so that it never grows while
	// the stream is replayed.
	inserts := 0
	for _, op := range ops {
		if op.Kind == Insert {
			inserts++
		}
	}
	present := make(map[uint32]struct{}, len(start)+inserts)
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
		case op.Kind == Lookup && !has:
			return fmt.Errorf("workload: op %d looks up element %d as present, which it is not", i, op.ID)
		case op.Kind == LookupMiss && has:
			return fmt.Errorf("workload: op %d looks up element %d as absent, which it is not", i, op.ID)
		case op.Kind == Lookup, op.Kind == LookupMiss:
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

// Replay plays a [Cycle] on one data structure instance. It bundles the
// stream, the function that applies operations to the structure, and the
// position in the cycle, so that they cannot be mismatched: create one Replay
// per structure instance, next to the structure.
//
// It also takes care of the first pass. That pass is where the structure grows
// to the largest size the cycle reaches, and it costs far more than every pass
// after it: for a Go map of 100,000 elements at Ratio 2, stretches of the first
// pass cost 35 to 128 ns per operation where later passes cost 17 to 20. That
// growth happens once in a program's life and would otherwise be spread over
// the measurement, in proportions that depend on how long it ran. The
// candidate from [Replay.Candidate] therefore plays one full cycle, untimed,
// before its first measured batch. What the growth costs is a question of its
// own, which [Compare] answers separately with a [Build] stream.
type Replay struct {
	ops    []Op
	apply  func([]Op)
	cursor Cursor
	primed bool
}

// NewReplay returns a Replay of ops, a stream from [Cycle], applied to one
// structure through apply. apply receives contiguous runs of the stream, so
// that its loop over them sits where the compiler can inline the structure's
// methods; see [Cursor.Batch].
func NewReplay(ops []Op, apply func([]Op)) *Replay {
	return &Replay{ops: ops, apply: apply}
}

// Prime plays one full cycle, untimed, the first time it is called, and does
// nothing after that. The candidate calls it before its first batch; call it
// yourself only when driving the replay by other means.
func (r *Replay) Prime() {
	if r.primed {
		return
	}
	r.primed = true
	r.cursor.Advance(r.ops, uint64(len(r.ops)), r.apply)
}

// Candidate returns an [rtcompare.Candidate] that replays the cycle, continuing
// where the previous batch stopped. Its Setup primes the structure before the
// first batch, outside the measured region, and does nothing after that.
func (r *Replay) Candidate(name string) rtcompare.Candidate {
	return rtcompare.Candidate{
		Name:  name,
		Setup: r.Prime,
		Batch: func(n uint64) { r.cursor.Advance(r.ops, n, r.apply) },
	}
}

// Settle plays the rest of the cycle, so that the structure is back in the
// state the cycle starts from. Call it before measuring anything else on the
// structure.
func (r *Replay) Settle() {
	r.cursor.Settle(r.ops, r.apply)
}
