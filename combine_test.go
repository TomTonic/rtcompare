package rtcompare

import (
	"math"
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

	mean, _, sd := Statistics(deltas)
	half := 2.776445 * sd / math.Sqrt(5)
	if math.Abs(p.Delta-mean) > 1e-12 || math.Abs(p.Low-(mean-half)) > 1e-5 || math.Abs(p.High-(mean+half)) > 1e-5 {
		t.Errorf("pooled %+.4f [%+.4f, %+.4f], want %+.4f [%+.4f, %+.4f]", p.Delta, p.Low, p.High, mean, mean-half, mean+half)
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
