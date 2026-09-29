package rtcompare

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// perProcess builds a validated report whose estimate is delta with a
// symmetric 95% interval of the given half-width, as one process would report.
func perProcess(delta, half float64) Report {
	return Report{
		Estimate:   Estimate{Delta: delta, Low: delta - half, High: delta + half, Level: 0.95},
		Validated:  true,
		NoiseFloor: 0.005,
		Resolved:   math.Abs(delta) > half && math.Abs(delta) > 0.005,
	}
}

// TestStudentTQuantile checks the one distribution quantile the pooled
// interval depends on. Combine computes it without a statistics dependency, so
// it is compared against published two-sided 95% and 90% critical values of
// Student's t, which it has to reproduce to five decimals.
func TestStudentTQuantile(t *testing.T) {
	cases := []struct {
		p, df, want float64
	}{
		{0.975, 1, 12.706205},
		{0.975, 2, 4.302653},
		{0.975, 4, 2.776445},
		{0.975, 9, 2.262157},
		{0.975, 19, 2.093024},
		{0.975, 100, 1.983972},
		{0.95, 4, 2.131847},
		{0.025, 4, -2.776445},
		{0.5, 7, 0},
	}
	for _, c := range cases {
		if got := studentTQuantile(c.p, c.df); math.Abs(got-c.want) > 1e-5 {
			t.Errorf("t quantile p=%v df=%v: got %.6f, want %.6f", c.p, c.df, got, c.want)
		}
	}
}

// TestCombinePoolsProcessesAsObservations checks the central promise of
// pooling: the interval reflects how much the processes actually disagree, not
// how confident each of them was. It belongs to Combine, which turns several
// single-process reports of the same comparison into one estimate. Five
// processes with tight intervals that scatter widely have to produce the mean
// of their deltas, a t interval over them, an inflation factor well above one
// and a warning that says so.
func TestCombinePoolsProcessesAsObservations(t *testing.T) {
	deltas := []float64{0.10, 0.14, 0.12, 0.25, 0.20}
	var reports []Report
	for _, d := range deltas {
		reports = append(reports, perProcess(d, 0.01))
	}
	p, err := Combine(reports, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Log("\n" + p.String())

	// Worked out by hand: the mean is 0.162, the squared deviations sum to
	// 0.01528, so the sample standard deviation is sqrt(0.01528/4), and the
	// half-width is t(0.975, 4) = 2.776445 times that over sqrt(5).
	mean, sd := 0.162, math.Sqrt(0.01528/4)
	half := 2.776445 * sd / math.Sqrt(5)
	if math.Abs(p.Delta-mean) > 1e-12 || math.Abs(p.Low-(mean-half)) > 1e-5 || math.Abs(p.High-(mean+half)) > 1e-5 {
		t.Errorf("pooled %+.4f [%+.4f, %+.4f], want %+.4f [%+.4f, %+.4f]", p.Delta, p.Low, p.High, mean, mean-half, mean+half)
	}
	if math.Abs(p.SpreadBetween-sd) > 1e-12 {
		t.Errorf("spread between processes: got %v, want the sample standard deviation %v", p.SpreadBetween, sd)
	}
	wantWithin := 0.01 / 1.959964
	if math.Abs(p.SpreadWithin-wantWithin) > 1e-6 {
		t.Errorf("within-process standard error: got %v, want %v", p.SpreadWithin, wantWithin)
	}
	if p.Inflation < 5 {
		t.Errorf("inflation %v, want well above one for processes this far apart", p.Inflation)
	}
	if !(p.I2 > 0.9 && p.I2 <= 1) {
		t.Errorf("I² %v, want close to one", p.I2)
	}
	if !p.Resolved {
		t.Errorf("a pooled interval well above zero should resolve")
	}
	if got := strings.Join(p.Warnings, "\n"); !strings.Contains(got, "times as widely") {
		t.Errorf("warnings do not mention the inflation:\n%s", got)
	}
}

// TestCombineAgreeingProcessesShowNoHeterogeneity checks the reassuring case:
// processes that agree within their own intervals. Combine is expected to
// report an inflation near one, an I² of zero and no warning about scatter, so
// that a clean result reads clean.
func TestCombineAgreeingProcessesShowNoHeterogeneity(t *testing.T) {
	reports := []Report{
		perProcess(0.050, 0.02), perProcess(0.052, 0.02), perProcess(0.049, 0.02),
		perProcess(0.051, 0.02), perProcess(0.048, 0.02),
	}
	p, err := Combine(reports, 0.95)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.I2 != 0 {
		t.Errorf("I² %v, want 0 for processes that agree within their intervals", p.I2)
	}
	if p.Inflation > 1 {
		t.Errorf("inflation %v, want below one", p.Inflation)
	}
	if got := strings.Join(p.Warnings, "\n"); strings.Contains(got, "times as widely") {
		t.Errorf("unexpected scatter warning:\n%s", got)
	}
}

// TestCombineWarnings checks the plain-language fine print of a pooled result.
// Each case sets up one condition that undermines it and expects a sentence
// that names it.
func TestCombineWarnings(t *testing.T) {
	opposite := []Report{perProcess(0.10, 0.01), perProcess(-0.10, 0.01), perProcess(0.02, 0.01)}
	unvalidated := []Report{perProcess(0.1, 0.01), perProcess(0.1, 0.01), {Estimate: Estimate{Delta: 0.1, Low: 0.09, High: 0.11, Level: 0.95}}}
	suspended := []Report{perProcess(0.1, 0.01), perProcess(0.1, 0.01), perProcess(0.1, 0.01)}
	suspended[1].Suspended = 20 * time.Minute
	cases := []struct {
		name    string
		reports []Report
		want    string
	}{
		{"warns about too few processes", opposite, "only 3 processes"},
		{"warns about processes resolving in opposite directions", opposite, "2 processes resolved A as faster and 1 as slower"},
		{"warns about an interval that includes zero", opposite, "includes zero"},
		{"warns about processes without validation", unvalidated, "not every process"},
		{"warns about a suspended process", suspended, "process 1 was suspended for 20m0s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := Combine(c.reports, 0)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := strings.Join(p.Warnings, "\n"); !strings.Contains(got, c.want) {
				t.Errorf("warnings do not mention %q:\n%s", c.want, got)
			}
		})
	}
}

