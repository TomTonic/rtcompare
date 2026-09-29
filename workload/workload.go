// Package workload generates realistic streams of insertions and deletions for
// benchmarking mutable data structures such as maps, sets, trees, tries,
// indexes, caches and queues with rtcompare.
//
// A hand-written "insert an element, then remove the same element" loop
// measures a structure that never changes shape: the node splits, merges,
// resizes, rehashes and tombstones that dominate real workloads never happen.
// Getting a realistic stream right by hand runs into four traps, and this
// package exists to avoid them:
//
//  1. Invalid operations. A deletion must target an element that is present
//     at that point of the stream, and an insertion one that is not, or part of
//     the timed work is a no-op. Every stream here is valid by construction,
//     and [Check] proves it against a model.
//  2. Overhead in the timed loop. Choosing a victim while timing adds cache
//     misses and branches to every candidate and dilutes the difference between
//     them. The streams are precomputed; the timed loop only reads the next
//     entry, sequentially.
//  3. State drift across batches. rtcompare calls a batch many times, and a
//     structure that grows or shrinks without bound is a different structure at
//     the last sample than at the first. A [Cycle] ends exactly in its start
//     state, so a [Replay] can repeat it endlessly without an untimed rebuild.
//  4. Shared state across comparisons. The position in a cycle belongs to the
//     data structure instance, not to the batch closure. A [Replay] carries
//     it with the structure, and [Replay.Settle] brings the structure back to
//     its start state before anything else is measured on it.
//
// Two more traps sit in the measurement itself. The first pass of a cycle is
// where a structure grows to its peak size, a one-time cost that must neither
// be spread over a steady-state measurement nor be dropped. And whichever of
// two structures is built last starts ahead.
//
// [Compare] does all of this in one call and answers the two questions a
// mutation benchmark has, what an operation costs in steady state and what a
// build costs, separately. [Cycle], [Build], [Replay], [Cursor] and [Check]
// are the parts it is made of, for setups it does not cover.
//
// Every operation names its element twice: by an ID, which says which element
// it is, and by a Key, which is what to insert into or delete from the
// structure. IDs 0 to target-1 are the elements the structure holds at rest,
// and transient IDs follow from target upwards, each inserted once and deleted
// once. That makes the IDs sequential, and the transient ones all larger than
// the permanent ones, which is the one arrangement a benchmark must not use as
// keys: an ordered structure would see every insertion at its right edge. The
// keys are therefore scattered over the whole uint64 range by default, see
// [KeyOrder]. Use op.Key for integer keys, or map op.ID to keys of your own
// through a precomputed slice, and keep the mapping out of the timed loop.
package workload

import (
	"fmt"
	"math"

	"github.com/TomTonic/rtcompare"
)

// Kind is what an [Op] does.
type Kind uint8

const (
	// Insert adds an element that is not present.
	Insert Kind = iota
	// Delete removes an element that is present.
	Delete
)

// String implements [fmt.Stringer].
func (k Kind) String() string {
	switch k {
	case Insert:
		return "insert"
	case Delete:
		return "delete"
	default:
		return fmt.Sprintf("Kind(%d)", uint8(k))
	}
}

// Op is one mutation of a stream: which element, and what to do with it. It
// occupies 16 bytes, so a cycle of 12.7 million operations takes about 200 MB.
type Op struct {
	// Key is the element's key, the value to insert into or delete from the
	// structure under test. Keys are distinct for distinct IDs, and the same
	// ID has the same key in every stream made with the same Config, so a
	// [Build] and a [Cycle] fit together. See [KeyOrder] for how they are
	// chosen.
	Key uint64

	// ID identifies the element among those of the stream: 0 to target-1 for
	// the elements present at rest, target upwards for transient ones.
	ID uint32

	// Kind is what the operation does.
	Kind Kind
}

// KeyOrder chooses how IDs become keys, see Config.Keys.
type KeyOrder int

const (
	// Scattered maps IDs to keys by a seeded permutation of the uint64 range,
	// so that the keys of permanent and transient elements are interleaved
	// and in no particular order, as the keys of a real workload are. It is
	// the zero value, and the right choice for any structure that orders its
	// keys or hashes them.
	Scattered KeyOrder = iota

	// Sequential uses the ID itself as the key. The permanent keys are then 0
	// to target-1 and every transient key lies above all of them, so an
	// ordered structure takes every insertion at its right edge. Choose it
	// only to measure exactly that, such as append-only time series keys.
	Sequential
)

