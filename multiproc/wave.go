package multiproc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
)

// waveRunner starts the processes of one wave together and collects their
// records in index order. A serial run is a sequence of waves of one.
type waveRunner struct {
	opt        Options
	dir        string
	env        []string // added to every child's environment
	gomaxprocs int      // what env sets GOMAXPROCS to, or what the caller set; zero if unset
	width      int      // digits of the largest process index, for output prefixes
	mu         sync.Mutex
}

func newWaveRunner(opt Options, dir string) *waveRunner {
	w := &waveRunner{opt: opt, dir: dir, width: max(2, len(strconv.Itoa(opt.MaxProcesses-1)))}
	if v := os.Getenv("GOMAXPROCS"); v != "" {
		// The caller's choice wins, and is inherited through the environment.
		w.gomaxprocs, _ = strconv.Atoi(v)
	} else if opt.Parallel > 1 {
		w.gomaxprocs = childGOMAXPROCS(runtime.NumCPU(), opt.Parallel)
		w.env = []string{"GOMAXPROCS=" + strconv.Itoa(w.gomaxprocs)}
	}
	return w
}

// childGOMAXPROCS shares the machine's CPUs among the children of a wave.
// Without it every child's runtime would start one P per CPU and run its
// garbage collector on a quarter of them, taking cores from its neighbours in
// the middle of their measurements. Two is the floor, so that a child's own
// collector still has a P besides the measuring goroutine.
func childGOMAXPROCS(cpus, parallel int) int {
	return max(2, cpus/parallel)
}

// run starts processes start to start+n-1 at the same time and returns their
// records in index order. If one fails, the others are killed, and the error
// of the lowest failing index is returned.
func (w *waveRunner) run(start, n int) ([][]record, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recs := make([][]record, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for k := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			i := start + k
			stdout, stderr, flush := w.writers(i)
			recs[k], errs[k] = runProcess(ctx, w.opt, filepath.Join(w.dir, strconv.Itoa(i)+".json"), i, stdout, stderr, w.env)
			flush()
			if errs[k] != nil {
				cancel()
			}
		}()
	}
	wg.Wait()
	var canceled error
	for _, err := range errs {
		switch {
		case err == nil:
		case errors.Is(err, errCanceled):
			canceled = err
		default:
			return nil, err
		}
	}
	if canceled != nil {
		return nil, canceled
	}
	return recs, nil
}

// writers returns the output a child writes to. A serial run passes the
// caller's writers through unchanged; a parallel run gives each child writers
// that emit whole lines prefixed with its index, under a lock shared by the
// wave, so that concurrent children cannot interleave within a line.
func (w *waveRunner) writers(i int) (stdout, stderr io.Writer, flush func()) {
	if w.opt.Parallel <= 1 {
		return w.opt.Stdout, w.opt.Stderr, func() {}
	}
	prefix := fmt.Sprintf("[p%0*d] ", w.width, i)
	out := w.lineWriter(w.opt.Stdout, prefix)
	errw := w.lineWriter(w.opt.Stderr, prefix)
	return out, errw, func() {
		for _, lw := range []io.Writer{out, errw} {
			if l, ok := lw.(*lineWriter); ok {
				l.flush()
			}
		}
	}
}

// lineWriter wraps a caller's writer for one child, or passes io.Discard
// through, where there is nothing to protect and the copying would only cost.
func (w *waveRunner) lineWriter(dst io.Writer, prefix string) io.Writer {
	if dst == io.Discard {
		return dst
	}
	return &lineWriter{mu: &w.mu, w: dst, prefix: prefix}
}

// lineWriter buffers a child's output and writes it to a shared writer in
// whole lines, each with the child's prefix.
type lineWriter struct {
	mu     *sync.Mutex
	w      io.Writer
	prefix string
	buf    []byte
}

// Write implements io.Writer. It always reports the whole input as written:
// the child's output is a courtesy, and a failing terminal is no reason to
// fail the child.
func (l *lineWriter) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		end := bytes.IndexByte(l.buf, '\n')
		if end < 0 {
			return len(p), nil
		}
		l.emit(l.buf[:end+1])
		l.buf = l.buf[end+1:]
	}
}

func (l *lineWriter) emit(line []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.w, l.prefix)
	_, _ = l.w.Write(line)
}

// flush writes a last line that did not end in a newline, completing it.
func (l *lineWriter) flush() {
	if len(l.buf) > 0 {
		l.emit(append(l.buf, '\n'))
		l.buf = nil
	}
}