// TestCombineRejectsBadInput checks that pooling fails loudly on input it
// cannot honestly pool: too few processes for a t interval to mean anything, a
// level outside (0,1), and an estimate that is not a number.
func TestCombineRejectsBadInput(t *testing.T) {
	ok := perProcess(0.1, 0.01)
	nan := perProcess(0.1, 0.01)
	nan.Estimate.Delta = math.NaN()
	cases := []struct {
		name    string
		reports []Report
		level   float64
		want    string
	}{
		{"returns error for two processes", []Report{ok, ok}, 0, "at least 3"},
		{"returns error for a level of one", []Report{ok, ok, ok}, 1, "level"},
		{"returns error for a NaN level", []Report{ok, ok, ok}, math.NaN(), "level"},
		{"returns error for a non-finite estimate", []Report{ok, nan, ok}, 0, "report 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Combine(c.reports, c.level)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want an error mentioning %q", err, c.want)
			}
		})
	}
}

// TestHeterogeneityWithoutStandardErrors checks that Q and I² are reported as
// unknown, not as zero, when a process gave an interval of zero width. Its
// weight in the heterogeneity statistic would be infinite, so any number would
// be made up.
func TestHeterogeneityWithoutStandardErrors(t *testing.T) {
	q, i2 := heterogeneity([]float64{0.1, 0.2, 0.3}, []float64{0.01, 0, 0.01})
	if !math.IsNaN(q) || !math.IsNaN(i2) {
		t.Errorf("got Q %v, I² %v, want NaN for both", q, i2)
	}
}

// TestPooledPrecise checks the stop rule the multi-process driver uses: a
// pooled interval is precise enough when its half-width is within an absolute
// bound or within a share of the difference itself, whichever is looser.
func TestPooledPrecise(t *testing.T) {
	cases := []struct {
		name string
		p    Pooled
		want bool
	}{
		{"is precise within two points", Pooled{Delta: 0.01, Low: -0.005, High: 0.025}, true},
		{"is precise within ten percent of a large difference", Pooled{Delta: 0.5, Low: 0.46, High: 0.54}, true},
		{"is not precise when wide and small", Pooled{Delta: 0.05, Low: 0.0, High: 0.1}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.p.Precise(0.02, 0.10); got != c.want {
				t.Errorf("Precise: got %v, want %v", got, c.want)
			}
		})
	}
}