// String implements [fmt.Stringer].
func (o KeyOrder) String() string {
	switch o {
	case Scattered:
		return "Scattered"
	case Sequential:
		return "Sequential"
	default:
		return fmt.Sprintf("KeyOrder(%d)", int(o))
	}
}

// Policy decides which present transient element a deletion removes.
type Policy int

const (
	// Uniform deletes a transient element drawn uniformly from those present,
	// so that some are deleted soon after their insertion and some long after.
	// It is the zero value, and what a general-purpose map or set sees.
	Uniform Policy = iota

	// FIFO deletes the oldest transient element present, as a queue, a
	// time-series retention window or a log compaction does.
	FIFO

	// LIFO deletes the youngest transient element present, as a stack or an
	// undo log does.
	LIFO
)

// String implements [fmt.Stringer].
func (p Policy) String() string {
	switch p {
	case Uniform:
		return "Uniform"
	case FIFO:
		return "FIFO"
	case LIFO:
		return "LIFO"
	default:
		return fmt.Sprintf("Policy(%d)", int(p))
	}
}

// DefaultRatio is the Config.Ratio used when it is left at zero: twice as many
// insertions as elements at rest, within the 1.5 to 3 that resembles a
// database index.
const DefaultRatio = 2.0

// DefaultMaxBurst is the Config.MaxBurst used when it is left at zero.
const DefaultMaxBurst = 16

// Config configures [Build] and [Cycle]. The zero value is usable and selects
// the documented defaults.
type Config struct {
	// Seed makes a stream reproducible: the same seed and configuration give
	// the same stream. Zero is a seed like any other.
	Seed uint64

	// Ratio is the number of insertions per element at rest. Build inserts
	// Ratio*target elements in all, of which target remain; Cycle inserts and
	// deletes (Ratio-1)*target transient elements. Zero selects DefaultRatio.
	// Build requires at least 1, Cycle more than 1.
	Ratio float64

	// MaxBurst bounds the length of a burst of insertions, which is drawn
	// uniformly from 1 to MaxBurst. Zero selects DefaultMaxBurst.
	MaxBurst int

	// LiveTarget is the number of transient elements the stream keeps present
	// on average: the more are present, the longer the deletion bursts. Zero
	// selects target/8, but no more than half the transient elements, and at
	// least one.
	LiveTarget int

	// Victims chooses which transient element a deletion removes. The zero
	// value is Uniform.
	Victims Policy

	// Keys chooses how IDs become keys. The zero value is Scattered, derived
	// from Seed.
	Keys KeyOrder
}

// Key returns the key of the element with the given ID, as the streams made
// with this Config carry it in Op.Key.
//
// Use it to fill a structure with the elements present at rest, IDs 0 to
// target-1, before replaying a [Cycle] on it, or to look up the elements a
// stream holds. It computes the key, a few nanoseconds, so read Op.Key in a
// timed loop rather than calling it there.
func (c Config) Key(id uint32) uint64 {
	if c.Keys == Sequential {
		return uint64(id)
	}
	return scatter(id, c.Seed)
}

// scatter is a permutation of the uint64 range applied to the ID, keyed by the
// seed: the ID is combined with a seed-derived constant and then mixed with
// the finaliser of MurmurHash3, which is a bijection, so that distinct IDs can
// never share a key.
func scatter(id uint32, seed uint64) uint64 {
	z := uint64(id) ^ mixSeed(seed^0x6b6579730a6b6579)
	z = (z ^ (z >> 33)) * 0xff51afd7ed558ccd
	z = (z ^ (z >> 33)) * 0xc4ceb9fe1a85ec53
	return z ^ (z >> 33)
}

// Build returns a stream that takes a structure from empty to holding exactly
// the IDs 0 to target-1, the way it would have got there in real use: with
// transient elements inserted and deleted along the way.
//
// Parameters: target is the number of elements at the end, at least one; c
// configures the stream, see [Config]. With Ratio r the stream holds r*target
// insertions, target of them permanent and the rest transient, and a deletion
// for each transient one. Bursts of insertions alternate with bursts of
// deletions, and the permanent elements are inserted in random order, spread
// across the whole stream.
//
// It returns the stream, or an error for a target below one, an invalid
// configuration, or more elements than fit in a uint32 ID.
//
// Use it to build a fixture with a realistic history, since a structure built
// by inserting its final elements in order can look quite different inside
// from one that has seen deletions, or to time the build itself. Replay it
// once, from the start; unlike a [Cycle] it does not return to where it began.
func Build(target int, c Config) ([]Op, error) {
	c, transients, err := c.resolve(target, 1)
	if err != nil {
		return nil, err
	}
	s := newSimulation(c, target, transients)
	s.permanent = shuffledIDs(target, &s.rng)
	s.run()
	return s.ops, nil
}

