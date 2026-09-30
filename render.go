package rtcompare

import (
	"fmt"
	"strings"
)

// The functions in this file phrase the numbers of a Report or a Pooled result
// as sentences, so that the printed output can be read without knowing how the
// library works. Both String methods use them, which keeps the two outputs
// worded alike.

// percent formats a fraction as a percentage with three significant digits.
func percent(fraction float64) string {
	p := fraction * 100
	if p >= 1000 || p <= -1000 {
		return fmt.Sprintf("%.0f%%", p)
	}
	return fmt.Sprintf("%.3g%%", p)
}

// lessTime says how much less time A needs than B for a Delta, which is
// positive when A is faster: "43.5% less time" or "3.2% more time".
func lessTime(delta float64) string {
	if delta >= 0 {
		return percent(delta) + " less time"
	}
	return percent(-delta) + " more time"
}

// differenceLines describes a difference and its interval in two sentences.
// delta is 1 - A/B, positive when A is faster; low and high bound it at
// level.
func differenceLines(delta, low, high, level float64) string {
	var b strings.Builder
	ratio, rlow, rhigh := Estimate{Delta: delta, Low: low, High: high}.Ratio()
	switch {
	case delta > 0:
		fmt.Fprintf(&b, "A needs %s than B: B takes %.2f times as long as A.\n", lessTime(delta), ratio)
	case delta < 0:
		fmt.Fprintf(&b, "A needs %s than B: B takes only %.2f times as long as A.\n", lessTime(delta), ratio)
	default:
		b.WriteString("A and B need the same time.\n")
	}
	fmt.Fprintf(&b, "With %.0f%% confidence the difference lies ", level*100)
	switch {
	case low >= 0:
		fmt.Fprintf(&b, "between %s and %s less time", percent(low), percent(high))
	case high <= 0:
		fmt.Fprintf(&b, "between %s and %s more time", percent(-high), percent(-low))
	default:
		fmt.Fprintf(&b, "between %s and %s", lessTime(low), lessTime(high))
	}
	fmt.Fprintf(&b, " (B takes between %.3g and %.2f times as long as A).", rlow, rhigh)
	return b.String()
}

// verdictLine states the verdict of a comparison in one sentence.
func verdictLine(resolved bool, delta float64, validated bool, scope string) string {
	if !resolved {
		return "Verdict: NOT RESOLVED. " + scope + " did not establish which candidate is faster. " +
			"That is not the same as \"equally fast\": the warnings below say what is missing."
	}
	who := "A is faster than B"
	if delta < 0 {
		who = "A is slower than B"
	}
	if !validated {
		return "Verdict: RESOLVED. " + who + ": the interval excludes zero. No noise floor was measured, so the difference was not checked against it."
	}
	return "Verdict: RESOLVED. " + who + ": the difference is real, as its interval excludes zero and it exceeds the noise floor."
}

// warningLines renders warnings as a list, or nothing when there are none.
func warningLines(warnings []string) string {
	if len(warnings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nWarnings:\n")
	for _, w := range warnings {
		fmt.Fprintf(&b, "  - %s\n", w)
	}
	return b.String()
}

// drift words a relative shift between the halves of a run, to be followed by
// "than in the first half": "7.12% lower" for a negative shift, "7.12% higher"
// for a positive one.
func drift(shift float64) string {
	if shift < 0 {
		return percent(-shift) + " lower"
	}
	return percent(shift) + " higher"
}

// thresholdPhrase words a relative-gain threshold for the confidence lines:
// "at least 5% less time than B", or, for a negative one, "at most 5% more
// time than B".
func thresholdPhrase(t float64) string {
	if t == 0 {
		return "no more time than B"
	}
	if t > 0 {
		return "at least " + lessTime(t) + " than B"
	}
	return "at most " + lessTime(t) + " than B"
}
