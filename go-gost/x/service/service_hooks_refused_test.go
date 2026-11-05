package service

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	xlogger "github.com/go-gost/x/logger"
)

// D2-02 regression: execCmds was a live, timeout-less root
// /bin/sh -c executor sitting behind the single parse-layer hook
// rejection (S-01). In this managed agent build the executor
// itself must refuse to run anything: the panel never sends hooks
// and ParseService already rejects them, so no reachable path may
// turn a hook string into a shell command.
func TestServiceHooksAreNotExecutedInManagedBuild(t *testing.T) {
	dir := t.TempDir()
	markers := map[string]string{
		"pre-up":    filepath.Join(dir, "pre-up-ran"),
		"post-up":   filepath.Join(dir, "post-up-ran"),
		"pre-down":  filepath.Join(dir, "pre-down-ran"),
		"post-down": filepath.Join(dir, "post-down-ran"),
	}
	hook := func(phase string) []string {
		return []string{fmt.Sprintf("touch %s", markers[phase])}
	}

	svc := NewService("svc-hooks-refused",
		&fakeCloseListener{},
		fakeCloseHandler{},
		LoggerOption(xlogger.NewLogger()),
		PreUpOption(hook("pre-up")),
		PostUpOption(hook("post-up")),
		PreDownOption(hook("pre-down")),
		PostDownOption(hook("post-down")),
	)

	// Serve would run post-up; drive the lifecycle points that
	// invoke execCmds synchronously instead (NewService already
	// ran pre-up above). Close runs pre-down and post-down.
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	// post-up runs from Serve; invoke the same phase directly to
	// cover it without standing up an accept loop.
	svc.(*defaultService).execCmds("post-up", hook("post-up"))

	for phase, marker := range markers {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("%s hook was executed in the managed agent build (marker %s exists)", phase, marker)
		}
	}
}