// Cycle returns a stream that starts with the IDs 0 to target-1 present,
// inserts and deletes transient elements, and ends with exactly the IDs 0 to
// target-1 present again, so that it can be replayed endlessly.
//
// Parameters: target is the number of elements present at rest, at least
// one; c configures the stream, see [Config]. With Ratio r the cycle inserts
// and deletes (r-1)*target transient elements in alternating bursts, keeping
// about LiveTarget of them present at a time. The elements present at rest are
// never deleted.
//
// It returns the stream, or an error for a target below one, an invalid
// configuration, a ratio at which no transient element would be inserted, or
// more elements than fit in a uint32 ID.
//
// Use it for steady-state mutation benchmarks: build the structure with the
// elements of IDs 0 to target-1, from a [Build] stream or with the keys from
// Config.Key, then replay the cycle through a [Replay] in the batch. Because
// the cycle ends in its start state, a batch can wrap around to the beginning,
// and the structure the last sample measures is the one the first sample
// measured. At 1M elements and r = 2 a cycle has 2M operations and takes
// 32 MB.
func Cycle(target int, c Config) ([]Op, error) {
	c, transients, err := c.resolve(target, 0)
	if err != nil {
		return nil, err
	}
	if transients == 0 {
		return nil, fmt.Errorf("workload: a cycle with Ratio %v over %d elements inserts no transient element; raise Ratio", c.Ratio, target)
	}
	s := newSimulation(c, target, transients)
	s.run()
	return s.ops, nil
}

// resolve checks the configuration, fills in its defaults and returns the
// number of transient elements. minRatio is 1 for Build, where a ratio of one
// is a plain build, and 0 for Cycle, whose ratio must exceed one.
func (c Config) resolve(target int, minRatio float64) (Config, int, error) {
	if target < 1 {
		return c, 0, fmt.Errorf("workload: target must be at least 1, got %d", target)
	}
	if c.Ratio == 0 {
		c.Ratio = DefaultRatio
	}
	if math.IsNaN(c.Ratio) || math.IsInf(c.Ratio, 0) || c.Ratio < 1 || (minRatio == 0 && c.Ratio <= 1) {
		return c, 0, fmt.Errorf("workload: Ratio must be at least 1 for Build and above 1 for Cycle, got %v", c.Ratio)
	}
	if c.MaxBurst < 0 || c.LiveTarget < 0 {
		return c, 0, fmt.Errorf("workload: MaxBurst and LiveTarget must not be negative, got %d and %d", c.MaxBurst, c.LiveTarget)
	}
	switch c.Victims {
	case Uniform, FIFO, LIFO:
	default:
		return c, 0, fmt.Errorf("workload: unknown Victims policy %d", int(c.Victims))
	}
	switch c.Keys {
	case Scattered, Sequential:
	default:
		return c, 0, fmt.Errorf("workload: unknown KeyOrder %d", int(c.Keys))
	}
	transients := math.Round((c.Ratio - 1) * float64(target))
	if float64(target)+transients > math.MaxUint32 {
		return c, 0, fmt.Errorf("workload: %d elements and %.0f transient ones do not fit in uint32 IDs", target, transients)
	}
	if c.MaxBurst == 0 {
		c.MaxBurst = DefaultMaxBurst
	}
	if c.LiveTarget == 0 {
		c.LiveTarget = max(1, min(target/8, int(transients)/2))
	}
	return c, int(transients), nil
}

// shuffledIDs returns 0 to n-1 in random order, by Fisher-Yates.
func shuffledIDs(n int, rng *rtcompare.DPRNG) []uint32 {
	ids := make([]uint32, n)
	for i := range ids {
		ids[i] = uint32(i)
	}
	for i := n - 1; i > 0; i-- {
		j := rng.Uint32N(uint32(i + 1))
		ids[i], ids[j] = ids[j], ids[i]
	}
	return ids
}
