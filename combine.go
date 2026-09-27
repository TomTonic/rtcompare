package rtcompare

// This file combines the same comparison run in several processes, which is
// the only way to put an interval on a difference that includes the scatter
// between processes, and not just the noise within one.

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// Pooled is one comparison combined across several processes by [Combine].
//
// It treats each process as a single observation of the difference. Its
// interval therefore covers what a single [Report]'s interval cannot: that each
// process lays its data out in memory differently, and that the layout can move
// the difference by more than the noise within any one of them.
type Pooled struct {
	// Processes is the number of per-process results that were combined.
	Processes int

	// Delta is the mean of the per-process deltas, each 1 - median(A)/median(B).
	// Positive means A is faster. The mean is unweighted on purpose: the
	// per-process intervals understate each process's real uncertainty, which is
	// the problem this type exists to solve, so weighting by them would give the
	// processes that happened to be least noisy inside the most say.
	Delta float64

	// Low and High bound Delta at Level: a Student t interval over the
	// per-process deltas with Processes-1 degrees of freedom. With few processes
	// it is wide, and honestly so.
	Low, High float64

	// Level is the coverage level of the interval.
	Level float64

	// SpreadBetween is the standard deviation of the per-process deltas.
	SpreadBetween float64

	// SpreadWithin is the median standard error that a single process's own
	// interval implies, (High-Low)/(2z) at that interval's level.
	SpreadWithin float64

	// Inflation is SpreadBetween/SpreadWithin: how many times more the
	// processes scatter than one process's interval says they should. Near one,
	// a single process could be trusted on its own; measured on large,
	// pointer-heavy data it has been 5 and more. Zero when SpreadWithin is zero.
	Inflation float64

	// Q is Cochran's heterogeneity statistic, the sum of the squared deviations
	// of the per-process deltas from their inverse-variance mean, in units of
	// each process's own standard error. Without variance between processes it
	// is about Processes-1. NaN when a process reported an interval of zero
	// width, since its standard error is then unknown.
	Q float64

	// I2 is the share of the scatter between processes that their own intervals
	// do not explain, max(0, (Q-(k-1))/Q), in [0,1]. Above about 0.5 the
	// variance between processes dominates. NaN when Q is.
	I2 float64

	// Validated reports whether every combined process ran its A/A validation.
	Validated bool

	// NoiseFloor is the median of the validated processes' noise floors, zero
	// when none was validated. Resolved requires Delta to clear it, as for a
	// single Report.
	NoiseFloor float64

	// Resolved is the short answer: the pooled interval excludes zero and Delta
	// clears the noise floor.
	Resolved bool

	// Warnings lists everything that undermines the pooled result, in plain
	// sentences.
	Warnings []string
}

// Precise reports whether the interval is narrow enough for the question: its
// half-width is at most abs, or at most rel times |Delta|. The multiproc
// package uses it as its rule for when to stop starting processes, with
// defaults of 0.02 and 0.10.
func (p Pooled) Precise(abs, rel float64) bool {
	half := (p.High - p.Low) / 2
	return half <= abs || half <= rel*math.Abs(p.Delta)
}

