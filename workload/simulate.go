package workload

import "github.com/TomTonic/rtcompare"

// simulation produces a stream by playing it out: it tracks which transient
// elements are present at every point, so that a deletion can only name one
// that is and an insertion only one that is not. That is what makes the
// streams valid by construction.
type simulation struct {
	c   Config
	rng rtcompare.DPRNG
	ops []Op

	// permanent holds the elements to be inserted that remain at the end,
	// taken from the back; empty for a cycle, whose permanent elements are
	// present from the start.
	permanent []uint32

	nextTransient  uint32 // the next fresh transient ID
	transientsLeft int    // transient insertions still to be made
	live           liveSet

	// meanTransients is the expected number of transient insertions per burst,
	// which the deletion bursts are scaled to so that the number of transient
	// elements present settles at LiveTarget.
	meanTransients float64

	// The optional parts, all nil or zero unless Config asks for them, so
	// that a stream without them draws exactly the random numbers it always
	// did. present holds every element present, for lookups that hit;
	// permanentPresent the permanent ones, for churn to pick from; pending
	// the permanent ones churn deleted, to be inserted again.
	present          *indexSet
	permanentPresent *indexSet
	pending          []uint32
	lookupCredit     float64
	target           uint32 // the first transient ID; permanent ones lie below
	missBase         uint32 // the first ID that is never inserted
}

// newSimulation prepares a stream over target permanent and transients
// transient elements; atRest says whether the permanent ones are present from
// the start, as in a cycle, or still to be inserted, as in a build.
func newSimulation(c Config, target, transients int, atRest bool) *simulation {
	s := &simulation{
		c:              c,
		rng:            rtcompare.NewDPRNG(rngSeed(c.Seed)),
		nextTransient:  uint32(target),
		transientsLeft: transients,
		live:           liveSet{policy: c.Victims},
		target:         uint32(target),
		missBase:       uint32(target + transients),
	}
	if c.Lookups > 0 {
		s.present = newIndexSet(target + transients)
	}
	if c.PermanentChurn > 0 {
		s.permanentPresent = newIndexSet(target)
	}
	if atRest {
		for id := range uint32(target) {
			s.markPresent(id)
		}
	}
	return s
}

// run alternates bursts of insertions and deletions until every insertion is
// made, then deletes the transient elements still present.
func (s *simulation) run() {
	pending := len(s.permanent) + s.transientsLeft
	s.ops = make([]Op, 0, pending+s.transientsLeft)
	meanBurst := float64(s.c.MaxBurst+1) / 2
	s.meanTransients = meanBurst * float64(s.transientsLeft) / float64(pending)
	for len(s.permanent)+s.transientsLeft > 0 {
		s.insertBurst()
		s.deleteBurst()
	}
	// Permanent elements that churn took out go back in before the stream
	// ends, so that it ends with exactly the permanent elements present.
	for len(s.pending) > 0 {
		last := len(s.pending) - 1
		s.insert(s.pending[last])
		s.pending = s.pending[:last]
	}
	for s.live.len() > 0 {
		for range min(1+int(s.rng.Uint32N(uint32(s.c.MaxBurst))), s.live.len()) {
			s.delete(s.live.remove(&s.rng))
		}
	}
}

// op makes the operation on the element with the given ID, with its key.
func (s *simulation) op(id uint32, kind Kind) Op {
	return Op{Key: s.c.Key(id), ID: id, Kind: kind}
}

// insert appends the insertion of id and the lookups that follow it.
func (s *simulation) insert(id uint32) {
	s.ops = append(s.ops, s.op(id, Insert))
	s.markPresent(id)
	s.lookups()
}

// delete appends the deletion of id and the lookups that follow it.
func (s *simulation) delete(id uint32) {
	s.ops = append(s.ops, s.op(id, Delete))
	if s.present != nil {
		s.present.remove(id)
	}
	if s.permanentPresent != nil && id < s.target {
		s.permanentPresent.remove(id)
	}
	s.lookups()
}

// markPresent records that id is present, for the optional parts that need
// to know.
func (s *simulation) markPresent(id uint32) {
	if s.present != nil {
		s.present.add(id)
	}
	if s.permanentPresent != nil && id < s.target {
		s.permanentPresent.add(id)
	}
}

// lookups appends the lookups owed after one mutation: Lookups of them on
// average, spread evenly, each missing with probability MissRate.
func (s *simulation) lookups() {
	if s.c.Lookups == 0 {
		return
	}
	s.lookupCredit += s.c.Lookups
	for s.lookupCredit >= 1 {
		s.lookupCredit--
		if s.present.len() == 0 || s.rng.Float64() < s.c.MissRate {
			s.ops = append(s.ops, s.op(s.missBase+s.rng.Uint32N(missPool(s.missBase)), LookupMiss))
		} else {
			s.ops = append(s.ops, s.op(s.present.random(&s.rng), Lookup))
		}
	}
}

