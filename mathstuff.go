package rtcompare

import (
	"math"
	"math/rand"
	"sort"
)

func Median(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	dataCopy := make([]float64, len(data))
	copy(dataCopy, data)
	sort.Float64s(dataCopy)

	l := len(dataCopy)
	if l%2 == 0 {
		return (dataCopy[l/2-1] + dataCopy[l/2]) / 2
	}
	return dataCopy[l/2]
}

func Statistics(data []float64) (mean, variance, stddev float64) {
	if len(data) == 0 {
		return 0, -1, -1
	}

	var sum float64
	n := float64(len(data))

	for _, value := range data {
		sum += value
	}
	mean = sum / n

	for _, value := range data {
		variance += (value - mean) * (value - mean)
	}
	variance /= n
	stddev = math.Sqrt(variance)
	return
}

func FloatsEqualWithTolerance(f1, f2, tolerancePercentage float64) bool {
	absTol1 := math.Abs(f1 * tolerancePercentage / 100)
	if f1-absTol1 <= f2 && f1+absTol1 >= f2 {
		return true
	}
	absTol2 := math.Abs(f2 * tolerancePercentage / 100)
	if f2-absTol2 <= f1 && f2+absTol2 >= f1 {
		return true
	}
	return false
}

// Partition rearranges xs around a pivot and returns its final index
func partition(xs []float64, low, high uint64) uint64 {
	pivot := xs[high]
	i := low
	for j := low; j < high; j++ {
		if xs[j] < pivot {
			xs[i], xs[j] = xs[j], xs[i]
			i++
		}
	}
	xs[i], xs[high] = xs[high], xs[i]
	return i
}

// Quickselect finds the k-th smallest element (0-based index) in expected O(n) time.
// For k = len(xs)/2, it returns the median.
// see https://en.wikipedia.org/wiki/Quickselect
func quickselect(xs []float64, k uint64) float64 {
	rng := DPRNG{State: rand.Uint64()}
	for rng.State == 0 {
		rng.State = rand.Uint64()
	}

	low, high := uint64(0), uint64(len(xs)-1)
	for low <= high {
		pivotIndex := rng.Uint64()%(high-low+1) + low
		xs[pivotIndex], xs[high] = xs[high], xs[pivotIndex] // move pivot to end
		p := partition(xs, low, high)
		if p == k {
			return xs[p]
		} else if p < k {
			low = p + 1
		} else {
			high = p - 1
		}
	}
	return xs[k] // fallback
}

// quickMedian returns the median in expected O(n) time.
// In case of an odd number of elements, it returns the middle one.
// In case of an even number of elements, it returns the higher of the two middle ones.
// Note: This function modifies the input slice. To avoid this, pass a copy of the slice.
func QuickMedian(xs []float64) float64 {
	n := uint64(len(xs))
	median := quickselect(xs, n/2)
	return median
}
