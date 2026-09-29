package prng_test

import (
	"fmt"

	"github.com/TomTonic/rtcompare/prng"
)

// NewDPRNG gives every seed its own fixed sequence, so inputs generated from
// it are the same in every run.
func ExampleNewDPRNG() {
	a, b := prng.NewDPRNG(42), prng.NewDPRNG(42)
	same := true
	for range 1000 {
		if a.Uint32N(100) != b.Uint32N(100) {
			same = false
		}
	}
	fmt.Println("same sequence from the same seed:", same)
	// Output:
	// same sequence from the same seed: true
}

// Shuffle puts fixtures in a seeded order, e.g. the order in which a
// benchmark builds its data structures in each process.
func ExampleDPRNG_Shuffle() {
	rng := prng.NewDPRNG(7)
	order := []string{"A", "B", "C", "D"}
	rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	again := prng.NewDPRNG(7)
	repeat := []string{"A", "B", "C", "D"}
	again.Shuffle(len(repeat), func(i, j int) { repeat[i], repeat[j] = repeat[j], repeat[i] })
	fmt.Println("reproducible:", fmt.Sprint(order) == fmt.Sprint(repeat))
	// Output:
	// reproducible: true
}
