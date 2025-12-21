package rtcompare

import (
	"crypto/rand"
	"encoding/binary"
	"math"
)

// CPRNG is a cryptographically secure random number generator ("CryptographicPrecisionRNG")
// that reads random bytes in batches to reduce the number of calls to the underlying
// crypto/rand.Reader (OS call). This improves performance while maintaining security
// and provides high-precision output suitable for statistical and numerical
// work. This RNG is thread-safe as long as each goroutine uses its own instance.
// The memory footprint can be adjusted by changing the capBytes parameter in NewCPRNG.
type CPRNG struct {
	bufPos uint32
	buf    []byte
}

// NewCPRNG creates a new CPRNG with a buffer capacity of capBytes.
// The buffer is filled with random bytes upon creation and refilled as needed.
// A larger buffer reduces the number of operating system calls to crypto/rand.Reader,
// improving performance. A smaller buffer reduces memory usage.
// This random number generator is not deterministic in the sequence of numbers it generates.
// This random number generator is not deterministic in its runtime (i.e., it does not have a constant runtime as it needs to periodically refill its buffer via to crypto/rand.Reader, an OS call).
// This random number generator is cryptographically secure (relying on crypto/rand, see https://pkg.go.dev/crypto/rand).
// This random number generator is thread-safe as long as each goroutine uses its own instance.
// This random number generator has a varying memory footprint (usually a few kilobytes).
func NewCPRNG(capBytes uint32) *CPRNG {
	if capBytes < 8 {
		capBytes = 8 // minimum buffer size to hold at least one uint64
	}
	b := &CPRNG{buf: make([]byte, capBytes)}
	if _, err := rand.Read(b.buf); err != nil {
		panic(err)
	}
	b.bufPos = 0
	return b
}

// ensure that n bytes are available, otherwise refill the buffer
func (c *CPRNG) ensure(n int) {
	if c.bufPos+uint32(n) > uint32(len(c.buf)) {
		if _, err := rand.Read(c.buf); err != nil {
			panic(err)
		}
		c.bufPos = 0
	}
}

// Uint64 returns a uniformly distributed uint64.
func (c *CPRNG) Uint64() uint64 {
	c.ensure(8)
	v := binary.LittleEndian.Uint64(c.buf[c.bufPos : c.bufPos+8])
	c.bufPos += 8
	return v
}

// Int64 returns a uniformly distributed int64.
func (c *CPRNG) Int64() int64 {
	v := c.Uint64()
	return int64(v)
}

// Uint32 returns a uniformly distributed uint32.
func (c *CPRNG) Uint32() uint32 {
	c.ensure(4)
	v := binary.LittleEndian.Uint32(c.buf[c.bufPos : c.bufPos+4])
	c.bufPos += 4
	return v
}

// Int32 returns a uniformly distributed int32.
func (c *CPRNG) Int32() int32 {
	v := c.Uint32()
	return int32(v)
}

// Uint16 returns a uniformly distributed uint16.
func (c *CPRNG) Uint16() uint16 {
	c.ensure(2)
	v := binary.LittleEndian.Uint16(c.buf[c.bufPos : c.bufPos+2])
	c.bufPos += 2
	return v
}

// Int16 returns a uniformly distributed int16.
func (c *CPRNG) Int16() int16 {
	v := c.Uint16()
	return int16(v)
}

// Uint8 returns a uniformly distributed uint8.
func (c *CPRNG) Uint8() uint8 {
	c.ensure(1)
	v := c.buf[c.bufPos]
	c.bufPos++
	return v
}

// Int8 returns a uniformly distributed int8.
func (c *CPRNG) Int8() int8 {
	v := c.Uint8()
	return int8(v)
}

// Float32 returns a uniformly distributed float32 in [0.0, 1.0).
// This function will never return -0.0.
// This function will never return 1.0.
// This function will never return NaN or Inf.
// If you need random values in a different range, scale and shift the result accordingly.
// This function uses 23 random bits for the mantissa. This is the maximum randomness
// that can be represented in a float32 without breaking uniformity.
// If you need more randomness, use Float64 instead.
// See: https://en.wikipedia.org/wiki/Single-precision_floating-point_format
func (c *CPRNG) Float32() float32 {
	c.ensure(4)
	u := binary.LittleEndian.Uint32(c.buf[c.bufPos : c.bufPos+4])
	c.bufPos += 4

	u &= 0x7FFFFF // 23 random bits for mantissa

	const sign uint32 = 0
	const exp uint32 = 127
	bits := (sign << 31) | (exp << 23) | u
	v := math.Float32frombits(bits) - 1.0
	return v
}

// Float64 returns a uniformly distributed float64 in [0.0, 1.0).
// This function will never return -0.0.
// This function will never return 1.0.
// This function will never return NaN or Inf.
// If you need random values in a different range, scale and shift the result accordingly.
// This function uses 52 random bits for the mantissa. This is the maximum randomness
// that can be represented in a float64 without breaking uniformity.
// See: https://en.wikipedia.org/wiki/Double-precision_floating-point_format
func (c *CPRNG) Float64() float64 {
	c.ensure(8)
	u := binary.LittleEndian.Uint64(c.buf[c.bufPos : c.bufPos+8])
	c.bufPos += 8

	u &= 0x000FFFFFFFFFFFFF // 52 random bits for mantissa

	const sign uint64 = 0
	const exp uint64 = 1023
	bits := (sign << 63) | (exp << 52) | u
	v := math.Float64frombits(bits) - 1.0
	return v
}

// Uint32N returns a non-negative pseudo-random number in the half-open interval [0,n).
// Use this function for generating random indices or sizes for slices or arrays, for example.
// Even though this function will probably not be inlined by the compiler, it has a
// very efficient implementation avoiding division or modulo operations.
// This function compensates for bias.
// For n=0 and n=1, Uint32N returns 0.
//
// For implementation details, see:
//
//	https://lemire.me/blog/2016/06/27/a-fast-alternative-to-the-modulo-reduction
//	https://lemire.me/blog/2016/06/30/fast-random-shuffling
func (c *CPRNG) Uint32N(n uint32) uint32 {
	v := c.Uint32()
	prod := uint64(v) * uint64(n)
	low := uint32(prod)
	if low < uint32(n) {
		thresh := uint32(-n) % uint32(n)
		for low < thresh {
			v = c.Uint32()
			prod = uint64(v) * uint64(n)
			low = uint32(prod)
		}
	}
	return uint32(prod >> 32)
}
