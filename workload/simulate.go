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
}

func newSimulation(c Config, target, transients int) *simulation {
	return &simulation{
		c:              c,
		rng:            rtcompare.NewDPRNG(mixSeed(c.Seed)),
		nextTransient:  uint32(target),
		transientsLeft: transients,
		live:           liveSet{policy: c.Victims},
	}
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
	for s.live.len() > 0 {
		for range min(1+int(s.rng.Uint32N(uint32(s.c.MaxBurst))), s.live.len()) {
			s.ops = append(s.ops, s.op(s.live.remove(&s.rng), Delete))
		}
	}
}

// op makes the operation on the element with the given ID, with its key.
func (s *simulation) op(id uint32, kind Kind) Op {
	return Op{Key: s.c.Key(id), ID: id, Kind: kind}
}

// insertBurst inserts between 1 and MaxBurst elements. Each one is transient
// or permanent in proportion to how many of each are left, so that both kinds
// are spread evenly over the stream.
func (s *simulation) insertBurst() {
	for range 1 + s.rng.Uint32N(uint32(s.c.MaxBurst)) {
		left := len(s.permanent) + s.transientsLeft
		if left == 0 {
			return
		}
		if int(s.rng.Uint32N(uint32(left))) < s.transientsLeft {
			id := s.nextTransient
			s.nextTransient++
			s.transientsLeft--
			s.live.add(id)
			s.ops = append(s.ops, s.op(id, Insert))
			continue
		}
		last := len(s.permanent) - 1
		s.ops = append(s.ops, s.op(s.permanent[last], Insert))
		s.permanent = s.permanent[:last]
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
		s.ops = append(s.ops, s.op(s.live.remove(&s.rng), Delete))
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

// mixSeed spreads a seed over all 64 bits with one round of splitmix64, so
// that small seeds do not start the generator in a low-entropy state and so
// that zero stays a fixed seed, which rtcompare.NewDPRNG would otherwise take
// as a request for a random one.
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
