# rtcompare

[![Go Reference](https://pkg.go.dev/badge/github.com/TomTonic/rtcompare.svg)](https://pkg.go.dev/github.com/TomTonic/rtcompare)
[![Linter](https://github.com/TomTonic/rtcompare/actions/workflows/linter.yml/badge.svg)](https://github.com/TomTonic/rtcompare/actions/workflows/linter.yml)
[![Tests](https://github.com/TomTonic/rtcompare/actions/workflows/coverage.yml/badge.svg?branch=main)](https://github.com/TomTonic/rtcompare/actions/workflows/coverage.yml)
![coverage](https://raw.githubusercontent.com/TomTonic/rtcompare/badges/.badges/main/coverage.svg)
[![Vulnerabilities](https://img.shields.io/endpoint?url=https://gist.githubusercontent.com/TomTonic/cbfad7cf38e139ba898fe41386efa4db/raw/release_scan.json)](https://gist.github.com/TomTonic/cbfad7cf38e139ba898fe41386efa4db#file-release_scan-md)

## Benchmark two code paths and know whether the difference is real

rtcompare answers one question: **is A really faster than B, and by how much?** Benchmarking that well means dodging a long list of traps, and this library deals with each of them for you, statistical significance included. One call, `rtcompare.Compare`, returns a verdict together with the fine print that qualifies it. You do not need to know statistics to use it; when it warns you, [HOWTO.md](HOWTO.md) explains in plain language what the warning means and what to do.

Keywords: benchmarking, performance, bootstrap, runtime comparison, statistics, deterministic prng, go

## What goes wrong when you compare two benchmarks, and what rtcompare does about it

| The problem | What rtcompare does |
|---|---|
| The clock is coarse. A single call to a fast function is below its resolution. | Sizes the batches automatically so the clock contributes at most a chosen share of error. Differences far below the clock's resolution become measurable. |
| Whichever code runs second finds warm caches, or the other way round. | Interleaves the two candidates, alternates who goes first, and warms both up together. |
| Setup and garbage collection leak into the numbers. | Keeps setup outside the measured region and places GC deterministically. |
| Two runs of *identical* code differ. A "10% win" may be less than what the machine invents on its own. | Measures that noise floor by running each candidate against itself, and tells you whether the result clears it. |
| The machine drifts during the run: thermal throttling, a busy neighbour, a suspended laptop. | Tests every run for a trend and for suspension, and warns. |
| "A is 12% faster" from one run says nothing about how sure you can be. | Bootstrap resampling gives an interval and the confidence that A beats B by at least the margin you care about, with the dependence between neighbouring samples taken into account. |
| Big or pointer-heavy data: where it happens to sit in memory moves the result by several points, and a new process gets a new layout. | `multiproc` runs the comparison in several processes and pools them, so the interval covers that scatter too. |
| Trees, maps and other structures behave differently under insertions and deletions than under a fixed input. | `workload` generates realistic, reproducible insert/delete streams and compares structures under them. |

It is more machinery than `testing.B`, and that is the point: `testing.B` gives you a number and leaves the question of whether two numbers really differ to you. rtcompare is for when the answer matters, for example to decide whether a rewrite is worth merging, to gate a pull request in CI, or to defend a claim in a paper. It is not a replacement for profiling, and the standard `testing` package remains the right tool for a quick look at a single function.

## Install

```shell
go get github.com/TomTonic/rtcompare
```

## Quickstart

```go
package main

import (
	"fmt"

	"github.com/TomTonic/rtcompare"
)

var sink float64

func main() {
	candidateA := rtcompare.Candidate{Name: "A", Batch: func(n uint64) {
		var acc float64
		for range n {
			acc += doSomething()
		}
		sink += acc
	}}
	candidateB := rtcompare.Candidate{Name: "B", Batch: func(n uint64) { /* ... */ }}

	// Everything is left at its default: the batches are sized so that the clock
	// contributes at most a tenth of a percent, both candidates are validated
	// against themselves, the order is interleaved, and the resampling scheme is
	// chosen from the dependence actually measured.
	report, err := rtcompare.Compare(candidateA, candidateB, rtcompare.CompareOptions{
		Thresholds: []float64{0.05, 0.10, 0.20},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(report)

	if report.Resolved {
		fmt.Printf("A is faster by %s\n", report.Estimate)
	}
}
```

which prints something like

```
A 712.7 per op, B 1262 per op
difference +43.52% [+42.31%, +44.48%] at 95% confidence; B/A 1.771× [1.733×, 1.801×]
noise floor 1.765%, autocorrelation +0.344, resampled in blocks of 5
resolved: A is faster than B
  warning: candidate B drifted during the run, shifting -7.12% from its first
  half to its second; the machine did not hold still
  confidence that A beats B by 5.00%: 100.0%
```

`Compare` validates both candidates against themselves before comparing them, so it costs a few seconds. Set `SkipValidation` to pay only for the measurement, accepting that the result then has no noise floor to be read against. `cmd/rtcompare-example` shows the one call, and then the same measurements taken apart by hand.

## Which call do I need?

| What you compare | Call |
|---|---|
| Two functions or code paths whose data fits in the CPU caches | `rtcompare.Compare` |
| Data structures under insertions and deletions | `workload.Compare` |
| Anything whose data is larger than the caches, or full of pointers (trees, linked structures, maps of heap objects) | `multiproc.Main` or `multiproc.RunTest` |
| Data structures under insertions and deletions, larger than the caches | `workload.Suite` with `multiproc.MainSuite` or `multiproc.RunTestSuite` |

"Fits in the caches" means, as a rule of thumb, well below 16 MB of live data for both candidates together; `Compare` warns above that. The reason for the last two rows is explained in [One process is one observation](HOWTO.md#one-process-is-one-observation).

## Where to go next

- **[HOWTO.md](HOWTO.md)**: what to do, step by step, in plain language: how to write a `Batch`, what each warning means, and how to fix it.
- **[API.md](API.md)**: everything the library exports, and when to reach for each part.
- **[BACKGROUND.md](BACKGROUND.md)**: why it works the way it does, with the measurements behind each choice, and what a result cannot tell you (in particular: you measure the loop, not the isolated function).
- **[pkg.go.dev](https://pkg.go.dev/github.com/TomTonic/rtcompare)**: the reference.

## Contributing

Contributions welcome. Please open issues or PRs for bug reports, performance tweaks, or additional comparison strategies. Add tests for any behavioral changes and keep CPU/memory overheads small.

## License

MIT — see LICENSE file.
