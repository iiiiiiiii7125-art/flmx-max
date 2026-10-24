package config

import "strings"

// HealPersistedServices dedupes cfg.Services in place (see
// DeduplicateServices) and, when duplicates were removed, writes the healed
// config back to the persist path so the next start does not read the
// poisoned file again. It returns the removed service names; err is non-nil
// only when duplicates were removed but the re-persist failed — the
// in-memory config is healed either way, so the caller should warn, not
// abort startup (the next start will self-heal again).
func HealPersistedServices(cfg *Config) ([]string, error) {
	removed := DeduplicateServices(cfg)
	if len(removed) == 0 {
		return nil, nil
	}
	if err := PersistNow(cfg); err != nil {
		return removed, err
	}
	return removed, nil
}

// DeduplicateServices removes later entries in cfg.Services that repeat an
// earlier service name (names compared after trimming whitespace), keeping
// the first occurrence. It returns the removed names in removal order.
//
// R17-A: a persisted gost.json poisoned with duplicate service names makes
// the agent fail at startup (loader.Load cannot register the same name
// twice — the second copy fails at listener bind or with
// registry: duplicate object — and program.Start returns the error).
// Startup self-heal calls this before loading, and createServices relies on
// the same first-wins rule when it upserts into the live config.
//
// A nil cfg or a config without duplicates returns nil and leaves cfg
// untouched. Nil service entries are kept as-is.
func DeduplicateServices(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(cfg.Services))
	var removed []string
	kept := cfg.Services[:0]
	for _, svc := range cfg.Services {
		if svc == nil {
			kept = append(kept, svc)
			continue
		}
		name := strings.TrimSpace(svc.Name)
		if _, ok := seen[name]; ok {
			removed = append(removed, svc.Name)
			continue
		}
		seen[name] = struct{}{}
		kept = append(kept, svc)
	}
	if len(removed) == 0 {
		return nil
	}
	cfg.Services = kept
	return removed
}
