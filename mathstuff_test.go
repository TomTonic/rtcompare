package rtcompare

import (
	"math"
	"math/rand"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMedian(t *testing.T) {
	testCases := []struct {
		data     []float64
		expected float64
	}{
		{[]float64{1, 2}, 1.5},
		{[]float64{1}, 1},
		{[]float64{1, 2, 3}, 2},
		{[]float64{1, 2, 3, 4}, 2.5},
		{[]float64{3, 1, 2}, 2},
		{[]float64{4, 1, 3, 2}, 2.5},
		{[]float64{1, 2, 2, 3, 4}, 2},
		{[]float64{1.5, 3.5, 2.5}, 2.5},
		{[]float64{1.1, 2.2, 3.3, 4.4}, (2.2 + 3.3) / 2},
	}

	if !math.IsNaN(Median(nil)) {
		t.Errorf("the median of nothing should be NaN, got %v", Median(nil))
	}
	for _, tc := range testCases {
		result := Median(tc.data)
		assert.True(t, result == tc.expected, "FAIL: data=%v, expected=%v, got=%v\n", tc.data, tc.expected, result)
	}
}

func TestQuickMedianDeterministic(t *testing.T) {
	cases := []struct {
		name   string
		input  []float64
		expect float64 // the mean of the two middle values for even counts, the middle one for odd
	}{
		{"odd sorted", []float64{1, 2, 3}, 2},
		{"odd unsorted", []float64{5, 1, 4, 2, 3}, 3},
		{"even sorted", []float64{1, 2, 3, 4}, 2.5},
		{"even unsorted", []float64{10, 1, 8, 3}, 5.5}, // sorted: [1,3,8,10] -> (3+8)/2
		{"duplicates even", []float64{2, 2, 2, 2}, 2},
		{"duplicates odd", []float64{7, 7, 7}, 7},
	}

	for _, cc := range cases {
		t.Run(cc.name, func(t *testing.T) {
			// QuickMedian mutates the slice, so it gets a copy.
			input := make([]float64, len(cc.input))
			copy(input, cc.input)
			got := quickMedian(input)
			if got != cc.expect {
				t.Fatalf("quickMedian(%v) = %v, want %v", cc.input, got, cc.expect)
			}
		})
	}
}

func TestQuickMedianRandomCompareToSortedMedian(t *testing.T) {
	const runs = 10_000
	for i := range runs {
		n := rand.Intn(5000) + 1 // length 1..50
		xs := make([]float64, n)
		for j := 0; j < n; j++ {
			// A mix of integer and fractional values, negative ones included.
			xs[j] = float64(rand.Intn(2001)-1000) + rand.Float64()
		}

		// QuickMedian mutates the slice, so both operations get copies.
		qs := make([]float64, n)
		copy(qs, xs)
		got := quickMedian(qs)

		sorted := make([]float64, n)
		copy(sorted, xs)
		slices.Sort(sorted)

		expected := sorted[n/2]
		if n%2 == 0 {
			expected = (sorted[n/2-1] + sorted[n/2]) / 2
		}

		if got != expected {
			t.Fatalf("run %d: mismatch\norig: %v\nsorted: %v\nexpected: %v\ngot: %v", i, xs, sorted, expected, got)
		}
	}
}

func TestQuickselectEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		xs      []float64
		k       uint64
		want    float64
		wantNaN bool
	}{
		// empty slice: any k is invalid -> expect NaN
		{name: "empty, k=0", xs: []float64{}, k: 0, wantNaN: true},
		{name: "empty, k=1", xs: []float64{}, k: 1, wantNaN: true},

		// one element
		{name: "one elem, k=0", xs: []float64{42}, k: 0, want: 42, wantNaN: false},
		{name: "one elem, k=1 (out of bounds)", xs: []float64{42}, k: 1, wantNaN: true},
		{name: "one elem, k=2 (out of bounds)", xs: []float64{42}, k: 2, wantNaN: true},

		// two elements
		{name: "two elems, small-first k=0", xs: []float64{1, 2}, k: 0, want: 1, wantNaN: false},
		{name: "two elems, small-first k=1", xs: []float64{1, 2}, k: 1, want: 2, wantNaN: false},
		{name: "two elems, large-first k=0", xs: []float64{2, 1}, k: 0, want: 1, wantNaN: false},
		{name: "two elems, large-first k=1", xs: []float64{2, 1}, k: 1, want: 2, wantNaN: false},
		{name: "two elems, k=2", xs: []float64{5, 5}, k: 2, wantNaN: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// call quickselect with uint64-converted k
			got := quickselect(append([]float64(nil), tc.xs...), uint64(tc.k))
			if tc.wantNaN {
				if !math.IsNaN(got) {
					t.Fatalf("%s: expected NaN, got %v", tc.name, got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestQuickMedianEmpty(t *testing.T) {
	got := quickMedian([]float64{})
	if !math.IsNaN(got) {
		t.Fatalf("expected NaN for empty input, got %v", got)
	}
}
