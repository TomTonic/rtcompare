package workload

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
)

// ids returns 0 to n-1.
func ids(n int) []uint32 {
	out := make([]uint32, n)
	for i := range out {
		out[i] = uint32(i)
	}
	return out
}

// counts returns the number of insertions and deletions and how often the
// stream switches between the two.
func counts(ops []Op) (inserts, deletes, switches int) {
	for i, op := range ops {
		if op.Kind == Insert {
			inserts++
		} else {
			deletes++
		}
		if i > 0 && op.Kind != ops[i-1].Kind {
			switches++
		}
	}
	return inserts, deletes, switches
}

// TestStreamsAreValidByConstruction checks the central promise of the
// generator: every operation of every stream does real work. It belongs to the
// workload package, whose streams drive mutation benchmarks through rtcompare.
// For each ratio, size and deletion policy, a Build has to take an empty model
// to exactly the IDs 0 to target-1 and a Cycle has to return the model to that
// state, with no insertion of a present element and no deletion of an absent
// one; the insertion counts have to match the ratio, and insertions and
// deletions have to interleave rather than come in two blocks.
func TestStreamsAreValidByConstruction(t *testing.T) {
	for _, target := range []int{1, 10, 1000, 20000} {
		for _, ratio := range []float64{1, 1.01, 1.5, 2, 3} {
			for _, policy := range []Policy{Uniform, FIFO, LIFO} {
				name := fmt.Sprintf("target %d ratio %v %s", target, ratio, policy)
				t.Run("build is valid for "+name, func(t *testing.T) {
					checkStream(t, target, ratio, policy, true)
				})
				if ratio > 1 && math.Round((ratio-1)*float64(target)) > 0 {
					t.Run("cycle is valid for "+name, func(t *testing.T) {
						checkStream(t, target, ratio, policy, false)
					})
				}
			}
		}
	}
}

func checkStream(t *testing.T, target int, ratio float64, policy Policy, build bool) {
	t.Helper()
	c := Config{Seed: 3, Ratio: ratio, Victims: policy}
	transients := int(math.Round((ratio - 1) * float64(target)))
	var ops []Op
	var err error
	var start []uint32
	wantInserts := transients
	if build {
		ops, err = Build(target, c)
		wantInserts += target
	} else {
		ops, err = Cycle(target, c)
		start = ids(target)
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := Check(ops, start, ids(target)); err != nil {
		t.Fatal(err)
	}
	inserts, deletes, switches := counts(ops)
	if inserts != wantInserts || deletes != transients {
		t.Errorf("got %d insertions and %d deletions, want %d and %d", inserts, deletes, wantInserts, transients)
	}
	// Interleaving: with enough transients the stream must change direction
	// many times, not insert everything and then delete everything.
	if transients >= 100 && switches < transients/20 {
		t.Errorf("only %d switches between insertions and deletions for %d transients", switches, transients)
	}
}

// TestStreamsAreDeterministic checks that a benchmark built on a stream can be
// repeated exactly: the same seed and configuration have to give the same
// stream, including seed zero, and a different seed a different one.
func TestStreamsAreDeterministic(t *testing.T) {
	for _, gen := range []struct {
		name string
		f    func(int, Config) ([]Op, error)
	}{{"Build", Build}, {"Cycle", Cycle}} {
		t.Run(gen.name+" repeats for the same seed", func(t *testing.T) {
			a, errA := gen.f(5000, Config{Seed: 0})
			b, errB := gen.f(5000, Config{Seed: 0})
			c, errC := gen.f(5000, Config{Seed: 1})
			if errA != nil || errB != nil || errC != nil {
				t.Fatalf("unexpected errors: %v %v %v", errA, errB, errC)
			}
			if !slices.Equal(a, b) {
				t.Error("the same seed gave two different streams")
			}
			if slices.Equal(a, c) {
				t.Error("seeds 0 and 1 gave the same stream")
			}
		})
	}
}

// TestPoliciesChooseTheirVictims checks that each deletion policy deletes what
// its name promises, since that is what makes a stream resemble a queue, a
// stack or a general-purpose set. Replaying a cycle, a FIFO deletion has to
// remove the oldest transient element present, a LIFO deletion the youngest,
// and a Uniform one neither consistently.
func TestPoliciesChooseTheirVictims(t *testing.T) {
	for _, c := range []struct {
		policy         Policy
		oldest, newest bool
		name           string
	}{
		{FIFO, true, false, "FIFO deletes the oldest"},
		{LIFO, false, true, "LIFO deletes the youngest"},
		{Uniform, false, false, "Uniform deletes neither consistently"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ops, err := Cycle(2000, Config{Seed: 11, Ratio: 2, Victims: c.policy})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var live []uint32 // transient elements present, oldest first
			oldest, newest, deletes := 0, 0, 0
			for _, op := range ops {
				if op.Kind == Insert {
					live = append(live, op.ID)
					continue
				}
				deletes++
				i := slices.Index(live, op.ID)
				if i == 0 {
					oldest++
				}
				if i == len(live)-1 {
					newest++
				}
				live = slices.Delete(live, i, i+1)
			}
			if got := oldest == deletes; got != c.oldest {
				t.Errorf("%d of %d deletions removed the oldest element", oldest, deletes)
			}
			if got := newest == deletes; got != c.newest {
				t.Errorf("%d of %d deletions removed the youngest element", newest, deletes)
			}
		})
	}
}

