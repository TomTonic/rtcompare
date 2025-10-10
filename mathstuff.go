package rtcompare

import (
	"math"
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
