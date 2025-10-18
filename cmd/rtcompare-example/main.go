package main

import (
	"fmt"

	"github.com/TomTonic/rtcompare"
)

func main() {
	A := []float64{100, 101, 99, 98, 102, 153, 97, 100, 99, 101, 98}
	B := []float64{120, 118, 122, 189, 121, 180, 117, 123, 119, 121, 118}
	thresholds := []float64{0.1, 0.2, 0.3}
	conf, err := rtcompare.CompareRuntimes(A, B, thresholds, 10000)
	if err != nil {
		panic(err)
	}
	for _, r := range conf {
		fmt.Printf("Speedup ≥ %.0f%% → Confidence: %.4f\n", r.RelativeSpeedupSampleAvsSampleB*100.0, r.Confidence)
	}
}
