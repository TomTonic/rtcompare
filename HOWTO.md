# How to get a trustworthy answer out of rtcompare

This is written for someone who is not a statistician and does not want to
become one. It explains what to actually do, in what order, and — this is the
part most guides skip — **what to do when something looks wrong**, because
something looking wrong is usually the measurement telling you something true.

If you just want the short version: pick the call from
[Which call do I need?](#which-call-do-i-need), and read what it prints. This
document exists for two reasons: to explain what that one call is doing on
your behalf, and to walk through doing it by hand for the cases where you need
more control than it gives you.

## What question this actually answers

You have two ways of doing something in Go — two functions, two algorithms, two
data structures — and you want to know: **is one of them actually faster than
the other, on this machine, right now?**

That sounds simple. It is not, for two reasons that have nothing to do with
your code:

1. **Your computer is noisy.** Between runs, the operating system schedules
   other work, the CPU changes clock speed to manage heat, memory access
   patterns change, caches warm up and cool down. Run the exact same code
   twice and you will get two different numbers, even though nothing about the
   code changed.
2. **The clock itself is coarse.** If one run of your code takes a few
   nanoseconds, and the clock you're using to time it only ticks every 40 or
   100 nanoseconds (depending on your operating system), you cannot time a
   single run at all. You are trying to weigh a feather with a bathroom scale.

rtcompare exists to get a real answer despite both problems: it runs your code
many times, times whole *batches* of repetitions instead of single ones (which
defeats the clock problem), and then uses statistics to separate "genuinely
different" from "just noise" (which defeats the scheduler-and-cache problem).

The price of doing this honestly is that the answer is never a bare number. It
is always a number plus a statement of how much to trust it — and sometimes
the honest answer is "I can't tell, and here is why."

## Which call do I need?

| What you compare | Call |
|---|---|
| Two functions or code paths whose data fits in the CPU caches | `rtcompare.Compare` |
| Data structures under insertions and deletions | `workload.Compare`, see [Benchmarking insertions and deletions](#benchmarking-insertions-and-deletions) |
| Anything whose data is larger than the caches, or full of pointers (trees, linked structures, maps of heap objects) | `multiproc.Main` or `multiproc.RunTest`, see [One process is one observation](#one-process-is-one-observation) |
| Data structures under insertions and deletions, larger than the caches | `workload.Suite` with `multiproc.MainSuite` or `multiproc.RunTestSuite` |

"Fits in the caches" means, as a rule of thumb, well below 16 MB of live data
for both candidates together; `Compare` warns above that. Why the size decides:
for large or pointer-heavy data, *where* the data happens to lie in memory
moves the result by several points, and that placement is fixed for the life
of a process and different in the next one. A single run then reports a narrow
interval around a number the next run does not confirm. The `multiproc`
package runs the comparison in several processes and pools them.

If in doubt, start with `Compare`: if your data is too large for it, its
warnings say so.

## The five-minute version

If your data fits in the caches, this is the whole document:

```go
report, err := rtcompare.Compare(candidateA, candidateB, rtcompare.CompareOptions{
    Thresholds: []float64{0.05, 0.10, 0.20}, // optional: "is A at least 5/10/20% faster?"
})
if err != nil {
    panic(err)
}
fmt.Println(report)

if report.Resolved {
    fmt.Printf("A is different from B by %s\n", report.Estimate)
}
```

`Compare` runs the entire protocol described in the rest of this document —
sizing the measurement, checking the machine's own noise level, running the
comparison, checking for a trend across the run, choosing the right statistical
method, and computing a result — and hands you back a `Report`. Two fields
matter most:

- **`report.Resolved`** is a plain yes/no: is this difference big enough, and
  certain enough, to act on? It is deliberately conservative. `false` does not
  mean "the two are equally fast" — it means "this run did not prove they are
  different." That is an important distinction; see [What "not resolved" does
  and does not mean](#what-not-resolved-does-and-does-not-mean).
- **`report.Warnings`** is a list of plain-English sentences about anything
  that undermines the result — the machine seemed to slow down mid-run, the
  measurement was too coarse, the noise floor is unusually high. **Read these
  even when `Resolved` is true.** A result can clear every bar and still have
  a warning worth knowing about.

Printing `report` gives you a short summary; the sections below explain what
each part of it means and, more usefully, what to do about it.

If `Compare`'s defaults don't fit your case — you need a specific number of
repeats, you want to skip validation to save time, you're comparing something
other than raw speed — read on. Everything below is what `Compare` does for
you, spelled out so you can adjust any part of it.

## The one rule for writing your `Batch` function

Before any of the machinery below can help you, your code has to be wrapped
correctly. rtcompare doesn't call your function once per timing measurement —
it calls it once per *batch* of `n` repetitions, and you write the loop:

```go
candidate := rtcompare.Candidate{
    Name: "my approach",
    Batch: func(n uint64) {
        for range n {
            result := myFunction(input)
            sink += result // see below
        }
    },
}
```

Two things matter here, and both exist to keep the measurement honest:

**Why does the batch own the loop, instead of rtcompare calling your function
`n` times itself?** Because if rtcompare called a function pointer once per
repetition, you would mostly be measuring the cost of calling a function
pointer, and the Go compiler could not inline, optimize, or otherwise treat
your code the way it would in a real program. Putting the loop inside `Batch`
lets the compiler compile it the same way it would compile your actual
application code. This is also *why* rtcompare can measure things far below
the length of a single clock tick at all: it isn't timing one repetition, it's
timing a few thousand of them and dividing, so the clock's coarseness gets
divided down along with everything else. `CalibrateInnerLoops` (below) is what
picks `n` for you.

**Why the `sink +=`?** Go does not delete a loop just because nothing reads
its result — unlike some C and C++ compilers, so this is mostly not a trap you
can fall into by accident in Go. But assigning every result to a
package-level variable is cheap insurance against the one case where it *can*
happen: if the compiler can prove at compile time that the loop always
produces the same value (for example, if the input never changes), it may
fold the whole thing to a constant and your "measurement" becomes the time it
takes to do nothing. Declare `var sink SomeType` at package level and add to
it; that alone is enough to keep the compiler honest.

If your code needs setup that shouldn't be timed — allocating a buffer,
building a test fixture — put it in `Candidate.Setup`, which runs before each
batch but outside the timed region. `Candidate.Teardown` is the same for
cleanup after. Anything your code needs *per repetition inside the loop*
(regenerating an input it mutates, say) cannot be moved to `Setup`, because
`Setup` runs once per batch, not once per repetition — and that per-repetition
cost gets measured along with your code. See
[Attenuation](#attenuation-your-number-is-real-but-smaller-than-the-truth)
for what this costs you.

## Benchmarking insertions and deletions

If your candidates are data structures and the question is how fast they
*change* — insert, delete, grow, shrink — the obvious batch is a trap:

```go
Batch: func(n uint64) {
    for i := range n {
        m[key(i)] = value // insert
        delete(m, key(i)) // and take it out again, so the map stays the same
    }
}
```

That measures a structure that never changes shape. The element goes into
the same slot it just left; nothing splits, merges, resizes, rehashes or
leaves a tombstone, and those are exactly the costs that separate one data
structure from another in real use. The honest alternatives have their own
traps: picking what to delete inside the timed loop adds work to both
candidates and dilutes the difference; a stream that deletes elements that
aren't there times no-ops; a structure that keeps growing over the run is a
different structure at the last sample than at the first.

The `workload` package does all of this. You describe each data structure by
how to create an empty one and how to apply an operation to it:

```go
goMap := workload.Structure[map[uint64]struct{}]{
    Name: "map",
    New:  func() map[uint64]struct{} { return make(map[uint64]struct{}, 100_000) },
    Apply: func(m map[uint64]struct{}, run []workload.Op) {
        for _, op := range run {
            if op.Kind == workload.Insert {
                m[op.Key] = struct{}{}
            } else {
                delete(m, op.Key)
            }
        }
    },
}
res, err := workload.Compare(100_000, goMap, otherSet, workload.Options{})
fmt.Println(res)
```

Each operation carries its key in `op.Key`: distinct for distinct elements,
and scattered over the whole `uint64` range, so that the keys of long-lived and
short-lived elements are interleaved as in real use. (The elements are also
numbered, `op.ID`, and those numbers are sequential; used as keys, they would
put every insertion at the right edge of an ordered structure.) For other key
types, map `op.ID` to your own keys through a precomputed slice. You get **two answers**, because there are
two different questions, and mixing them into one number would get both
wrong:

- **Steady state:** what does one insertion or deletion cost in a structure
  that has been in use for a while? Both structures are filled to 100,000
  elements, then a *cycle* is replayed on them: bursts of insertions and
  deletions of 100,000 further, transient elements, about 12,500 of them present
  at a time, ending exactly where it started, so it can repeat endlessly.
- **Build:** what does it cost to build such a structure from empty, with the
  same kind of back-and-forth along the way? Every sample is one complete
  build of a fresh structure.

The reason for the split: the first pass of a cycle is where a structure grows
to the largest size the cycle reaches. For a Go map that pass cost up to seven
times as much per operation as every pass after it. Timing it along with the
steady state would spread a one-time cost over the measurement, in a
proportion that depends on how long the run was. Dropping it would favour
structures whose growth is expensive. So it is measured where it belongs, in
the build, where creating the structure with or without a capacity hint, every
resize and the garbage all count, and the steady state starts after one
untimed cycle. If the two answers disagree, that is the result: one structure
can be faster to use and slower to build.

What `workload.Compare` takes care of, so that you don't have to:

- Every operation is valid by construction: nothing is inserted twice and
  nothing is deleted that isn't there.
- The streams are precomputed; the timed loop only reads the next operation.
- Both structures are built in alternating chunks, so neither is the one built
  last.
- The first pass of the cycle is untimed, and the position in the cycle stays
  with each structure across batches.
- Whole builds take milliseconds, so the build comparison uses its own, smaller
  defaults (31 repeats, 10 validation runs, about 1,300 builds in all) and
  collects garbage between batches, so that one build's garbage is not
  collected in the middle of the next.

`workload.Config` tunes the streams: `Ratio` (insertions per element at rest,
default 2), burst length, and `Victims`, which chooses what a deletion removes:
`Uniform` (the default, like a general-purpose map), `FIFO` (a queue or a
retention window), or `LIFO` (a stack or undo log).

Both answers describe one run of your program. For structures of more than a
few megabytes, or full of pointers, that is one observation of where their
memory happened to lie, see
[One process is one observation](#one-process-is-one-observation). Run the
comparison in several processes instead, which takes one line:

```go
func main() {
    multiproc.MainSuite(multiproc.Options{},
        workload.Suite("map vs other", 1_000_000, goMap, otherSet, workload.Options{}))
}
```

Each process runs `workload.Compare` once, with the structure built second
alternating between processes, and both answers come back pooled, as
`map vs other/steady state` and `map vs other/build`. In a test,
`multiproc.RunTestSuite(t, ...)` does the same.

For setups `Compare` does not cover, the parts are available on their own:
`workload.Cycle` and `workload.Build` make the streams, `workload.Replay`
replays a cycle on one structure (including the untimed first pass and
`Settle`, which returns the structure to its start state), and
`workload.Check` replays any stream against a model and reports the first
invalid operation.

## The long version: what `Compare` does, step by step

This is the sequence `Compare` runs automatically. Read it if you want to
understand what's happening, if you're calling the pieces yourself, or if
you're trying to figure out why a result looks strange.

### Step 1 — Decide how long one batch needs to run

**Function:** `CalibrateInnerLoops`

The clock on your machine can only tell time in fixed-size ticks — on a Mac
that's about 40 nanoseconds, on Linux around 1 nanosecond, on Windows about
100 nanoseconds. If your code runs faster than one tick, timing a single
execution is meaningless: you'd get "0 ticks" or "1 tick," which tells you
nothing.

The fix is to time a *batch* of many repetitions at once and divide. If you
run your code 50,000 times and the whole batch takes 50,000 nanoseconds, you
know each repetition took about 1 nanosecond on average — even though no
single repetition could be timed on its own. The more repetitions per batch,
the finer the resolution you get, because whatever error the clock's coarseness
introduces gets spread across all of them.

`CalibrateInnerLoops` figures out, automatically, how many repetitions (`n`)
one batch needs so that the clock's coarseness contributes only a tiny,
controlled amount of error — a tenth of a percent by default. You almost never
need to call this yourself; leaving `CollectOptions.InnerLoops` at zero (the
default) does it for you. `Compare` does this once, up front, for both
candidates, and reuses the same batch size for every later step — that
matters, because a noise measurement taken at one batch size doesn't tell you
anything trustworthy about a comparison run at a different one.

**When to touch this yourself:** almost never. The one exception is if
calibration fails with an error saying it couldn't reach the target batch
duration — see [Troubleshooting](#troubleshooting-what-to-do-when).

### Step 2 — Find out what your own machine invents from nothing

**Function:** `ValidatePair` (or `ValidateHarness` for a single candidate)

This is the step every other benchmarking approach skips, and it's the reason
rtcompare exists.

Here is the experiment: take *one* candidate, and compare it against **itself**
— literally the same code, measured twice. Since it's the same code, any
"difference" the tool reports between the two runs is not real. It's pure
noise: scheduler jitter, cache effects, thermal throttling, whatever else
your machine happened to be doing. `ValidateHarness` runs this experiment
several times and tells you how big that noise typically gets. This number is
called the **noise floor**.

Why does this matter? Because a bootstrap confidence calculation (Step 6, later)
can tell you "I am 99% confident A is faster than B" — and be completely
right about that confidence — while the actual difference is still smaller than
what your machine invents between two runs of the *same* code. High confidence
in a small, meaningless number is not a contradiction; the confidence is about
whether the result would repeat if you resampled the same data, not about
whether the data was trustworthy in the first place. The noise floor is the
only thing in this toolkit that can catch that.

**Do this for *both* candidates**, not just one. They don't have to be equally
well-behaved — one might allocate memory and get interrupted by garbage
collection more than the other — and your comparison is only as trustworthy as
the *worse* of the two. `Compare` does this automatically and uses the higher
(worse) of the two floors.

**And do it for both *together*,** with `ValidatePair(a, b, ...)`, rather than
calling `ValidateHarness(a)` and then `ValidateHarness(b)`. Validating one
candidate on its own means running it alone for a while, and whatever runs
alone last leaves the CPU's caches full of its own data. If you validate A and
then B and then compare them, B starts the comparison warm and A starts cold.
See [Whatever ran last starts warm](#whatever-ran-last-starts-warm) for how
much that can matter. `ValidatePair` interleaves the two validations batch by
batch, so that neither candidate ever has the machine to itself, and the noise
floors are measured with the other candidate competing for the caches, just
as it will in the real comparison.

**What you get back**, and what each number tells you:

- **`NoiseFloor`** — the practical floor. If your measured difference is
  smaller than this, you have measured nothing, however confident the
  bootstrap sounds. Note that this is a *high percentile*, not an absolute
  ceiling — roughly one run in ten of identical code will exceed it. That's
  intentional: a true ceiling (the worst case ever observed) gets worse the
  more carefully you check, which would be a strange thing for "more
  validation" to do to your confidence.
- **`TieRate`** — how often two measurements come out *exactly* equal. This
  usually means your measurement is too coarse — see
  [Troubleshooting](#the-tie-rate-is-high).
- **`DriftRate`** — how often the machine seemed to change behavior partway
  through a run (see Step 4).
- **`Autocorrelation`** — how much each measurement resembles the one right
  before it (see Step 5).

### Step 3 — Actually measure both candidates

**Function:** `Collect`

Now run the real comparison: alternate between candidate A and candidate B,
batch after batch, and record one timing per batch for each. By default the
order alternates as A-B-B-A-B-A-A-B... (called ABBA), which is cheap insurance
against a machine that's changing over time — if it's getting slower as the
run progresses, both candidates are equally exposed to that instead of
whichever one happens to run later.

Before the first measured batch, `Collect` warms both candidates up, again
alternating between them, for at least `CollectOptions.WarmupDuration` (300 ms
by default). That is not about the one-time costs you might expect a warm-up
for, such as page faults; a single batch would cover those. It is about the
caches, see [below](#whatever-ran-last-starts-warm).

This gives you two lists of numbers — `SamplesA` and `SamplesB` — one
measurement per batch. Everything from here on works from these two lists.

### Step 4 — Check whether the machine held still

**Function:** `DetectDrift`

The statistics in the next steps (the bootstrap) treat your list of
measurements as an unordered bag of numbers — as if you'd drawn them all at
once, in no particular order. But you didn't: you drew them in sequence, over
however long the run took. If your machine was getting steadily slower (say,
from thermal throttling) or steadily faster (say, from the CPU ramping up
after being idle), that shows up as a **trend across the run** — and the
bootstrap is structurally blind to it, because it discards the order the
numbers arrived in.

`DetectDrift` looks specifically for that trend, on each candidate's series
separately. `Compare` also runs it on the ratio B/A of each pair of
neighbouring batches (`Report.DriftRatio`), which catches something the two
series on their own cannot: one candidate that started with an advantage and
loses it as the run goes on. A machine that slows down slows both candidates
and leaves the ratio flat; a head start moves the ratio and nothing else. If it finds one, it's worth knowing even though ABBA ordering
already protects the *comparison* from being biased by it — a real trend
means your measurements are less independent of each other than the
statistics assume, which affects how much you should trust *any* interval or
confidence number that follows.

### Step 5 — Check whether measurements depend on their neighbors

**Function:** `lag1Autocorrelation`, feeding into a choice between
`BootstrapConfidence` and `BlockBootstrapConfidence`

A related but distinct problem: even without an overall trend, one
measurement can be quietly correlated with the one right before it — a slow
batch tends to be followed by another slow batch, say, because whatever
disturbed the first one (another process waking up, a cache getting cold) is
still going on. The ordinary bootstrap (Step 6) assumes each measurement is
independent of the others; when they're not, it becomes *overconfident* — it
reports a tighter, more certain answer than the data actually supports.

The fix, when this correlation is strong enough to matter (above roughly
0.2), is to resample in contiguous *blocks* of measurements instead of one at
a time, which keeps nearby measurements together and preserves the
dependence between them rather than pretending it isn't there. `Compare` checks this and switches to
`BlockBootstrapConfidence` automatically when it's warranted; below that
threshold, blocks cost you nothing but don't help either, so the plain
version is used. `ValidateHarness` only reports the autocorrelation it saw, so
if you're doing this by hand, make the switch yourself.

**You never need to compute this by hand.** It's here so that when a `Report`
says "resampled in blocks of 5," you know why, and so you understand why the
threshold exists if you're calling the bootstrap functions directly.

### Step 6 — Compute the actual comparison

**Functions:** `CompareSamples` (confidence against thresholds you name) and
`EstimateDifference` (the size of the difference, with an interval)

This is the step that finally answers "how different are they, and how sure
am I?" There are two slightly different questions you might be asking, and
rtcompare has a function for each:

**"Is A at least X% faster than B?"** — use `CompareSamples`, or read
`report.Confidence` if you gave `Compare` a list of `Thresholds`. You give it
one or more thresholds (5%, 10%, 20% — whatever you actually care about,
such as a performance budget you need to hit) and it tells you, for each one,
how confident you can be that the true difference meets it. This is the right
tool when you already know the number you care about — a regression budget, a
release gate.

**"How big is the difference, actually?"** — use `EstimateDifference`, or read
`report.Estimate`. Instead of testing against a threshold you provide, this
gives you the difference itself, plus an interval around it: "A is 23% faster,
and we're 95% confident the true value is somewhere between 18% and 28%."
This is the right tool when you don't have a specific number in mind and just
want to know what's going on.

Both work by **bootstrap resampling**: computer-science-speak for "shuffle the
measurements you have, with repeats allowed, recompute the answer thousands of
times, and see how much the answer moves around." If the answer barely moves
no matter how you reshuffle, you can trust it. If it swings wildly, you can't
— and that swinginess *is* the confidence interval. You don't need to
understand the mechanics to use it; you need to know that "5,000 resamples" (the
default) is doing exactly what it sounds like, and more resamples cost more
CPU time for a more precise (but not more *correct*) answer to the same
question.

### Step 7 — Put it together

`Compare` folds all of the above into `report.Resolved`: a difference counts
as resolved only if **both** of these are true:

1. The interval from Step 6 does not include zero (a real difference, not
   just noise scattering around zero), **and**
2. The size of that difference is bigger than the noise floor from Step 2
   (a difference big enough to matter on this machine, not an artifact).

Either condition alone is not enough — see
[What "not resolved" does and does not mean](#what-not-resolved-does-and-does-not-mean).

## What "not resolved" does and does not mean

This trips people up, so it's worth stating plainly:

**`Resolved == false` does not mean "A and B are the same speed."** It means
*this run, on this machine, did not produce enough evidence to say they're
different.* Those are not the same claim. The true difference might be real
but too small for this setup to see (below the noise floor), or your sample
might genuinely have been too noisy this particular time (a one-off machine
hiccup that would look completely different on a re-run).

If you get `Resolved == false` and want a real answer, in rough order of what
to try first:

1. **Do not simply run it again until it resolves.** Each run has some chance
   of resolving by luck, so repeating until one does is a reliable way to
   manufacture a difference that is not there. Repeat a run to check that a
   result *reproduces*, and count every run, not the one you liked. If you
   need more evidence, collect it on purpose: run the comparison in several
   processes and pool them with `multiproc`, which also covers the
   process-to-process scatter a single run cannot see.
2. **Make the batches longer.** See
   [The measurement seems too coarse](#the-tie-rate-is-high) below — a coarser
   measurement has a higher noise floor, so a real but small difference can
   hide inside it.
3. **Quiet the machine down.** Close other applications, plug in a laptop
   (power-saving modes throttle the CPU), disable Turbo Boost / dynamic
   frequency scaling if your OS lets you, and avoid running anything else
   heavy at the same time.
4. **Accept it.** Sometimes two implementations really are close enough that
   the difference doesn't matter for your purposes. "Not resolved" at a
   demanding threshold can be a perfectly good answer: it tells you the
   difference, if any, is too small to be worth choosing between them for.

## Attenuation: your number is real, but smaller than the truth

One thing no amount of resampling can fix, so it's worth flagging on its own:
**you are always measuring the whole batch loop, not the isolated function.**
Whatever fixed cost sits in that loop besides your code under test — the loop
counter, an accumulator, regenerating an input — is added to *both*
candidates equally, so it never flips which one looks faster. But it does
shrink the apparent *size* of the difference, because it's a constant cost
that both share, diluting the relative size of whatever your code actually
does differently.

Concretely: a test where the true difference between two pieces of code was
exactly 50% measured as 35%, because a fixed 1.81 ns of loop overhead sat on
top of 2.13 ns of the actual work being compared. Subtracting an empty loop's
time as a baseline does not fix this — the compiler optimizes an empty loop
differently from a real one, so that correction barely helps.

**What this means for you:** move everything you can out of the loop and into
`Setup` (which isn't timed at all). What's left inside the loop is measured
honestly — as the cost of that whole region, not as an isolated number for
your function alone. Read a result as "how much faster is this measured
region," not "how much faster is this one function in isolation."

## Whatever ran last starts warm

Your CPU keeps recently used memory in its caches, and the last-level cache is
the one that matters here: tens of megabytes, shared by everything. Whatever
touched its data last, for long enough, owns most of that cache. That can be
one of your candidates, for reasons that have nothing to do with its speed:

- it was **built** last — you set up A's data structure, then B's;
- it was **validated** last, if you called `ValidateHarness` once per
  candidate;
- it was **calibrated** last, since calibration runs each candidate on its own.

The candidate that ran last then starts the measurement warm and the other
cold. When both candidates' data fit in the cache together, this evens out
within a batch or two and does not matter. When each one's data is about the
size of the last-level cache, it matters a lot. Short, calibrated batches may
not touch enough memory to turn the cache over, so the head start can survive
into the medians. Two *identical* 16 MB pointer-chasing structures, compared
on a machine with 32 MB of L3, came out 5 to 50% apart, always in favour of
the one that ran alone last, with narrow intervals that reported the
difference as real. (That is [issue #111](https://github.com/TomTonic/rtcompare/issues/111);
`cmd/rtcompare-aa` reproduces it.)

rtcompare deals with this in three ways, and `Compare` uses all of them:

1. `ValidatePair` validates both candidates together, so validation leaves
   neither of them ahead.
2. `Collect` warms both candidates up, alternately, for at least
   `CollectOptions.WarmupDuration`, 300 ms by default. In the experiment
   above, about 250 ms were enough to erase the head start. This is what a
   comparison now costs extra: 0.3 s per `Collect` and once more for the
   validation, 0.6 s per `Compare`. For small data that stays in the cache you
   can lower it, and `time.Nanosecond` warms up by batch count alone.
3. `Compare` checks the ratio B/A for a trend across the run
   (`Report.DriftRatio`) and warns when it is larger than the result's
   resolution — the sign that the candidates had not settled when the
   measurement began. If you see that warning, raise `WarmupDuration`.

What none of this can remove is a difference in **where** the data landed in
memory. Two structures with identical content, built one after the other, are
not laid out identically: different addresses, different pages, different
cache sets. In the experiment above, after the warm-up, the structure built
second was still 3 to 5% faster, whichever role it played, and a warm-up of
2 s instead of 0.3 s did not change that. That is a real difference between the
two data structures as they sit in memory, not a measurement artefact, and it
is why comparing large data in a single process is not enough; see
[issue #109](https://github.com/TomTonic/rtcompare/issues/109).

The practical checks, for any comparison of large data:

- **Swap the roles.** Compare (A, B) and then (B, A). A bias towards a role
  flips the sign of the delta when you swap; a real difference does not.
- **Swap the build order.** Build B's data first and A's second. If the
  result moves with the build order, you are measuring layout.
- **Do not validate candidates one at a time** before comparing them. Use
  `ValidatePair`, or `Compare`, which does.

## One process is one observation

Everything `Compare` reports — the interval, the noise floor, `Resolved` —
describes the noise **within one run of your program**. Run the same program
again and you get a new process, and a new process lays its data out in memory
differently: different addresses, different pages, different cache sets. For
small data that barely matters. For data larger than the caches, or full of
pointers (trees, linked structures, maps of heap objects), it can move the
difference between A and B by several points, and that shift is fixed for the
whole life of the process. No amount of `Repeats` inside the process sees it,
and the A/A validation cannot either, because both halves of an A/A run share
the same layout.

How big this gets was measured on a 1M-key ordered index
([issue #109](https://github.com/TomTonic/rtcompare/issues/109)): the same
comparison, repeated in separate processes, scattered 4 to 10 times more
widely than each process's own interval said it should. One process reported
`+14.3% [+13.2, +15.2]`, the next `+24.8%`. Merely adding an unrelated
structure to the fixture build moved another comparison from `+2.9%` to
`-27.3%`, both resolved with narrow intervals. Even for data that fits in the
cache, the intervals were about twice too narrow.

So the rule is: **when your data is large or pointer-heavy, one process is one
observation.** Run several and pool them. `Compare` reminds you: when the
program holds more than 16 MB of live data, its warnings say so.

### How

The `multiproc` package does all of it. You say how to build each candidate;
it does the rest:

```go
func main() {
    multiproc.Main(multiproc.Options{}, multiproc.Pair{
        Name: "lookup",
        A:    func() rtcompare.Candidate { return lookupIn(buildTreeA()) },
        B:    func() rtcompare.Candidate { return lookupIn(buildTreeB()) },
    })
}
```

In a test, `multiproc.RunTest(t, multiproc.Options{}, pair)` does the same and
returns the pooled results for your assertions. For data structures under
insertions and deletions, `workload.Suite` fits in the same place, through
`multiproc.MainSuite` or `multiproc.RunTestSuite`; see
[Benchmarking insertions and deletions](#benchmarking-insertions-and-deletions).
`multiproc.Suites` combines several suites, pairs included through
`multiproc.Pairs`, into one run of processes.

What happens behind that call, so that you don't have to remember any of it:

- **Your program is started again as child processes**, by default one after
  another, so they don't disturb each other (see
  [Serial or parallel](#serial-or-parallel) for the alternative). In a test,
  the children run only that test.
- **Each child gets a different heap layout.** Before anything is built, it
  fills the heap with a seeded amount of filler, so that your data lands at
  different addresses in each process (`rtcompare.PerturbHeap`). Otherwise
  every process would repeat the same layout, and its bias with it.
- **The build order alternates.** Even-numbered processes build A's data
  first, odd-numbered ones B's. This matters more than the heap: in the
  reproduction in `cmd/rtcompare-aa`, whichever of two identical 1M-node lists
  was built second was about 3% faster in every process, however the heap was
  perturbed. That is also why the builders are functions: they have to run
  inside each process, after the perturbation, in the right order. Build
  everything the measurement depends on inside them.
- **It runs as many processes as the answer needs.** The first 6 processes
  show how much the processes scatter. From that, it works out once how many
  processes every comparison needs for its pooled interval to be within ±2
  percentage points, or within ±10% of the difference itself, and runs that
  many, at most 40, always an even number so that both build orders count
  equally. It deliberately does not look at the interval after every process
  and stop as soon as it is narrow enough: that stops preferentially when the
  processes happened to agree, and such intervals covered the truth only 92
  to 94% of the time instead of 95%. Six were enough for data in the cache;
  far out of it, runs have needed 13 to 40, and where processes scattered by
  10 points it would have taken over 100.

Keep the machine awake while this runs: a laptop that goes to sleep pauses the
measurement for as long as it sleeps. rtcompare notices that
(`Report.Suspended`) but cannot give you the time back.

### Serial or parallel

Far out of cache, 40 processes may still not be enough, and a process of a
realistic suite can take a minute or two, so running more of them one after
another quickly stops being practical. `Options.Parallel` runs several at the
same time, in waves:

```go
multiproc.Main(multiproc.Options{Parallel: 12}, pair) // 12 children at a time
```

Why that is worth it: the uncertainty of the pooled result is roughly
σ²_between/P + σ²_within/(P·R), with P processes of R samples each. Out of
cache the scatter between processes dominates by far, so only more processes
help, and running them at the same time buys P per hour many times over. The
default budget of a parallel run is 10 waves, 120 processes at
`Parallel: 12`, for about the time of 10 serial processes.

But it measures something else. The children share the last-level cache, the
memory bandwidth and the clock headroom, so each gets a fraction of the cache
it would have alone, and data falls out of cache at smaller sizes. That is
closer to a program with neighbours in production, and A and B are still
compared fairly, since both run interleaved within each process. It is not
the same as the serial result, though:

- **Never mix the two regimes** in one pooled result or one comparison.
  `Results.Parallel` says which one ran; state it in your reports.
- **Stay at or below the number of physical cores.** Two children on the two
  hardware threads (SMT/Hyper-Threading) of one core share its L1 and L2 cache
  and disturb each other far more than neighbours on other cores do.
- Each child gets `GOMAXPROCS` = the number of CPUs divided by `Parallel`
  (at least 2), unless you set `GOMAXPROCS` yourself, so that one child's
  garbage collector cannot steal cores from its neighbours.
- The children's output is written line by line with a `[p07] ` prefix, so
  that concurrent children don't garble each other's lines.

For anything the pairs don't cover, `multiproc.Run` takes a suite function of
your own, and still perturbs the heap and pools for you; alternate the build
order by `p.Index` yourself there. If you run the processes by some other
means, `rtcompare.Combine(reports, 0)` does the pooling part on its own.

### Reading a pooled result

`Combine` treats each process as one number, its delta, and puts a Student t
interval around their mean. With few processes that interval is wide, and it
should be: five observations are five observations. Next to it you get:

- **`Inflation`** — how many times more the processes scatter than one
  process's interval implies. Near 1, a single process would have told you the
  truth. Well above 1, it would not have, and the pooled result is the one to
  quote.
- **`I2`** — the share of the scatter that the per-process intervals do not
  explain. Above about 0.5, the differences between processes dominate.
- **`NoiseFloor` and `Bias`** — the pooled difference has to clear a floor
  too, but a different one from a single process's. A single process's floor
  is a high percentile of what identical code reported, because its interval
  cannot see run-to-run noise. The pooled interval already includes
  everything that differs between processes, so the only thing left for the
  floor to catch is an error that repeats in *every* process: a systematic
  bias of the harness, which the A/A validations measure as `Bias`. That
  makes the pooled floor far lower than any single process's, which is
  exactly why pooling can resolve differences smaller than one process ever
  could.
- **Warnings** — among them, when processes resolved the difference with
  opposite signs, each of them confident, and when the A/A validations found
  a systematic bias.

## Troubleshooting: what to do, when

This is the part of the "long protocol" that's normally invisible — the
judgment calls between steps. Here they are as an explicit table. Everything
below is available on a `Report` from `Compare`, or by calling the named
function yourself.

### The tie rate is high

**Symptom:** `HarnessValidation.TieRate` (or a warning on the `Report`
mentioning "bootstrap replicates tied") is above a few percent.

**What it means:** two measurements are landing on *exactly* the same
number often enough to matter. This happens when your batches are too short
relative to the clock's granularity — the measurement is rounding several
genuinely different durations onto the same value.

**What to do:** make the batches longer, by lowering
`CollectOptions.MaxQuantizationError` (it defaults to 0.001, i.e. 0.1%; try
0.0001). This asks the calibration step to run each batch about ten times
longer, which spreads the same clock-tick error over ten times as many
repetitions. **Do not** try to fix this by increasing `Repeats` — that draws
more samples from the same coarse set of possible values and doesn't change
the granularity at all. Only a longer batch does.

### The noise floor is high, or higher than expected

**Symptom:** `HarnessValidation.NoiseFloor` (or `Report.NoiseFloor`) is a
percent or more, when you expected it to be a small fraction of a percent.

**What it means:** your machine is disturbing the measurement more than
usual. Common causes: running on battery power (power-saving throttles the
CPU unpredictably), other applications competing for the CPU or memory
bandwidth, thermal throttling on a machine that's been under load for a
while, or a candidate that allocates memory and triggers garbage collection
mid-batch.

**What to do:**
- Plug in, close other applications, and let the machine cool down or idle
  briefly before running.
- If one candidate allocates and the other doesn't, try `GCBetween: true` in
  `CollectOptions` — it forces a garbage collection between batches instead
  of letting one land unpredictably in the middle of a timed region.
  Pairing it with `DisableGC: true` gives fully deterministic collection
  points, though see that option's documentation for the trade-off (it
  under-counts collector overhead, which matters if you care about
  real-world behavior rather than a clean comparison).
- If none of that helps, the noise floor is telling you the truth about this
  machine right now — treat any difference smaller than it as unresolved and
  move on, or find a quieter machine (a dedicated benchmark server, a CI
  runner with less contention) if this comparison matters enough.

### A warning says the candidates had not reached a steady state

**Symptom:** `Report.Warnings` says that "the ratio B/A shifted" during the
run.

**What it means:** one candidate was ahead at the start of the run and lost
that advantage as it went on, usually because it started with the caches full
of its own data. The median of the run then depends on how long the run was,
and the difference it reports is partly an artefact. See
[Whatever ran last starts warm](#whatever-ran-last-starts-warm).

**What to do:** raise `CollectOptions.WarmupDuration`, for example to one or
two seconds, and check that nothing runs one candidate alone between your
setup and the comparison. Then swap the roles of the two candidates and see
whether the result keeps its sign.

### A drift warning appears

**Symptom:** `DriftReport.Drifted(...)` returns true, or `Report.Warnings`
mentions a candidate that "drifted during the run."

This warning only appears when the trend is both significant and larger than
what the result can resolve (the noise floor, or half the interval, whichever
is larger). A long run finds shifts of a tenth of a percent significant, and a
warning that fires on nearly every run tells you nothing.

**What it means:** the machine changed behavior over the course of the run —
usually getting slower, most often from thermal throttling as sustained load
heats up the CPU. Because measurement order is interleaved by default (ABBA),
a drift usually doesn't bias *which* candidate looks faster — both are
equally exposed to it — but it does mean your measurements are less
independent than the statistics assume, which widens the real uncertainty
beyond what the reported interval shows.

**What to do:**
- Let the machine idle and cool before running, especially on a laptop.
- Consider shorter runs (fewer `Repeats`) if the drift is thermal — a shorter
  run gives the CPU less time to heat up in the first place.
- If drift keeps appearing on a machine you use often for this, treat every
  result from it with a bit more skepticism than the headline confidence
  suggests.

### A warning says the machine was suspended

**Symptom:** `Report.Suspended` is non-zero, or a warning says the machine
"appears to have been suspended."

**What it means:** the wall clock moved further than the monotonic clock
during the comparison, which on Linux and macOS happens when the machine
sleeps. The samples straddle a pause, possibly a long one, after which caches,
clock speeds and everything else started cold.

**What to do:** repeat the comparison with the machine kept awake — plugged
in, and with `caffeinate -i` on macOS, `systemd-inhibit --what=idle:sleep` on
Linux, or the power settings on Windows.

### The autocorrelation is high

**Symptom:** `HarnessValidation.Autocorrelation` (or `Report.Autocorrelation`)
is above about 0.2, or the report mentions resampling "in blocks."

**What it means:** consecutive measurements resemble each other more than
pure chance would produce — see Step 5, above. This is usually a symptom of
the same causes as a high noise floor (something intermittently loading the
machine), just showing up as a pattern between neighbors rather than as pure
scatter.

**What to do:** nothing, usually — `Compare` detects this and switches to
block resampling automatically, which repairs most of the resulting
overconfidence. If you're calling `BootstrapConfidence`
directly rather than through `Compare`, switch to
`BlockBootstrapConfidence` yourself once this crosses roughly 0.2. If the
value is very high (0.4 and up), block resampling only partly repairs the
problem — that's a sign to also address whatever is disturbing the
machine, using the same steps as for a high noise floor.

One thing worth knowing if you've set `SkipValidation: true`: this decision is
then made from a single, noisier read of the correlation, taken from the
comparison run itself rather than from the several separate A/A experiments
`ValidateHarness` would otherwise average over. With validation left on (the
default), the number `Compare` acts on is the median across those experiments
— a steadier estimate of a property of your machine and setup, not a one-off
reading of whatever happened to be going on during this particular run. That's
one more reason `SkipValidation` trades away more than just the noise floor.

**Why doesn't interleaving the order (ABBA, see Step 3) already fix this?**
It's a fair question, and the answer is that ABBA and blocks solve two
different problems. ABBA cancels a trend's effect on *which candidate looks
faster* — if the machine is slowly getting hotter, both candidates are
measured, on average, at the same points in that slowdown, so it washes out of
the comparison between them. Autocorrelation is a property of *one candidate's
own sequence of measurements* — whether its 51st batch resembles its 50th more
than chance would suggest — and that says nothing about bias between A and B.
It says something about how much independent information those 51
measurements actually contain. Resampling one at a time, as the plain
bootstrap does, assumes each of them is a fresh, independent look; strong
autocorrelation means they're not, and no amount of interleaving the order
between A and B changes that fact about A's own numbers. This has been
measured directly: on data manufactured to have no real difference at all,
the plain bootstrap reported a "difference" in 21.7% of runs at a lag-1
correlation of 0.4, against a target of 10% — interleaving the order was
already in effect and did not prevent it.

### The confidence interval is very wide

**Symptom:** `Estimate.Low` and `Estimate.High` are far apart — say, "A is
somewhere between 5% and 45% faster."

**What it means:** either you don't have enough measurements to pin the
number down precisely, or the measurements themselves are highly variable
(which usually traces back to one of the machine-noise issues above).

**What to do:**
- Increase `CollectOptions.Repeats` (more measurements narrows the interval,
  up to a point — it helps with random scatter, not with a systematic
  problem like drift or a high noise floor).
- Increase `Resamples` if the interval's *edges* seem to jump around between
  otherwise-identical runs — that's Monte Carlo noise in the resampling
  itself, and it settles down with more resamples (5,000 is the default;
  10,000+ helps when you specifically care about the tails).
- If the interval is wide because the underlying noise floor is high, fix
  that first — more repeats can't out-run a genuinely noisy machine.

### Calibration fails ("could not reach target batch duration")

**Symptom:** an error from `CalibrateInnerLoops` or `Collect`, saying it
couldn't get the batch long enough even at the maximum allowed number of
repetitions.

**What it means:** your `Batch` function almost certainly isn't doing `n`
times the work — most often because it ignores `n` entirely (a copy-paste
bug where the loop uses a fixed count instead of `range n`), or because the
compiler managed to fold the whole computation to a constant (see the
`sink +=` advice above). This is *not* usually a sign that your code is "too
fast to measure" — Go does not delete a loop merely because nothing reads
its result, so a genuinely-too-fast operation is rare in practice.

**What to do:** check that `Batch` actually loops `n` times and that its
result depends on something the compiler can't precompute (real input data,
not a hardcoded constant), and check that the result is fed into a
package-level sink variable.

## A short glossary

- **Batch** — one call to your `Batch` function, running your code `n` times
  in a row so the whole thing can be timed together.
- **Median** — the middle value of a sorted list of numbers. rtcompare uses
  this instead of the average because a single wildly slow batch (the machine
  hiccuped) barely moves the median but can swing an average a lot — see
  `CompareSamples`'s documentation ("Why the median") for the evidence behind
  that choice, including where it stops being the right pick.
- **Bootstrap / resampling** — repeatedly reshuffling your actual
  measurements (with repeats allowed) to see how much an answer computed from
  them would wobble if you'd happened to draw a slightly different sample.
  Wobbles a little → trust the answer. Wobbles a lot → don't, yet.
- **Confidence** — in this library, always "the fraction of resampled
  reshuffles in which the stated threshold held." Not a probability that a
  hypothesis is true in some absolute sense — a statement about how
  consistently your specific data supports a specific claim.
- **Noise floor** — what this exact setup, on this exact machine, reports as
  a "difference" between two runs of the *same* code. The yardstick every
  real result should be measured against.
- **Tie rate** — how often two resampled measurements come out exactly equal.
  High values mean the measurement is too coarse (see
  [above](#the-tie-rate-is-high)).
- **Drift** — a trend across a run, usually the machine slowing down (or
  speeding up) as the run progresses.
- **Autocorrelation** — how much each measurement resembles the one right
  before it. High values mean measurements aren't fully independent of one
  another.
- **Layout effect** — the part of a measured difference that comes from where
  the data happens to lie in memory in this particular process, not from the
  code. It is fixed for the life of a process and changes with the next one;
  see [One process is one observation](#one-process-is-one-observation).
- **Difference and ratio** — rtcompare's difference is 1 − A/B: how much
  less time A needs, as a share of B's time. `Compare` takes it from the
  batches of A and B measured next to each other, as the median of their
  ratios, so that whatever disturbed both batches of a pair cancels out. It is not symmetric: if B takes
  twice as long as A, the difference is +50%, but with the roles swapped it
  is −100%. The report therefore also prints the ratio B/A (2.000× in that
  example, 0.500× swapped), which reads the same either way round.
- **Attenuation** — the true difference between two pieces of code getting
  diluted in the measured number because of fixed overhead (a loop, an
  accumulator) that both candidates carry equally. See
  [above](#attenuation-your-number-is-real-but-smaller-than-the-truth).
- **Quantization / quantization error** — the rounding introduced by the
  system clock only being able to tell time in discrete ticks, rather than
  continuously.
