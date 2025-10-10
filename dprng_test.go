package rtcompare

import (
	"testing"

	set3 "github.com/TomTonic/Set3"
	"github.com/stretchr/testify/assert"
)

func TestPrngSeqLength(t *testing.T) {
	state := DPRNG{State: 0x1234567890ABCDEF}
	limit := uint32(30_000_000)
	set := set3.EmptyWithCapacity[uint64](limit * 7 / 5)
	counter := uint32(0)
	for set.Size() < limit {
		set.Add(state.Uint64())
		counter++
	}
	assert.True(t, counter == limit, "sequence < limit")
}

func TestPrngDeterminism(t *testing.T) {
	state1 := DPRNG{State: 0x1234567890ABCDEF}
	state2 := DPRNG{State: 0x1234567890ABCDEF} // create two differnet instances with the same seed
	limit := 30_000_000
	for i := range limit {
		v1 := state1.Uint64()
		v2 := state2.Uint64()
		assert.True(t, v1 == v2, "out of sync: values not equal in round %d", i)
	}
	_ = state2.Uint64() // skip one value to get both prng out of sync
	for i := range limit {
		v1 := state1.Uint64()
		v2 := state2.Uint64()
		assert.False(t, v1 == v2, "in: values equal in round %d", i)
	}
	_ = state1.Uint64() // get both prng back in sync
	for i := range limit {
		v1 := state1.Uint64()
		v2 := state2.Uint64()
		assert.True(t, v1 == v2, "out of sync: values not equal in round %d", i)
	}
}