// TestCombineIntervalCoversAtItsLevel checks that a pooled result is as
// trustworthy as it says: across many repetitions, a 95% pooled interval has
// to contain the true difference about 95% of the time. It belongs to Combine,
// whose t interval is what multiproc reports and stops on. Processes are
// simulated with normally scattered deltas and negligible intervals of their
// own, from a fixed seed so that the test is deterministic, and the coverage
// has to lie within about three standard errors of 95% for as few as three
// processes, where a population standard deviation gave 93%.
func TestCombineIntervalCoversAtItsLevel(t *testing.T) {
	rng := rand.New(rand.NewPCG(118, 1))
	const truth, spread, trials = 0.05, 0.02, 4000
	for _, k := range []int{3, 5, 10} {
		t.Run(fmt.Sprintf("covers 95%% with %d processes", k), func(t *testing.T) {
			hits := 0
			reports := make([]Report, k)
			for range trials {
				for i := range reports {
					reports[i] = perProcess(truth+spread*rng.NormFloat64(), 0.001)
				}
				p, err := Combine(reports, 0.95)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if p.Low <= truth && truth <= p.High {
					hits++
				}
			}
			// The standard error of a proportion near 0.95 over 4000 trials is
			// 0.34 points.
			if coverage := float64(hits) / trials; coverage < 0.94 || coverage > 0.96 {
				t.Errorf("coverage %.1f%%, want 95%% within one point", coverage*100)
			}
		})
	}
}

// TestProcessesForFollowsSteinsRule checks how a multi-process run is sized
// from its first stage: the number of processes has to be what Stein's
// two-stage rule gives for the precision asked for, never fewer than already
// ran, and unbounded when no precision can be met.
func TestProcessesForFollowsSteinsRule(t *testing.T) {
	// Six processes scattering with a sample standard deviation of 0.03
	// around 0.05: t(0.975, 5) = 2.570582, and (2.570582*0.03/0.02)² = 14.87.
	stage := Pooled{Processes: 6, Delta: 0.05, SpreadBetween: 0.03, Level: 0.95}
	cases := []struct {
		name     string
		abs, rel float64
		want     int
	}{
		{"asks for the processes an absolute precision needs", 0.02, 0.10, 15},
		// h = 0.5*0.05 = 0.025, and (2.570582*0.03/0.025)² = 9.52.
		{"takes the relative precision where it is looser", 0.005, 0.50, 10},
		{"never asks for fewer processes than already ran", 1, 0.10, 6},
		{"is unbounded when no precision can be met", 0, 0, math.MaxInt},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stage.ProcessesFor(c.abs, c.rel); got != c.want {
				t.Errorf("got %d processes, want %d", got, c.want)
			}
		})
	}
}

// TestCombineStagedCoversAtItsLevel checks that a pooled result from a run
// sized by its own first stage is as trustworthy as it says. It belongs to the
// two-stage procedure behind multiproc: size the run with ProcessesFor from
// the first six processes, run that many, pool with CombineStaged. Simulated
// from a fixed seed at the multiproc defaults, a 95% interval has to contain
// the truth 95% of the time within about three standard errors, where
// checking the interval after every process covered 92 to 94%.
func TestCombineStagedCoversAtItsLevel(t *testing.T) {
	rng := rand.New(rand.NewPCG(119, 1))
	const firstStage, most, trials = 6, 40, 4000
	cases := []struct {
		name          string
		truth, spread float64
	}{
		{"covers 95% for no difference", 0, 0.03},
		{"covers 95% for a difference as large as the scatter", 0.05, 0.05},
		{"covers 95% for a large difference", 0.3, 0.05},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hits := 0
			for range trials {
				var reports []Report
				for range firstStage {
					reports = append(reports, perProcess(c.truth+c.spread*rng.NormFloat64(), 0.001))
				}
				stage, err := Combine(reports, 0.95)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				need := min(most, stage.ProcessesFor(0.02, 0.10))
				for len(reports) < need {
					reports = append(reports, perProcess(c.truth+c.spread*rng.NormFloat64(), 0.001))
				}
				p, err := CombineStaged(reports, firstStage, 0.95)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if p.Low <= c.truth && c.truth <= p.High {
					hits++
				}
			}
			if coverage := float64(hits) / trials; coverage < 0.94 || coverage > 0.96 {
				t.Errorf("coverage %.1f%%, want 95%% within one point", coverage*100)
			}
		})
	}
}

// TestCombineStagedRejectsABadFirstStage checks that a first stage that
// cannot have sized a run is refused rather than silently pooled.
func TestCombineStagedRejectsABadFirstStage(t *testing.T) {
	reports := []Report{perProcess(0.1, 0.01), perProcess(0.1, 0.01), perProcess(0.1, 0.01), perProcess(0.1, 0.01)}
	for _, stage := range []int{2, 5} {
		if _, err := CombineStaged(reports, stage, 0); err == nil || !strings.Contains(err.Error(), "firstStage") {
			t.Errorf("first stage %d: got %v, want an error", stage, err)
		}
	}
	whole, err := CombineStaged(reports, 4, 0)
	plain, _ := Combine(reports, 0)
	if err != nil || whole.Low != plain.Low || whole.High != plain.High || whole.FirstStage != 4 {
		t.Errorf("a first stage of every report should give Combine's interval: %+v, %v", whole, err)
	}
}
