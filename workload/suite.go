package workload

import (
	"fmt"

	"github.com/TomTonic/rtcompare/multiproc"
)

// SteadyStateSuffix and BuildSuffix are appended to the name given to [Suite]
// to name the two comparisons it records in each process.
const (
	SteadyStateSuffix = "/steady state"
	BuildSuffix       = "/build"
)

// Suite returns a suite for the multiproc package that runs [Compare] once in
// each process and records both of its answers, so that they are pooled across
// processes.
//
// Parameters: name identifies the comparison; the steady-state report is
// recorded as name+[SteadyStateSuffix] and the build report as
// name+[BuildSuffix], the latter only unless opt.SkipBuild is set. target, a,
// b and opt are those of Compare.
//
// Use it whenever the structures hold more than a few megabytes or are full of
// pointers, which for the sizes this package is meant for is the usual case.
// There, a single process's result is one observation of where its memory
// happened to lie, and its interval is several times too narrow; see
// [rtcompare.Combine]. multiproc starts the processes, perturbs each one's
// heap and pools the reports; Suite additionally alternates which structure's
// build comes second between processes, A in even ones and B in odd ones, so
// that the small head start of the one built last averages out.
//
// The suite returns Compare's error, naming the comparison, if a structure
// lacks New or Apply, the configuration is invalid or a comparison fails.
// Invalid structures are therefore reported by the first child process.
//
//	func main() {
//		multiproc.MainSuite(multiproc.Options{},
//			workload.Suite("map vs Set3", 1_000_000, goMap, set3Set, workload.Options{}))
//	}
func Suite[SA, SB any](name string, target int, a Structure[SA], b Structure[SB], opt Options) func(*multiproc.Process) error {
	return func(p *multiproc.Process) error {
		res, err := compare(target, a, b, opt, p.Index%2 == 1)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		p.Record(name+SteadyStateSuffix, res.SteadyState)
		if !opt.SkipBuild {
			p.Record(name+BuildSuffix, res.Build)
		}
		return nil
	}
}
