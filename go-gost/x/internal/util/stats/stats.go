package stats

import (
	"sync"

	"github.com/go-gost/core/observer"
	"github.com/go-gost/core/observer/stats"
	xstats "github.com/go-gost/x/observer/stats"
)

// statsEntry wraps one client's counters with an idle-cycle count used
// for eviction (P1-07): an entry is dropped once it has produced no
// update for statsEvictIdleCycles consecutive Events sweeps *and* has
// no live connections. Such an entry contributes nothing to Events
// output (non-updated entries are skipped there), so eviction does not
// change what observers see; a returning client simply starts a fresh
// counter set, exactly as it would after a service restart.
type statsEntry struct {
	st         stats.Stats
	idleCycles int
}

const statsEvictIdleCycles = 2

type HandlerStats struct {
	service      string
	stats        map[string]*statsEntry
	resetTraffic bool
	mu           sync.RWMutex
}

func NewHandlerStats(service string, resetTraffic bool) *HandlerStats {
	return &HandlerStats{
		service:      service,
		stats:        make(map[string]*statsEntry),
		resetTraffic: resetTraffic,
	}
}

func (p *HandlerStats) Stats(client string) stats.Stats {
	p.mu.RLock()
	entry := p.stats[client]
	p.mu.RUnlock()
	if entry != nil {
		return entry.st
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	entry = p.stats[client]
	if entry == nil {
		entry = &statsEntry{st: xstats.NewStats(p.resetTraffic)}
		p.stats[client] = entry
	}

	return entry.st
}

func (p *HandlerStats) Events() (events []observer.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for k, entry := range p.stats {
		if !entry.st.IsUpdated() {
			entry.idleCycles++
			if entry.idleCycles >= statsEvictIdleCycles &&
				entry.st.Get(stats.KindCurrentConns) == 0 {
				delete(p.stats, k)
			}
			continue
		}
		entry.idleCycles = 0
		v := entry.st
		events = append(events, xstats.StatsEvent{
			Kind:         "handler",
			Service:      p.service,
			Client:       k,
			TotalConns:   v.Get(stats.KindTotalConns),
			CurrentConns: v.Get(stats.KindCurrentConns),
			InputBytes:   v.Get(stats.KindInputBytes),
			OutputBytes:  v.Get(stats.KindOutputBytes),
			TotalErrs:    v.Get(stats.KindTotalErrs),
		})
	}
	return
}
