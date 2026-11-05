package selector

import (
	"context"
	"time"

	"github.com/go-gost/core/chain"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/core/metadata"
	"github.com/go-gost/core/selector"
	mdutil "github.com/go-gost/x/metadata/util"
)

type failFilter[T any] struct {
	maxFails    int
	failTimeout time.Duration
}

// FailFilter filters the dead objects.
// An object is marked as dead if its failed count is greater than MaxFails.
func FailFilter[T any](maxFails int, timeout time.Duration) selector.Filter[T] {
	return &failFilter[T]{
		maxFails:    maxFails,
		failTimeout: timeout,
	}
}

// Filter filters dead objects.
// For single-node case, skip filtering to ensure availability (matches upstream).
// For multi-node case, filter out failed nodes to enable failover.
func (f *failFilter[T]) Filter(ctx context.Context, vs ...T) []T {
	if len(vs) <= 1 {
		return vs
	}
	var l []T
	for _, v := range vs {
		maxFails := f.maxFails
		failTimeout := f.failTimeout
		if mi, _ := any(v).(metadata.Metadatable); mi != nil {
			if md := mi.Metadata(); md != nil {
				if md.IsExists(labelMaxFails) {
					maxFails = mdutil.GetInt(md, labelMaxFails)
				}
				if md.IsExists(labelFailTimeout) {
					failTimeout = mdutil.GetDuration(md, labelFailTimeout)
				}
			}
		}
		if maxFails <= 0 {
			maxFails = 1
		}
		if failTimeout <= 0 {
			failTimeout = DefaultFailTimeout
		}

		if mi, _ := any(v).(selector.Markable); mi != nil {
			if marker := mi.Marker(); marker != nil {
				count := marker.Count()
				timeSince := time.Since(marker.Time())
				passed := count < int64(maxFails) || timeSince >= failTimeout

				// Debug logging for failover analysis. This must go
				// through the structured logger at Debug level: this
				// filter runs for every node of every selection (the
				// per-connection hot path), and an unconditional
				// fmt.Printf to stdout bypassed level control and
				// redaction and amplified journal writes (D3-06).
				nodeName := "unknown"
				nodeAddr := "unknown"
				if node, ok := any(v).(*chain.Node); ok {
					nodeName = node.Name
					nodeAddr = node.Addr
				}
				// logger.Default() is nil until the host process
				// installs one, so guard before use.
				if log := logger.Default(); log != nil {
					log.Debugf("[FailFilter] node=%s addr=%s count=%d maxFails=%d timeSince=%v failTimeout=%v passed=%v",
						nodeName, nodeAddr, count, maxFails, timeSince, failTimeout, passed)
				}

				if passed {
					l = append(l, v)
				}
				continue
			}
		}
		l = append(l, v)
	}
	return l
}

type backupFilter[T any] struct{}

// BackupFilter filters the backup objects.
// An object is marked as backup if its metadata has backup flag.
func BackupFilter[T any]() selector.Filter[T] {
	return &backupFilter[T]{}
}

// Filter filters backup objects.
func (f *backupFilter[T]) Filter(ctx context.Context, vs ...T) []T {
	if len(vs) <= 1 {
		return vs
	}

	var l, backups []T
	for _, v := range vs {
		if mi, _ := any(v).(metadata.Metadatable); mi != nil {
			if mdutil.GetBool(mi.Metadata(), labelBackup) {
				backups = append(backups, v)
				continue
			}
		}
		l = append(l, v)
	}

	if len(l) == 0 {
		return backups
	}
	return l
}