// String renders the pooled result as a short multi-line summary.
func (p Pooled) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "difference %+.2f%% [%+.2f%%, %+.2f%%] at %.0f%% confidence, pooled over %d processes\n",
		p.Delta*100, p.Low*100, p.High*100, p.Level*100, p.Processes)
	fmt.Fprintf(&b, "scatter between processes %.3f%%, within one %.3f%% (%.1fx), I² %.2f\n",
		p.SpreadBetween*100, p.SpreadWithin*100, p.Inflation, p.I2)
	switch {
	case p.Resolved && p.Delta > 0:
		b.WriteString("resolved: A is faster than B\n")
	case p.Resolved:
		b.WriteString("resolved: A is slower than B\n")
	default:
		b.WriteString("not resolved: these processes did not establish a difference\n")
	}
	for _, w := range p.Warnings {
		fmt.Fprintf(&b, "  warning: %s\n", w)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Combine pools the reports of the same comparison, run once in each of
// several processes, into one estimate whose interval includes the scatter
// between processes.
//
// Parameters: reports holds one [Report] per process, all of the same
// comparison, with the same candidates in the same roles; at least three are
// required, and five or more are advisable. level is the coverage level of the
// pooled interval, zero selecting [DefaultConfidenceLevel].
//
// It returns the [Pooled] result, or an error if there are fewer than three
// reports, if level is not strictly between zero and one, or if a report holds
// a non-finite estimate.
//
// Use it whenever the data of a comparison does not fit in the caches or is
// full of pointers. A single process reports an interval that covers only the
// noise within that process. The process's memory layout is fixed for its
// whole lifetime, and it can move the difference by several points, so the
// next process reports a different, equally narrow interval somewhere else.
// Observed on 1M-key trees, the processes scattered 4 to 10 times more widely
// than their own intervals implied, and two processes resolved the same
// comparison with opposite signs. Combine treats each process as one
// observation: Delta is the mean of the per-process deltas and the interval is
// a t interval over them, which is honest for few processes where random-effects
// weighting is known to be too narrow. Q, I2 and Inflation say how much the
// processes disagreed beyond their own intervals.
//
// Pooling only helps if the processes sample different layouts. A process that
// allocates the same things in the same order gets much the same layout every
// time, and its bias repeats rather than averages out; see [PerturbHeap]. The
// multiproc package starts the processes, perturbs each one's heap and calls
// Combine for you.
//
//	var reports []rtcompare.Report // one per process, e.g. read back from files
//	pooled, err := rtcompare.Combine(reports, 0)
//	if err != nil { ... }
//	fmt.Println(pooled)
func Combine(reports []Report, level float64) (Pooled, error) {
	k := len(reports)
	if k < 3 {
		return Pooled{}, fmt.Errorf("rtcompare: need at least 3 per-process reports to combine, got %d", k)
	}
	if level == 0 {
		level = DefaultConfidenceLevel
	}
	if math.IsNaN(level) || level <= 0 || level >= 1 {
		return Pooled{}, fmt.Errorf("rtcompare: level must be strictly between 0 and 1, got %v", level)
	}
	deltas := make([]float64, k)
	ses := make([]float64, k)
	for i, r := range reports {
		e := r.Estimate
		if !finite(e.Delta) || !finite(e.Low) || !finite(e.High) {
			return Pooled{}, fmt.Errorf("rtcompare: report %d holds a non-finite estimate %s", i, e)
		}
		deltas[i] = e.Delta
		ses[i] = standardErrorOf(e)
	}

	mean, _, sd := Statistics(deltas)
	half := studentTQuantile((1+level)/2, float64(k-1)) * sd / math.Sqrt(float64(k))
	p := Pooled{
		Processes:     k,
		Delta:         mean,
		Low:           mean - half,
		High:          mean + half,
		Level:         level,
		SpreadBetween: sd,
		SpreadWithin:  Median(slices.Clone(ses)),
	}
	if p.SpreadWithin > 0 {
		p.Inflation = p.SpreadBetween / p.SpreadWithin
	}
	p.Q, p.I2 = heterogeneity(deltas, ses)
	p.Validated, p.NoiseFloor = pooledFloor(reports)
	p.Resolved = (p.Low > 0 || p.High < 0) && math.Abs(p.Delta) > p.NoiseFloor
	p.Warnings = p.warnings(reports)
	return p, nil
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

// standardErrorOf recovers the standard error a single process's interval
// implies, treating it as a normal interval at its own level. A report that
// carries no level is read at the default.
func standardErrorOf(e Estimate) float64 {
	level := e.Level
	if !(level > 0 && level < 1) {
		level = DefaultConfidenceLevel
	}
	z := math.Sqrt2 * math.Erfinv(level)
	return (e.High - e.Low) / (2 * z)
}

// heterogeneity computes Cochran's Q and I² from the per-process deltas and
// their standard errors. Both are NaN when a standard error is zero, because
// the weight of that process is then undefined.
func heterogeneity(deltas, ses []float64) (q, i2 float64) {
	var sw, swd float64
	for i, se := range ses {
		if !(se > 0) {
			return math.NaN(), math.NaN()
		}
		w := 1 / (se * se)
		sw += w
		swd += w * deltas[i]
	}
	fixed := swd / sw
	for i, se := range ses {
		d := deltas[i] - fixed
		q += d * d / (se * se)
	}
	if q > 0 {
		i2 = math.Max(0, (q-float64(len(deltas)-1))/q)
	}
	return q, i2
}

// pooledFloor returns whether every report was validated and the median noise
// floor of those that were.
func pooledFloor(reports []Report) (all bool, floor float64) {
	var floors []float64
	for _, r := range reports {
		if r.Validated {
			floors = append(floors, r.NoiseFloor)
		}
	}
	return len(floors) == len(reports), medianOrZero(floors)
}

// warnings lists the things that undermine a pooled result.
func (p Pooled) warnings(reports []Report) []string {
	var w []string
	if p.Processes < 5 {
		w = append(w, fmt.Sprintf(
			"only %d processes were combined; below five the interval is wide and the heterogeneity figures are rough", p.Processes))
	}
	if !p.Validated {
		w = append(w, "not every process ran its A/A validation, so the noise floor is taken from those that did, or is unknown")
	} else if math.Abs(p.Delta) <= p.NoiseFloor {
		w = append(w, fmt.Sprintf(
			"the pooled difference of %.2f%% does not clear the %.2f%% median noise floor of the processes",
			p.Delta*100, p.NoiseFloor*100))
	}
	if !(p.Low > 0 || p.High < 0) {
		w = append(w, fmt.Sprintf(
			"the pooled interval [%.2f%%, %.2f%%] includes zero", p.Low*100, p.High*100))
	}
	if p.Inflation > 2 {
		w = append(w, fmt.Sprintf(
			"the processes scatter %.1f times as widely as one process's interval implies, so a single process's result for this comparison is not to be trusted on its own",
			p.Inflation))
	}
	faster, slower := 0, 0
	for _, r := range reports {
		switch {
		case r.Resolved && r.Estimate.Delta > 0:
			faster++
		case r.Resolved && r.Estimate.Delta < 0:
			slower++
		}
	}
	if faster > 0 && slower > 0 {
		w = append(w, fmt.Sprintf(
			"%d processes resolved A as faster and %d as slower; each was confident, and the disagreement between them is the layout of memory, not the code",
			faster, slower))
	}
	for i, r := range reports {
		if r.Suspended > 0 {
			w = append(w, fmt.Sprintf("process %d was suspended for %s during its comparison", i, r.Suspended.Round(time.Second)))
		}
	}
	return w
}

// studentTQuantile returns the p-quantile of Student's t distribution with df
// degrees of freedom, by bisection on its distribution function. It exists so
// that Combine needs no dependency for the one quantile it uses.
func studentTQuantile(p, df float64) float64 {
	if p == 0.5 {
		return 0
	}
	if p < 0.5 {
		return -studentTQuantile(1-p, df)
	}
	lo, hi := 0.0, 1.0
	for studentTCDF(hi, df) < p {
		lo, hi = hi, 2*hi
	}
	for range 200 {
		mid := (lo + hi) / 2
		if studentTCDF(mid, df) < p {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

// studentTCDF is the distribution function of Student's t for t >= 0, written
// through the regularized incomplete beta function.
func studentTCDF(t, df float64) float64 {
	return 1 - 0.5*regularizedIncompleteBeta(df/(df+t*t), df/2, 0.5)
}

// regularizedIncompleteBeta is I_x(a, b), evaluated by its continued fraction
// on whichever side of the mean converges fastest.
func regularizedIncompleteBeta(x, a, b float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	la, _ := math.Lgamma(a)
	lb, _ := math.Lgamma(b)
	lab, _ := math.Lgamma(a + b)
	front := math.Exp(a*math.Log(x) + b*math.Log1p(-x) + lab - la - lb)
	if x < (a+1)/(a+b+2) {
		return front * betaContinuedFraction(x, a, b) / a
	}
	return 1 - front*betaContinuedFraction(1-x, b, a)/b
}

// betaContinuedFraction evaluates the continued fraction of the incomplete
// beta function by the modified Lentz method.
func betaContinuedFraction(x, a, b float64) float64 {
	const (
		maxIter = 300
		eps     = 1e-15
		tiny    = 1e-300
	)
	guard := func(v float64) float64 {
		if math.Abs(v) < tiny {
			return tiny
		}
		return v
	}
	c, d := 1.0, 1/guard(1-(a+b)*x/(a+1))
	h := d
	for m := 1.0; m <= maxIter; m++ {
		even := m * (b - m) * x / ((a + 2*m - 1) * (a + 2*m))
		d = 1 / guard(1+even*d)
		c = guard(1 + even/c)
		h *= d * c
		odd := -(a + m) * (a + b + m) * x / ((a + 2*m) * (a + 2*m + 1))
		d = 1 / guard(1+odd*d)
		c = guard(1 + odd/c)
		step := d * c
		h *= step
		if math.Abs(step-1) < eps {
			break
		}
	}
	return h
}
