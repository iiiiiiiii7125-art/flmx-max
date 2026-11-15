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
	"net/http"
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
// (experimental.clash_api.external_controller, e.g. "127.0.0.1:40900")
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
