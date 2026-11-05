package selector

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/go-gost/core/chain"
)

// D3-06 regression: FailFilter must not write to stdout. It runs on
// every node of every selection (the per-connection hot path), and the
// unconditional fmt.Printf bypassed the structured logger, amplified
// journal/disk writes under churn and leaked node names/addresses into
// whatever captures stdout.
func TestFailFilterDoesNotWriteToStdout(t *testing.T) {
	dead := chain.NewNode("dead", "192.0.2.1:1080")
	dead.Marker().Mark() // count=1 >= DefaultMaxFails=1, within failTimeout
	live := chain.NewNode("live", "192.0.2.2:1080")

	f := FailFilter[*chain.Node](DefaultMaxFails, DefaultFailTimeout)

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	out := f.Filter(context.Background(), dead, live)
	w.Close()
	os.Stdout = old

	captured, _ := io.ReadAll(r)
	if strings.Contains(string(captured), "[FailFilter]") {
		t.Fatalf("FailFilter wrote to stdout: %q", string(captured))
	}

	// Filtering behaviour itself must be unchanged: the node marked
	// failed beyond maxFails is excluded, the healthy one survives.
	if len(out) != 1 || out[0] != live {
		t.Fatalf("Filter result = %v, want only the live node", out)
	}

}
