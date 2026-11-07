package eventlog

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-gost/core/logger"
	xlogger "github.com/go-gost/x/logger"
)

// captureLogger is a logger.Logger that records Warn lines.
type captureLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *captureLogger) WithFields(map[string]any) logger.Logger { return l }
func (l *captureLogger) Trace(args ...any)                       {}
func (l *captureLogger) Tracef(string, ...any)                   {}
func (l *captureLogger) Debug(args ...any)                       {}
func (l *captureLogger) Debugf(string, ...any)                   {}
func (l *captureLogger) Info(args ...any)                        {}
func (l *captureLogger) Infof(string, ...any)                    {}
func (l *captureLogger) Warn(args ...any) {
	l.add(fmt.Sprint(args...))
}
func (l *captureLogger) Warnf(format string, args ...any) { l.add(fmt.Sprintf(format, args...)) }
func (l *captureLogger) Error(args ...any)                {}
func (l *captureLogger) Errorf(string, ...any)            {}
func (l *captureLogger) Fatal(args ...any)                {}
func (l *captureLogger) Fatalf(string, ...any)            {}
func (l *captureLogger) GetLevel() logger.LogLevel        { return logger.InfoLevel }
func (l *captureLogger) IsLevelEnabled(logger.LogLevel) bool {
	return true
}

func (l *captureLogger) add(s string) {
	l.mu.Lock()
	l.lines = append(l.lines, s)
	l.mu.Unlock()
}

func (l *captureLogger) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

func TestWindowAggregation(t *testing.T) {
	cl := &captureLogger{}
	a := New(cl, 40*time.Millisecond)
	c := a.Counter("test: %d event(s), %d total")

	for i := 0; i < 100; i++ {
		c.Note()
	}
	if lines := cl.Lines(); len(lines) != 0 {
		t.Fatalf("within window: got %d log lines, want 0: %v", len(lines), lines)
	}
	if got := c.Total(); got != 100 {
		t.Fatalf("total = %d, want 100", got)
	}

	// Let the window elapse; the next event flushes the summary,
	// including itself in the window count.
	time.Sleep(60 * time.Millisecond)
	c.Note()
	lines := cl.Lines()
	if len(lines) != 1 {
		t.Fatalf("after window: got %d log lines, want 1: %v", len(lines), lines)
	}
	if want := "test: 101 event(s), 101 total"; lines[0] != want {
		t.Fatalf("summary = %q, want %q", lines[0], want)
	}
	if got := c.Pending(); got != 0 {
		t.Fatalf("pending after flush = %d, want 0", got)
	}

	// Nothing new: an explicit Flush emits nothing more.
	a.Flush()
	if lines := cl.Lines(); len(lines) != 1 {
		t.Fatalf("idle flush: got %d log lines, want 1: %v", len(lines), lines)
	}
}

func TestDetailCounterKeepsLastAndTotal(t *testing.T) {
	cl := &captureLogger{}
	a := New(cl, time.Minute)
	c := a.DetailCounter("test: %d failure(s), last: %s, %d total")

	c.NoteErr(errors.New("first failure"))
	c.NoteErr(errors.New("second failure"))
	c.NoteDetail("third sample")

	if lines := cl.Lines(); len(lines) != 0 {
		t.Fatalf("within window: got %d log lines, want 0", len(lines))
	}

	// The close-time flush must surface the accumulated counts and
	// the most recent detail: single, non-flood errors are delayed
	// by at most the window, never swallowed.
	a.Flush()
	lines := cl.Lines()
	if len(lines) != 1 {
		t.Fatalf("flush: got %d log lines, want 1: %v", len(lines), lines)
	}
	if want := "test: 3 failure(s), last: third sample, 3 total"; lines[0] != want {
		t.Fatalf("summary = %q, want %q", lines[0], want)
	}
}

func TestMultipleCountersShareWindow(t *testing.T) {
	cl := &captureLogger{}
	a := New(cl, time.Minute)
	c1 := a.Counter("one: %d event(s), %d total")
	c2 := a.Counter("two: %d event(s), %d total")

	c1.Note()
	c1.Note()
	c2.Note()
	a.Flush()

	lines := cl.Lines()
	if len(lines) != 2 {
		t.Fatalf("flush: got %d log lines, want 2: %v", len(lines), lines)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "one: 2 event(s), 2 total") ||
		!strings.Contains(joined, "two: 1 event(s), 1 total") {
		t.Fatalf("unexpected summaries: %v", lines)
	}
}

