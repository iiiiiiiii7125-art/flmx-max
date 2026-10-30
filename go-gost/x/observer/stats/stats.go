package stats

import (
	"sync/atomic"

	"github.com/go-gost/core/observer"
	"github.com/go-gost/core/observer/stats"
)

type Stats struct {
	updated      atomic.Bool
	totalConns   atomic.Uint64
	currentConns atomic.Uint64
	inputBytes   atomic.Uint64
	outputBytes  atomic.Uint64
	totalErrs    atomic.Uint64
	resetTraffic bool
}

func NewStats(resetTraffic bool) stats.Stats {
	return &Stats{
		resetTraffic: resetTraffic,
	}
}

func (s *Stats) Add(kind stats.Kind, n int64) {
	if s == nil {
		return
	}
	switch kind {
	case stats.KindTotalConns:
		if n > 0 {
			s.totalConns.Add(uint64(n))
		}
	case stats.KindCurrentConns:
		s.currentConns.Add(uint64(n))
	case stats.KindInputBytes:
		s.inputBytes.Add(uint64(n))
	case stats.KindOutputBytes:
		s.outputBytes.Add(uint64(n))
	case stats.KindTotalErrs:
		if n > 0 {
			s.totalErrs.Add(uint64(n))
		}
	}
	s.updated.Store(true)
}

func (s *Stats) Get(kind stats.Kind) uint64 {
	if s == nil {
		return 0
	}

	switch kind {
	case stats.KindTotalConns:
		return s.totalConns.Load()
	case stats.KindCurrentConns:
		return s.currentConns.Load()
	case stats.KindInputBytes:
		return s.inputBytes.Load() // 只获取，不自动清零
	case stats.KindOutputBytes:
		return s.outputBytes.Load() // 只获取，不自动清零
	case stats.KindTotalErrs:
		return s.totalErrs.Load()
	}
	return 0
}

// ResetTraffic deducts the just-reported snapshot amounts from the
// traffic counters. The parameters are the snapshot values that were
// taken BEFORE reporting (the amounts to subtract), not the values
// to store.
//
// A02-004: the old contract made the caller compute the remainder
// (Get - snapshot) and Store it here. That read-modify-write is not
// atomic: bytes the data plane Adds between the caller's Get and
// this Store were silently overwritten and lost, once per observe
// period on busy services. Subtracting the snapshot atomically
// (uint64 wraparound arithmetic on the atomic counters) leaves
// concurrently added bytes untouched.
func (s *Stats) ResetTraffic(reportedInputBytes, reportedOutputBytes uint64) {
	if s == nil {
		return
	}
	s.inputBytes.Add(-reportedInputBytes)
	s.outputBytes.Add(-reportedOutputBytes)
}

func (s *Stats) Reset() {
	s.updated.Store(false)
	s.totalConns.Store(0)
	s.currentConns.Store(0)
	s.inputBytes.Store(0)
	s.outputBytes.Store(0)
	s.totalErrs.Store(0)
}

func (s *Stats) IsUpdated() bool {
	return s.updated.Swap(false)
}

type StatsEvent struct {
	Kind    string
	Service string
	Client  string

	TotalConns   uint64
	CurrentConns uint64
	InputBytes   uint64
	OutputBytes  uint64
	TotalErrs    uint64
}

func (StatsEvent) Type() observer.EventType {
	return observer.EventStats
}
