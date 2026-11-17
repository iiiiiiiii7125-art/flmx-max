// Selective per-user connection kill for sing-box companions (C-(4)).
//
// Root cause (live proof 2026-11-15, test machine): a SIGHUP reload of
// the companion drops EVERY user's established sessions, not just the
// removed user's. sing-box re-creates the shared inbound/outbound/router
// on any config change, so both the removed and the kept users' upstream
// sockets close within ~1-3s of the reload (both conntrack marks gone,
// both downloads draining from buffers). Which download "survives" is a
// buffer race biased by start order — that is the C-(4) "reverse"
// (disabled u2 completed, kept u3 froze at 20,816,221 B), not a
// selective kill. The reload-as-kill is therefore NOT a per-user kill.
//
// The selective kill uses sing-box's own Clash API (experimental.clash_api
// on the companion, localhost only): list the live connections, match
// the removed users by the route rule the connection was admitted under
// ("auth_user=u2 => route(flmx-u2)"), and DELETE only those. sing-box
// tears the connection down on both sides, so the client sees the break
// and the transfer fails instead of draining from buffers. The kept
// users' connections are never touched, and no reload is needed for a
// removal-only change (the reload is exactly what killed them).
//
// A removal-only change also leaves the in-memory config stale (the
// removed user is gone from disk but still in the running config), so a
// removed user could reconnect until the next reload. The tombstone
// sweeper (Manager, 2s tick) closes any NEW connection a tombstoned
// user opens, until the user is re-added or the instance is reloaded /
// restarted (when the in-memory config is fresh again).
package kernel

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// clashAPIHTTP is the HTTP client the Clash API helpers use; a variable so
// tests can point it at a stub server.
var clashAPIHTTP = &http.Client{Timeout: 5 * time.Second}

// clashConnection is one entry of GET /connections.
type clashConnection struct {
	ID   string `json:"id"`
	Rule string `json:"rule"`
	// Metadata is present in the reply but not needed for the kill; the
	// rule alone identifies the admitting user.
}

// clashConnectionsReply is the shape of GET /connections.
type clashConnectionsReply struct {
	Connections []clashConnection `json:"connections"`
}