// insertBurst inserts between 1 and MaxBurst elements. Each one is a new
// transient, a permanent one that churn took out, or a permanent one still to
// be inserted by a build, in proportion to how many of each are left, so that
// all kinds are spread evenly over the stream.
func (s *simulation) insertBurst() {
	for range 1 + s.rng.Uint32N(uint32(s.c.MaxBurst)) {
		left := len(s.permanent) + s.transientsLeft + len(s.pending)
		if left == 0 {
			return
		}
		r := int(s.rng.Uint32N(uint32(left)))
		switch {
		case r < s.transientsLeft:
			id := s.nextTransient
			s.nextTransient++
			s.transientsLeft--
			s.live.add(id)
			s.insert(id)
		case r < s.transientsLeft+len(s.pending):
			j, last := r-s.transientsLeft, len(s.pending)-1
			id := s.pending[j]
			s.pending[j] = s.pending[last]
			s.pending = s.pending[:last]
			s.insert(id)
		default:
			last := len(s.permanent) - 1
			id := s.permanent[last]
			s.permanent = s.permanent[:last]
			s.insert(id)
		}
	}
}

// deleteBurst deletes a number of transient elements whose expectation is the
// expected number of transient insertions per burst, scaled by how far the
// elements present exceed LiveTarget. At LiveTarget insertions and deletions
// balance, so that is where the count settles.
func (s *simulation) deleteBurst() {
	n := s.live.len()
	if n == 0 {
		return
	}
	mean := s.meanTransients * float64(n) / float64(s.c.LiveTarget)
	// Uniform on [0, 2*mean], rounded stochastically so that the expectation
	// survives even when it is a small fraction: plain rounding would turn
	// every burst below one half into no deletion at all, and a stream with
	// few transients would then delete them all at the end, in one block.
	x := 2 * mean * s.rng.Float64()
	d := int(x)
	if s.rng.Float64() < x-float64(d) {
		d++
	}
	d = min(d, n)
	for range d {
		if s.permanentPresent != nil && s.permanentPresent.len() > 0 && s.rng.Float64() < s.c.PermanentChurn {
			id := s.permanentPresent.random(&s.rng)
			s.pending = append(s.pending, id)
			s.delete(id)
			continue
		}
		s.delete(s.live.remove(&s.rng))
	}
}

// liveSet holds the transient elements present, in the order of their
// insertion as far as the policy needs it.
type liveSet struct {
	policy Policy
	ids    []uint32
	head   int // FIFO only: the oldest element still present
}

func (l *liveSet) len() int { return len(l.ids) - l.head }

func (l *liveSet) add(id uint32) { l.ids = append(l.ids, id) }

// remove takes out the element the policy selects and returns it.
func (l *liveSet) remove(rng *rtcompare.DPRNG) uint32 {
	switch l.policy {
	case FIFO:
		id := l.ids[l.head]
		l.head++
		if l.head >= 1024 && 2*l.head >= len(l.ids) {
			// Reclaim the consumed front so the slice does not grow without
			// bound over a long stream.
			l.ids = append(l.ids[:0], l.ids[l.head:]...)
			l.head = 0
		}
		return id
	case LIFO:
		last := len(l.ids) - 1
		id := l.ids[last]
		l.ids = l.ids[:last]
		return id
	default:
		i := rng.Uint32N(uint32(len(l.ids)))
		id := l.ids[i]
		last := len(l.ids) - 1
		l.ids[i] = l.ids[last]
		l.ids = l.ids[:last]
		return id
	}
}

// mixSeed spreads a seed over all 64 bits with one round of splitmix64, for
// deriving the key permutation's seed from the stream's.
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

// indexSet is a set of IDs that can be added to, removed from and drawn from
// uniformly, each in constant time, for picking the target of a lookup or of
// churn among the elements present.
type indexSet struct {
	ids []uint32
	pos []int32 // pos[id] is id's index in ids, or -1 when absent
}

func newIndexSet(capacity int) *indexSet {
	pos := make([]int32, capacity)
	for i := range pos {
		pos[i] = -1
	}
	return &indexSet{pos: pos}
}

func (x *indexSet) len() int { return len(x.ids) }

func (x *indexSet) add(id uint32) {
	x.pos[id] = int32(len(x.ids))
	x.ids = append(x.ids, id)
}

func (x *indexSet) remove(id uint32) {
	i, last := x.pos[id], len(x.ids)-1
	moved := x.ids[last]
	x.ids[i] = moved
	x.pos[moved] = i
	x.ids = x.ids[:last]
	x.pos[id] = -1
}

func (x *indexSet) random(rng *rtcompare.DPRNG) uint32 {
	return x.ids[rng.Uint32N(uint32(len(x.ids)))]
}

// missPool is how many IDs above missBase a lookup that misses draws from:
// as many as there are elements, so that misses are as spread over the keys
// as hits are.
func missPool(missBase uint32) uint32 {
	return max(1, missBase)
}

// rngSeed maps a Config seed to a DPRNG seed. NewDPRNG treats zero as a request
// for a random seed, whereas a Config with Seed zero, the default, must give
// the same stream every time.
func rngSeed(seed uint64) uint64 {
	if seed == 0 {
		return 0x5EED5EED5EED5EED
	}
	return seed
}