// TestCycleKeepsAboutLiveTargetPresent checks the steady state a cycle is
// meant to hold: the number of transient elements present settles around
// LiveTarget instead of growing with the length of the cycle. Over the middle
// of a long cycle, the median count has to lie within a factor of two of it.
func TestCycleKeepsAboutLiveTargetPresent(t *testing.T) {
	const target, live = 10000, 500
	ops, err := Cycle(target, Config{Seed: 5, Ratio: 3, LiveTarget: live})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	present, samples := 0, []int{}
	for i, op := range ops {
		if op.Kind == Insert {
			present++
		} else {
			present--
		}
		if i > len(ops)/4 && i < 3*len(ops)/4 {
			samples = append(samples, present)
		}
	}
	slices.Sort(samples)
	if median := samples[len(samples)/2]; median < live/2 || median > 2*live {
		t.Errorf("median of %d transient elements present, want about %d", median, live)
	}
}

// TestConfigIsChecked checks that the generator refuses configurations that
// would produce something other than what was asked for, and names the
// problem.
func TestConfigIsChecked(t *testing.T) {
	cases := []struct {
		name  string
		build bool
		n     int
		c     Config
		want  string
	}{
		{"returns error for an empty target", true, 0, Config{}, "target"},
		{"returns error for a ratio below one", true, 10, Config{Ratio: 0.5}, "Ratio"},
		{"returns error for a NaN ratio", true, 10, Config{Ratio: math.NaN()}, "Ratio"},
		{"returns error for a cycle at ratio one", false, 10, Config{Ratio: 1}, "Ratio"},
		{"returns error for a cycle without transients", false, 10, Config{Ratio: 1.01}, "no transient"},
		{"returns error for a negative burst", true, 10, Config{MaxBurst: -1}, "MaxBurst"},
		{"returns error for an unknown policy", true, 10, Config{Victims: Policy(7)}, "Victims"},
		{"returns error for more IDs than fit in uint32", false, 3_000_000_000, Config{Ratio: 2}, "uint32"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var err error
			if c.build {
				_, err = Build(c.n, c.c)
			} else {
				_, err = Cycle(c.n, c.c)
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want an error mentioning %q", err, c.want)
			}
		})
	}
}

// TestCheckRejectsInvalidStreams checks the verifier itself, which tests of
// anything that produces a stream rely on: it has to catch every kind of
// invalid stream, name the offending operation, and accept a valid one.
func TestCheckRejectsInvalidStreams(t *testing.T) {
	cases := []struct {
		name       string
		ops        []Op
		start, end []uint32
		want       string
	}{
		{"accepts a valid stream", []Op{{5, Insert}, {1, Delete}}, []uint32{1}, []uint32{5}, ""},
		{"rejects inserting a present element", []Op{{1, Insert}}, []uint32{1}, []uint32{1}, "op 0 inserts element 1"},
		{"rejects deleting an absent element", []Op{{2, Insert}, {3, Delete}}, nil, []uint32{2}, "op 1 deletes element 3"},
		{"rejects an unknown kind", []Op{{1, Kind(9)}}, nil, nil, "Kind(9)"},
		{"rejects a missing element at the end", nil, []uint32{1}, []uint32{1, 2}, "element 2 should be present"},
		{"rejects an extra element at the end", []Op{{4, Insert}}, nil, nil, "first being 4"},
		{"rejects a duplicate start element", nil, []uint32{1, 1}, nil, "twice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Check(c.ops, c.start, c.end)
			if c.want == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want an error mentioning %q", err, c.want)
			}
		})
	}
}

// model is a set that fails the test on any invalid operation, for replaying
// streams through a Cursor.
type model struct {
	t       *testing.T
	present map[uint32]bool
}