func TestNilSafety(t *testing.T) {
	// Nil logger: counts advance, summaries drop, nothing panics.
	a := New(nil, time.Minute)
	c := a.Counter("test: %d event(s), %d total")
	d := a.DetailCounter("test: %d failure(s), last: %s, %d total")
	for i := 0; i < 5; i++ {
		c.Note()
		d.NoteErr(errors.New("boom"))
	}
	a.Flush()
	if got := c.Total(); got != 5 {
		t.Fatalf("nil-logger total = %d, want 5", got)
	}
	if got := c.Pending(); got != 0 {
		t.Fatalf("nil-logger pending after flush = %d, want 0", got)
	}

	// Nil aggregator / counter: all calls are no-ops.
	var na *Aggregator
	na.Flush()
	nc := na.Counter("test: %d event(s), %d total")
	nc.Note()
	nc.NoteDetail("x")
	nc.NoteErr(errors.New("boom"))
	if got := nc.Total(); got != 0 {
		t.Fatalf("nil counter total = %d, want 0", got)
	}
}

func TestConcurrentNotes(t *testing.T) {
	cl := &captureLogger{}
	a := New(cl, time.Millisecond)
	c := a.DetailCounter("test: %d failure(s), last: %s, %d total")

	const workers = 8
	const perWorker = 1000
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			err := fmt.Errorf("worker %d failure", w)
			for i := 0; i < perWorker; i++ {
				c.NoteErr(err)
			}
		}(w)
	}
	wg.Wait()
	a.Flush()

	if got, want := c.Total(), uint64(workers*perWorker); got != want {
		t.Fatalf("total = %d, want %d", got, want)
	}
	if got := c.Pending(); got != 0 {
		t.Fatalf("pending after final flush = %d, want 0", got)
	}
	// 8000 events inside ~1ms windows must collapse to a handful
	// of summary lines, never one line per event.
	if lines := cl.Lines(); len(lines) == 0 || len(lines) > 100 {
		t.Fatalf("got %d summary lines for %d events", len(lines), workers*perWorker)
	}
}

func BenchmarkCounterNote(b *testing.B) {
	a := New(xlogger.Nop(), time.Hour) // window never elapses: pure hot path
	c := a.Counter("bench: %d event(s), %d total")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Note()
	}
}

func BenchmarkCounterNoteDetail(b *testing.B) {
	a := New(xlogger.Nop(), time.Hour)
	c := a.DetailCounter("bench: %d failure(s), last: %s, %d total")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.NoteDetail("203.0.113.7:51000")
	}
}

// discardLogger formats every call and writes it to io.Discard:
// the floor cost of the pre-aggregation per-event Warnf (a real
// file logger costs strictly more). A nop logger is not a valid
// baseline: its empty Warnf is inlined away by the compiler.
type discardLogger struct{}

func (l *discardLogger) WithFields(map[string]any) logger.Logger { return l }
func (l *discardLogger) Trace(args ...any)                       {}
func (l *discardLogger) Tracef(f string, a ...any)               { fmt.Fprintf(io.Discard, f, a...) }
func (l *discardLogger) Debug(args ...any)                       {}
func (l *discardLogger) Debugf(f string, a ...any)               { fmt.Fprintf(io.Discard, f, a...) }
func (l *discardLogger) Info(args ...any)                        {}
func (l *discardLogger) Infof(f string, a ...any)                { fmt.Fprintf(io.Discard, f, a...) }
func (l *discardLogger) Warn(args ...any)                        {}
func (l *discardLogger) Warnf(f string, a ...any)                { fmt.Fprintf(io.Discard, f, a...) }
func (l *discardLogger) Error(args ...any)                       {}
func (l *discardLogger) Errorf(f string, a ...any)               { fmt.Fprintf(io.Discard, f, a...) }
func (l *discardLogger) Fatal(args ...any)                       {}
func (l *discardLogger) Fatalf(f string, a ...any)               { fmt.Fprintf(io.Discard, f, a...) }
func (l *discardLogger) GetLevel() logger.LogLevel               { return logger.InfoLevel }
func (l *discardLogger) IsLevelEnabled(logger.LogLevel) bool     { return true }

// BenchmarkLoggerWarnfBaseline is the pre-aggregation cost of one
// event on the same points: a single Warnf through a formatting,
// discarding logger.
func BenchmarkLoggerWarnfBaseline(b *testing.B) {
	l := &discardLogger{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.Warnf("connection queue is full, client %s discarded", "203.0.113.7:51000")
	}
}
