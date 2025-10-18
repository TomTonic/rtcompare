package rtcompare

import (
	"math"
	"math/rand"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMedian(t *testing.T) {
	testCases := []struct {
		data     []float64
		expected float64
	}{
		{[]float64{}, 0},
		{[]float64{1}, 1},
		{[]float64{1, 2, 3}, 2},
		{[]float64{1, 2, 3, 4}, 2.5},
		{[]float64{3, 1, 2}, 2},
		{[]float64{4, 1, 3, 2}, 2.5},
		{[]float64{1, 2, 2, 3, 4}, 2},
		{[]float64{1.5, 3.5, 2.5}, 2.5},
		{[]float64{1.1, 2.2, 3.3, 4.4}, 2.75},
	}

	for _, tc := range testCases {
		result := Median(tc.data)
		assert.True(t, result == tc.expected, "FAIL: data=%v, expected=%v, got=%v\n", tc.data, tc.expected, result)
	}
}

func TestStatistics(t *testing.T) {
	testCases := []struct {
		data     []float64
		expected struct {
			mean     float64
			variance float64
			stddev   float64
		}
	}{
		{[]float64{}, struct{ mean, variance, stddev float64 }{0, -1, -1}},
		{[]float64{1}, struct{ mean, variance, stddev float64 }{1, 0, 0}},
		{[]float64{1, 2, 3}, struct{ mean, variance, stddev float64 }{2, 2 / 3.0, math.Sqrt(2 / 3.0)}},
		{[]float64{1, 2, 3, 4}, struct{ mean, variance, stddev float64 }{2.5, 1.25, math.Sqrt(1.25)}},
		{[]float64{1, 1, 1, 1}, struct{ mean, variance, stddev float64 }{1, 0, 0}},
		{[]float64{1.5, 2.5, 3.5}, struct{ mean, variance, stddev float64 }{2.5, 2 / 3.0, math.Sqrt(2 / 3.0)}},
		{[]float64{3, 53, 512, 11, 75, 201, 335}, struct{ mean, variance, stddev float64 }{170, 31576.285714285714, math.Sqrt(31576.285714285714)}},
	}

	for _, tc := range testCases {
		mean, variance, stddev := Statistics(tc.data)
		assert.True(t, mean == tc.expected.mean && variance == tc.expected.variance && stddev == tc.expected.stddev,
			"FAIL: data=%v, expected=(%v, %v, %v), got=(%v, %v, %v)\n", tc.data, tc.expected.mean, tc.expected.variance, tc.expected.stddev, mean, variance, stddev)
	}
}

func TestFloatsEqualWithTolerance(t *testing.T) {
	testCases := []struct {
		f1, f2, tolerance float64
		expected          bool
	}{
		{1.0, 1.0, 10, true},   // Exact match
		{1.0, 1.05, 10, true},  // Within tolerance
		{1.0, 1.15, 10, false}, // Outside tolerance
		{1.0, 0.95, 10, true},  // Within tolerance
		{1.0, 0.85, 10, false}, // Outside tolerance
		{1.0, 1.1, 10, true},   // On the edge of tolerance
		{1.0, 0.9, 10, true},   // On the edge of tolerance
		{2.0, 2.15, 10, true},  // Inside tolerance
		{2.0, 1.85, 10, true},  // Inside tolerance
		{2.0, 2.21, 10, true},  // Inside tolerance of second parameter
	}

	for _, tc := range testCases {
		result := FloatsEqualWithTolerance(tc.f1, tc.f2, tc.tolerance)
		assert.True(t, result == tc.expected, "%f == %f with a tolerance of %f should be %v", tc.f1, tc.f2, tc.tolerance, tc.expected)
	}
}

func TestQuickMedianDeterministic(t *testing.T) {
	cases := []struct {
		name   string
		input  []float64
		expect float64 // expected lower-middle for even counts, exact middle for odd
	}{
		{"odd sorted", []float64{1, 2, 3}, 2},
		{"odd unsorted", []float64{5, 1, 4, 2, 3}, 3},
		{"even sorted", []float64{1, 2, 3, 4}, 3},    // higher middle
		{"even unsorted", []float64{10, 1, 8, 3}, 8}, // sorted: [1,3,8,10] -> higher middle = 8
		{"duplicates even", []float64{2, 2, 2, 2}, 2},
		{"duplicates odd", []float64{7, 7, 7}, 7},
	}

	for _, cc := range cases {
		t.Run(cc.name, func(t *testing.T) {
			// QuickMedian mutiert das Slice, also erst eine Kopie übergeben, falls Input mehrfach gebraucht wird.
			input := make([]float64, len(cc.input))
			copy(input, cc.input)
			got := QuickMedian(input)
			if got != cc.expect {
				t.Fatalf("QuickMedian(%v) = %v, want %v", cc.input, got, cc.expect)
			}
		})
	}
}

func TestQuickMedianRandomCompareToSortedLowerMedian(t *testing.T) {
	const runs = 10_000
	for i := range runs {
		n := rand.Intn(5000) + 1 // length 1..50
		xs := make([]float64, n)
		for j := 0; j < n; j++ {
			// Erzeuge eine Mischung aus Ganz- und Gleitkommawerten (inkl. negativer Werte)
			xs[j] = float64(rand.Intn(2001)-1000) + rand.Float64()
		}

		// QuickMedian verändert das Slice, also Kopien für beide Operationen verwenden
		qs := make([]float64, n)
		copy(qs, xs)
		got := QuickMedian(qs)

		sorted := make([]float64, n)
		copy(sorted, xs)
		sort.Float64s(sorted)

		var expected float64
		// if n%2 == 0 {
		//	expected = sorted[n/2-1] // lower middle for even count
		// } else {
		expected = sorted[n/2]
		// }

		if got != expected {
			t.Fatalf("run %d: mismatch\norig: %v\nsorted: %v\nexpected(lower-mid): %v\ngot: %v", i, xs, sorted, expected, got)
		}
	}
}
