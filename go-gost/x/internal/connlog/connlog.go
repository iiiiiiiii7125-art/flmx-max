// Package connlog holds the agent-side view of the panel's inbound
// connection-log entry marks (design doc feat-inbound-connlog §2,
// §5.3, §6 unit U7).
//
// The panel — never the agent — decides which of a node's services
// and kernel inbounds are connection-log entries: the points where
// an end client actually enters this node. A gost forwarding service
// that serves as a managed relay entrance carries the mark in its
// service metadata ("connlogEntryKind"/"connlogEntryID"); a kernel
// inbound that is itself the entry (direct or chain-source) is
// listed in the KernelApply payload's "connlogEntries". Everything
// else — ordinary user forwards, tunnel legs, relay landings,
// metering companions — is unmarked on purpose: collecting there
// would record a previous hop's address as if it were the client.
//
// Integration ruling (one mark form, consumed at the point of use,
// no shadow registry): the gost mark is parsed exactly once, by
// ParseServiceMark below, when the service is built
// (config/parsing/service) and travels with the service object — a
// topology change converges on the next service rebuild, with no
// snapshot that could go stale independently of the services
// actually running. The kernel entries ride each ApplyRequest and
// are intersected with the per-inbound opt-in list inside the
// kernel manager at Apply time. This package therefore holds only
// the shared mark vocabulary and the metadata parser; it records
// nothing itself.
package connlog

import (
	"strings"
	"sync"
)

// Entry kinds, identical to the panel's entry_kind column values.
const (
	KindForward = "forward"
	KindInbound = "inbound"
)

// Service metadata keys written by the panel. The lookup is
// case-insensitive on purpose, for the same reason as in
// internal/idle: a gost config file reaches the agent through viper,
// whose key handling lowercases map keys, so the panel-written
// "connlogEntryKind" may arrive as "connlogentrykind".
const (
	MetadataKindKey = "connlogEntryKind"
	MetadataIDKey   = "connlogEntryID"
)

// Mark is one entry designation: what kind of entry a service or
// kernel inbound is, and the entry_id its connection events carry
// (the gost service name for forwards, the kernel inbound tag
// "in-<ID>" for kernel inbounds).
type Mark struct {
	Kind string
	ID   string
}

// KernelEntry is one marked kernel inbound from a KernelApply
// payload.
type KernelEntry struct {
	InboundID int64
	Tag       string
}

var (
	mu            sync.RWMutex
	kernelEntries = map[string][]KernelEntry{}
)

// ParseServiceMark reads the entry mark out of one service's
// metadata. A mark is valid only with kind "forward"; the ID falls
// back to the service's own name when the panel value is absent or
// blank (the service name is the canonical entry_id for forwards).
func ParseServiceMark(serviceName string, md map[string]any) (Mark, bool) {
	kind, ok := metadataString(md, MetadataKindKey)
	if !ok || !strings.EqualFold(kind, KindForward) {
		return Mark{}, false
	}
	id, _ := metadataString(md, MetadataIDKey)
	if strings.TrimSpace(id) == "" {
		id = serviceName
	}
	if strings.TrimSpace(id) == "" {
		return Mark{}, false
	}
	return Mark{Kind: KindForward, ID: id}, true
}

func metadataString(md map[string]any, key string) (string, bool) {
	if md == nil {
		return "", false
	}
	if v, ok := md[key]; ok {
		s, ok := v.(string)
		return s, ok
	}
	for k, v := range md {
		if strings.EqualFold(k, key) {
			s, ok := v.(string)
			return s, ok
		}
	}
	return "", false
}

// PublishKernelEntries replaces the marked-inbound set of one
// kernel type with the entries of the configuration now in force.
// Publishing an empty (or nil) list clears the type's set.
func PublishKernelEntries(kernelType string, entries []KernelEntry) {
	next := make([]KernelEntry, 0, len(entries))
	for _, e := range entries {
		if strings.TrimSpace(e.Tag) == "" {
			continue
		}
		next = append(next, e)
	}
	mu.Lock()
	kernelEntries[kernelType] = next
	mu.Unlock()
}

// KernelEntries returns a copy of one kernel type's marked
// inbounds.
func KernelEntries(kernelType string) []KernelEntry {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]KernelEntry, len(kernelEntries[kernelType]))
	copy(out, kernelEntries[kernelType])
	return out
}

// KernelMark resolves a kernel inbound tag to its entry mark within
// one kernel type.
func KernelMark(kernelType, tag string) (Mark, bool) {
	mu.RLock()
	defer mu.RUnlock()
	for _, e := range kernelEntries[kernelType] {
		if e.Tag == tag {
			return Mark{Kind: KindInbound, ID: e.Tag}, true
		}
	}
	return Mark{}, false
}