func newModel(t *testing.T, start []uint32) *model {
	m := &model{t: t, present: map[uint32]bool{}}
	for _, id := range start {
		m.present[id] = true
	}
	return m
}

func (m *model) apply(run []Op) {
	for _, op := range run {
		if (op.Kind == Insert) == m.present[op.ID] {
			m.t.Fatalf("invalid %s of element %d", op.Kind, op.ID)
		}
		if op.Kind == Insert {
			m.present[op.ID] = true
		} else {
			delete(m.present, op.ID)
		}
	}
}

// TestCursorWrapsAroundTheCycle checks the replay a benchmark runs: batches of
// arbitrary size, smaller and larger than the cycle, have to continue exactly
// where the last one stopped and wrap around at the end, so that every
// operation stays valid, and Settle has to bring the structure back to the
// state the cycle starts from.
func TestCursorWrapsAroundTheCycle(t *testing.T) {
	const target = 300
	ops, err := Cycle(target, Config{Seed: 9})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := newModel(t, ids(target))
	cur := &Cursor{}
	batch := cur.Batch(ops, m.apply)
	applied := 0
	for _, n := range []uint64{1, 7, uint64(len(ops)) - 3, 0, uint64(2*len(ops) + 5), 13, uint64(len(ops))} {
		batch(n)
		applied += int(n)
		if want := applied % len(ops); cur.Position() != want {
			t.Fatalf("after %d operations the cursor is at %d, want %d", applied, cur.Position(), want)
		}
	}
	cur.Settle(ops, m.apply)
	if cur.Position() != 0 || len(m.present) != target {
		t.Errorf("after Settle: position %d, %d elements present, want 0 and %d", cur.Position(), len(m.present), target)
	}
	for _, id := range ids(target) {
		if !m.present[id] {
			t.Fatalf("element %d missing after Settle", id)
		}
	}
	cur.Settle(ops, func([]Op) { t.Error("Settle at the start of the cycle should do nothing") })
}

// TestCursorIgnoresAnEmptyStream checks that a batch over an empty stream does
// nothing rather than looping forever.
func TestCursorIgnoresAnEmptyStream(t *testing.T) {
	cur := &Cursor{}
	cur.Advance(nil, 10, func([]Op) { t.Error("apply called for an empty stream") })
	cur.Settle(nil, func([]Op) { t.Error("apply called for an empty stream") })
}

// TestStrings checks the names that appear in error messages and output.
func TestStrings(t *testing.T) {
	for got, want := range map[string]string{
		Insert.String(): "insert", Delete.String(): "delete", Kind(4).String(): "Kind(4)",
		Uniform.String(): "Uniform", FIFO.String(): "FIFO", LIFO.String(): "LIFO", Policy(9).String(): "Policy(9)",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// FuzzStreamsAndReplay checks the promises of the generator and the cursor on
// arbitrary configurations and batch sizes: whatever the seed, size, ratio,
// burst, live target and policy, a Build and a Cycle have to pass Check, and
// replaying the cycle in batches of the fuzzed sizes has to stay valid and
// settle back to the start.
func FuzzStreamsAndReplay(f *testing.F) {
	f.Add(uint64(1), uint16(100), uint8(100), uint8(16), uint16(0), uint8(0), uint16(7), uint16(250))
	f.Add(uint64(0), uint16(1), uint8(200), uint8(1), uint16(1), uint8(1), uint16(1), uint16(1))
	f.Fuzz(func(t *testing.T, seed uint64, target uint16, extra, burst uint8, live uint16, policy uint8, n1, n2 uint16) {
		c := Config{
			Seed:       seed,
			Ratio:      1 + float64(extra)/100,
			MaxBurst:   int(burst),
			LiveTarget: int(live),
			Victims:    Policy(policy % 3),
		}
		size := int(target%2000) + 1
		ops, err := Build(size, c)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if err := Check(ops, nil, ids(size)); err != nil {
			t.Fatalf("Build: %v", err)
		}
		cycle, err := Cycle(size, c)
		if err != nil {
			return // too few transients for a cycle at this ratio
		}
		if err := Check(cycle, ids(size), ids(size)); err != nil {
			t.Fatalf("Cycle: %v", err)
		}
		m := newModel(t, ids(size))
		cur := &Cursor{}
		cur.Advance(cycle, uint64(n1), m.apply)
		cur.Advance(cycle, uint64(n2), m.apply)
		cur.Settle(cycle, m.apply)
		if len(m.present) != size {
			t.Fatalf("%d elements present after Settle, want %d", len(m.present), size)
		}
	})
}