// clashAPIAddr extracts the companion's Clash API address
// (experimental.clash_api.external_controller, e.g. "127.0.0.1:41234")
// from a rendered sing-box config. Empty means the API is not configured.
func clashAPIAddr(configJSON []byte) string {
	var cfg struct {
		Experimental struct {
			ClashAPI struct {
				ExternalController string `json:"external_controller"`
			} `json:"clash_api"`
		} `json:"experimental"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return ""
	}
	return cfg.Experimental.ClashAPI.ExternalController
}

// companionClashAPIPortMin/Max bound the per-agent Clash API port
// range (R2C-01). The panel mirrors this range in
// go-backend/internal/kernel (ClashAPIPortForSecret); the two must
// stay in sync. 40900 — the old fixed port — is deliberately outside
// it: it is never rendered by the new panel and never chosen by the
// agent for a new instance. A running instance that already listens
// on 40900 keeps it until its next (re)start (sticky rule in
// resolveClashAPIAddr), when it is reallocated from this range.
const (
	companionClashAPIPortMin = 41000
	companionClashAPIPortMax = 41999
)

// clashAPIPort extracts the port from a "host:port" address; ok is
// false when the address does not parse or the port is out of range.
func clashAPIPort(addr string) (int, bool) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return 0, false
	}
	return port, true
}

// inClashAPIPortRange reports whether port is inside the per-agent
// Clash API range.
func inClashAPIPortRange(port int) bool {
	return port >= companionClashAPIPortMin && port <= companionClashAPIPortMax
}

// clashPortFree probes whether a Clash API port is free on this host
// by briefly listening on it. The probe-then-close window is tiny;
// if two agents race into it, the loser fails loudly at sing-box
// bind and picks a different port on the next apply (the winner now
// holds it) — never silently.
func clashPortFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// rewriteClashAPIAddr returns a copy of a rendered sing-box config
// with experimental.clash_api.external_controller set to addr
// (creating the experimental/clash_api objects if absent). The
// re-marshal uses the same shape as the panel's render, so only the
// address changes.
func rewriteClashAPIAddr(configJSON []byte, addr string) ([]byte, error) {
	var cfg map[string]interface{}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil, err
	}
	exp, _ := cfg["experimental"].(map[string]interface{})
	if exp == nil {
		exp = map[string]interface{}{}
	}
	clash, _ := exp["clash_api"].(map[string]interface{})
	if clash == nil {
		clash = map[string]interface{}{}
	}
	clash["external_controller"] = addr
	exp["clash_api"] = clash
	cfg["experimental"] = exp
	return json.MarshalIndent(cfg, "", "  ")
}

// resolveClashAPIAddr decides the Clash API address a companion will
// actually listen on. rendered is the address the panel rendered for
// this agent; prevAddr is the address the previously applied config
// carried ("" on first apply); running reports whether the instance
// is currently up.
//
//   - A running instance keeps the port it already listens on
//     (sticky): rewriting it under a live process would point the
//     selective kill at a port nothing — or something else —
//     listens on. The port only moves when the instance is
//     (re)started.
//   - A fresh (re)start prefers the previously used port (stable
//     across agent restarts), then the rendered port, then scans the
//     range from the rendered port upward (wrapping) for the first
//     free port. A rendered port outside the range (e.g. the old
//     40900 from an older panel) is never kept: the scan starts at
//     the range floor.
//   - The returned address is always 127.0.0.1:<port> (loopback
//     only); a rendered non-loopback host is not honoured.
//   - "" rendered means no Clash API is configured; "" is returned
//     and the caller must not rewrite anything.
func resolveClashAPIAddr(rendered, prevAddr string, running bool) (string, error) {
	if rendered == "" {
		return "", nil
	}
	if running && prevAddr != "" {
		return prevAddr, nil
	}
	for _, cand := range []string{prevAddr, rendered} {
		if cand == "" {
			continue
		}
		port, ok := clashAPIPort(cand)
		if !ok || !inClashAPIPortRange(port) {
			continue
		}
		if clashPortFree(port) {
			return fmt.Sprintf("127.0.0.1:%d", port), nil
		}
	}
	start := companionClashAPIPortMin
	if port, ok := clashAPIPort(rendered); ok && inClashAPIPortRange(port) {
		start = port
	}
	size := companionClashAPIPortMax - companionClashAPIPortMin + 1
	for i := 0; i < size; i++ {
		port := companionClashAPIPortMin + (start-companionClashAPIPortMin+i)%size
		if clashPortFree(port) {
			return fmt.Sprintf("127.0.0.1:%d", port), nil
		}
	}
	return "", fmt.Errorf("no free Clash API port in %d-%d", companionClashAPIPortMin, companionClashAPIPortMax)
}

// clashRuleUser parses a sing-box Clash API connection rule of the form
// "auth_user=u2 => route(flmx-u2)" and returns the user name ("u2").
// Rules that are not per-user auth rules return "".
func clashRuleUser(rule string) string {
	const prefix = "auth_user="
	if !strings.HasPrefix(rule, prefix) {
		return ""
	}
	rest := rule[len(prefix):]
	if i := strings.Index(rest, " "); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" {
		return ""
	}
	return rest
}

// listClashConnections returns the companion's live connections.
func listClashConnections(addr string) ([]clashConnection, error) {
	resp, err := clashAPIHTTP.Get("http://" + addr + "/connections")
	if err != nil {
		return nil, fmt.Errorf("clash api list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clash api list: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("clash api list: %w", err)
	}
	var reply clashConnectionsReply
	if err := json.Unmarshal(body, &reply); err != nil {
		return nil, fmt.Errorf("clash api list: %w", err)
	}
	return reply.Connections, nil
}

// closeClashConnection DELETEs one connection by id.
func closeClashConnection(addr, id string) error {
	req, err := http.NewRequest(http.MethodDelete, "http://"+addr+"/connections/"+id, nil)
	if err != nil {
		return err
	}
	resp, err := clashAPIHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("clash api delete: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("clash api delete: status %d", resp.StatusCode)
	}
	return nil
}

// closeClashUserConnections closes every live connection admitted as one
// of users (auth_user names, e.g. "u2"). It returns how many it closed.
// A connection whose rule is not a per-user auth rule is never touched.
func closeClashUserConnections(addr string, users []string) (int, error) {
	if addr == "" || len(users) == 0 {
		return 0, nil
	}
	want := make(map[string]bool, len(users))
	for _, u := range users {
		want[u] = true
	}
	conns, err := listClashConnections(addr)
	if err != nil {
		return 0, err
	}
	closed := 0
	for _, c := range conns {
		if !want[clashRuleUser(c.Rule)] {
			continue
		}
		if err := closeClashConnection(addr, c.ID); err != nil {
			return closed, err
		}
		closed++
	}
	return closed, nil
}

// singboxUsers extracts the auth_user names from every inbound of a
// rendered sing-box config (inbounds[].users[].name, e.g. "u1","u2").
func singboxUsers(configJSON []byte) ([]string, error) {
	var cfg struct {
		Inbounds []struct {
			Users []struct {
				Name string `json:"name"`
			} `json:"users"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var out []string
	for _, in := range cfg.Inbounds {
		for _, u := range in.Users {
			if u.Name == "" || seen[u.Name] {
				continue
			}
			seen[u.Name] = true
			out = append(out, u.Name)
		}
	}
	return out, nil
}

// addedSingboxUsers returns the user names present in newJSON but absent
// from oldJSON.
func addedSingboxUsers(oldJSON, newJSON []byte) ([]string, error) {
	newUsers, err := singboxUsers(newJSON)
	if err != nil {
		return nil, err
	}
	oldUsers, err := singboxUsers(oldJSON)
	if err != nil {
		return nil, err
	}
	inOld := make(map[string]bool, len(oldUsers))
	for _, u := range oldUsers {
		inOld[u] = true
	}
	var added []string
	for _, u := range newUsers {
		if !inOld[u] {
			added = append(added, u)
		}
	}
	return added, nil
}

// removedSingboxUsers returns the user names present in oldJSON but absent
// from newJSON (a removal-only diff; additions are ignored here).
func removedSingboxUsers(oldJSON, newJSON []byte) ([]string, error) {
	oldUsers, err := singboxUsers(oldJSON)
	if err != nil {
		return nil, err
	}
	newUsers, err := singboxUsers(newJSON)
	if err != nil {
		return nil, err
	}
	inNew := make(map[string]bool, len(newUsers))
	for _, u := range newUsers {
		inNew[u] = true
	}
	var removed []string
	for _, u := range oldUsers {
		if !inNew[u] {
			removed = append(removed, u)
		}
	}
	return removed, nil
}

// normalizeSingboxConfig returns a copy of a rendered sing-box config with
// every user-dependent part removed: inbound user lists, per-user direct
// outbounds (tag flmx-u*), and route rules that mention an auth_user.
// Two configs that differ only by which users exist normalize equal.
func normalizeSingboxConfig(configJSON []byte) ([]byte, error) {
	var cfg map[string]interface{}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil, err
	}
	if inbounds, ok := cfg["inbounds"].([]interface{}); ok {
		for _, raw := range inbounds {
			if in, ok := raw.(map[string]interface{}); ok {
				delete(in, "users")
			}
		}
	}
	if outbounds, ok := cfg["outbounds"].([]interface{}); ok {
		kept := outbounds[:0]
		for _, raw := range outbounds {
			if ob, ok := raw.(map[string]interface{}); ok {
				if tag, _ := ob["tag"].(string); strings.HasPrefix(tag, "flmx-u") {
					continue
				}
			}
			kept = append(kept, raw)
		}
		cfg["outbounds"] = kept
	}
	if route, ok := cfg["route"].(map[string]interface{}); ok {
		if rules, ok := route["rules"].([]interface{}); ok {
			kept := rules[:0]
			for _, raw := range rules {
				if r, ok := raw.(map[string]interface{}); ok {
					if _, hasAuth := r["auth_user"]; hasAuth {
						continue
					}
				}
				kept = append(kept, raw)
			}
			route["rules"] = kept
		}
	}
	// R2C-01: the Clash API address is an agent-local operational
	// detail, not user state — two configs differing only by it
	// normalize equal. The Apply-time rewrite already aligns it
	// with the running instance; this makes the classification
	// robust if they ever disagree (a port change alone must never
	// turn a user-only change into a reload).
	if exp, ok := cfg["experimental"].(map[string]interface{}); ok {
		delete(exp, "clash_api")
		if len(exp) == 0 {
			delete(cfg, "experimental")
		}
	}
	return json.Marshal(cfg)
}

// isUserOnlySingboxChange reports whether two rendered sing-box configs
// differ only by which users exist (inbound users, per-user outbounds,
// per-user route rules). A reload for such a change is exactly the C-(4)
// blast radius (it drops every user's sessions), so the caller skips it
// and enforces the removal with the selective kill + tombstones instead.
func isUserOnlySingboxChange(oldJSON, newJSON []byte) (bool, error) {
	oldNorm, err := normalizeSingboxConfig(oldJSON)
	if err != nil {
		return false, err
	}
	newNorm, err := normalizeSingboxConfig(newJSON)
	if err != nil {
		return false, err
	}
	return string(oldNorm) == string(newNorm), nil
}
