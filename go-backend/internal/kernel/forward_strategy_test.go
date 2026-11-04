package kernel

import "testing"

// R4-F4-02: the write-face legal set for forward strategies is a
// single definition shared by the handler write faces and the repo
// restore face. Aliases the agent parser tolerates (rr/random/ha)
// are NOT panel contract values and must stay illegal here.
func TestForwardStrategyLegal(t *testing.T) {
	for _, s := range []string{"fifo", "round", "rand", "hash", " fifo ", "\thash\n"} {
		if !ForwardStrategyLegal(s) {
			t.Fatalf("ForwardStrategyLegal(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "banana", "FIFO", "Round", "rr", "random", "ha", "roundrobin", "fifo2"} {
		if ForwardStrategyLegal(s) {
			t.Fatalf("ForwardStrategyLegal(%q) = true, want false", s)
		}
	}
}
