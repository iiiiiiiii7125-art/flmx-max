package kernel

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	logRingSize    = 200
	maxLogLineLen  = 500
	stopGrace      = 5 * time.Second
	stopHardLimit  = 30 * time.Second
	maxBackoff     = 60 * time.Second
	stableAfter    = 60 * time.Second
	startupProbeIn = 1500 * time.Millisecond
)

// ansiEscape matches terminal colour sequences; shadow-tls and sing-box
// colour their output even when it is not a TTY.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

type logRing struct {
	mu    sync.Mutex
	lines []string
}

func (r *logRing) add(line string) {
	line = ansiEscape.ReplaceAllString(line, "")
	if len(line) > maxLogLineLen {
		line = line[:maxLogLineLen] + "…"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
	if over := len(r.lines) - logRingSize; over > 0 {
		r.lines = append([]string(nil), r.lines[over:]...)
	}
}

func (r *logRing) tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || n > len(r.lines) {
		n = len(r.lines)
	}
	return append([]string(nil), r.lines[len(r.lines)-n:]...)
}

// process supervises one kernel binary, restarting it with exponential
// backoff until stop is called.
type process struct {
	bin    string
	args   []string
	dir    string
	logs   *logRing
	prefix string // prepended to output lines when a kernel runs several processes

	// credUID/credGID, when credUID > 0, run the child as that user
	// instead of the agent's own (Snell egress guard, see
	// snell_guard.go). Zero means inherit.
	credUID uint32
	credGID uint32

	// relaunchGuard, when set, is consulted before every supervisor
	// relaunch — never before the first launch, which the caller
	// already gated. A non-nil error keeps the process down for that
	// backoff round; the gate is retried on the next round, so the
	// process comes back on its own once the gate passes again.
	relaunchGuard func() error

	mu        sync.Mutex
	cmd       *exec.Cmd
	running   bool
	pid       int
	restarts  int
	lastErr   string
	startedAt int64

	stopCh   chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func newProcess(bin string, args []string, dir string, logs *logRing) *process {
	return &process{
		bin:    bin,
		args:   args,
		dir:    dir,
		logs:   logs,
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
	}
}

func (p *process) start() {
	go p.supervise()
}

func (p *process) supervise() {
	defer close(p.done)
	backoff := time.Second
	launched := false
	for {
		// D2-05: honour a stop requested before the first
		// launch (and between rounds) instead of running one
		// more process first.
		select {
		case <-p.stopCh:
			return
		default:
		}
		if launched && p.relaunchGuard != nil {
			if err := p.relaunchGuard(); err != nil {
				p.logs.add(p.prefix + "[flmx] 守卫规则未就绪，本轮不重启: " + err.Error())
				select {
				case <-p.stopCh:
					return
				case <-time.After(backoff):
				}
				if backoff *= 2; backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}
		}
		began := time.Now()
		err := p.runOnce()
		launched = true
		select {
		case <-p.stopCh:
			return
		default:
		}
		msg := "进程退出"
		if err != nil {
			msg = fmt.Sprintf("进程退出: %v", err)
		}
		p.mu.Lock()
		p.lastErr = msg
		p.restarts++
		p.mu.Unlock()
		p.logs.add(p.prefix + "[flmx] " + msg)

		if time.Since(began) > stableAfter {
			backoff = time.Second
		}
		select {
		case <-p.stopCh:
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (p *process) runOnce() error {
	cmd := exec.Command(p.bin, p.args...)
	cmd.Dir = p.dir
	cmd.SysProcAttr = sysProcAttrFor(p.credUID, p.credGID)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return err
	}
	p.mu.Lock()
	p.cmd = cmd
	p.running = true
	p.pid = cmd.Process.Pid
	p.startedAt = time.Now().UnixMilli()
	p.mu.Unlock()

	// D2-05: a stop requested while Start was in flight fired
	// its signals into a nil cmd and missed. Re-check now that
	// the process exists and kill it immediately in that case.
	select {
	case <-p.stopCh:
		signalGroup(cmd.Process.Pid, syscall.SIGKILL)
	default:
	}

	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			if line := strings.TrimRight(sc.Text(), "\r"); line != "" {
				p.logs.add(p.prefix + line)
			}
		}
		_, _ = io.Copy(io.Discard, pr)
	}()

	err := cmd.Wait()
	pw.Close()
	<-scanDone
	pr.Close()

	p.mu.Lock()
	p.running = false
	p.pid = 0
	p.cmd = nil
	p.mu.Unlock()
	return err
}

// stop terminates the process group and waits for the supervisor to exit.
func (p *process) stop() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
		p.signal(syscall.SIGTERM)
		// D2-05: exactly two signal shots are not enough — a
		// shot fired while cmd is still unassigned (slow exec
		// start) is a no-op, after which the old code blocked
		// on done forever. Keep signalling (TERM during the
		// grace window, KILL after it) until the supervisor
		// exits, bounded by a hard limit so stop itself can
		// never wedge the caller.
		grace := time.NewTimer(stopGrace)
		defer grace.Stop()
		hard := time.NewTimer(stopHardLimit)
		defer hard.Stop()
		poll := time.NewTicker(500 * time.Millisecond)
		defer poll.Stop()
		killing := false
		for {
			select {
			case <-p.done:
				return
			case <-grace.C:
				killing = true
				p.signal(syscall.SIGKILL)
			case <-poll.C:
				if killing {
					p.signal(syscall.SIGKILL)
				} else {
					p.signal(syscall.SIGTERM)
				}
			case <-hard.C:
				p.logs.add(p.prefix + "[flmx] stop 等待进程退出超时，放弃等待")
				return
			}
		}
	})
}

func (p *process) signal(sig syscall.Signal) {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	signalGroup(cmd.Process.Pid, sig)
}

func (p *process) snapshot() (running bool, pid, restarts int, lastErr string, startedAt int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running, p.pid, p.restarts, p.lastErr, p.startedAt
}
