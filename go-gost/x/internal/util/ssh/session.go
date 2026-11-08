package ssh

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gost/core/logger"
	"golang.org/x/crypto/ssh"
)

const (
	defaultKeepaliveInterval = 30 * time.Second
	defaultKeepaliveTimeout  = 15 * time.Second
	defaultkeepaliveRetries  = 1
)

type Session struct {
	net.Conn
	client *ssh.Client
	closed chan struct{}
	dead   chan struct{}
	log    logger.Logger

	// streams counts the channels currently open over this
	// session (F05). retired marks the session as owned by a
	// replaced chain generation: once retired, the session's
	// underlying connection is closed as soon as its last stream
	// drains, so a retired session — and its Wait/WaitClose
	// goroutines — cannot outlive the generation that created it.
	streams   atomic.Int64
	retired   atomic.Bool
	closeOnce sync.Once
}

func NewSession(c net.Conn, client *ssh.Client, log logger.Logger) *Session {
	return &Session{
		Conn:   c,
		client: client,
		closed: make(chan struct{}),
		dead:   make(chan struct{}),
		log:    log,
	}
}

func (s *Session) OpenChannel(name string) (ssh.Channel, <-chan *ssh.Request, error) {
	ch, reqs, err := s.client.OpenChannel(name, nil)
	if err != nil {
		return nil, nil, err
	}
	s.streams.Add(1)
	return &countingChannel{
		Channel: ch,
		done: func() {
			s.streams.Add(-1)
			s.maybeClose()
		},
	}, reqs, nil
}

// countingChannel decrements the session's stream count exactly
// once when the channel is closed.
type countingChannel struct {
	ssh.Channel
	done func()
	once sync.Once
}

func (c *countingChannel) Close() error {
	err := c.Channel.Close()
	c.once.Do(c.done)
	return err
}

// NumStreams returns the number of channels currently open over
// the session.
func (s *Session) NumStreams() int {
	return int(s.streams.Load())
}

// Retire marks the session as belonging to a replaced chain
// generation. The underlying connection is closed as soon as the
// last open stream drains (immediately, if none is open), which
// also ends the Wait and WaitClose goroutines. New streams are
// unaffected only in the sense that a channel opened concurrently
// with Retire is counted and drained like any other; callers stop
// handing the session out before retiring it.
func (s *Session) Retire() {
	s.retired.Store(true)
	s.maybeClose()
}

// Close immediately closes the session's underlying connection,
// ending the session and all of its channels.
func (s *Session) Close() error {
	return s.Conn.Close()
}

func (s *Session) maybeClose() {
	if s.retired.Load() && s.streams.Load() == 0 {
		s.closeOnce.Do(func() {
			s.Conn.Close()
		})
	}
}

func (s *Session) IsClosed() bool {
	select {
	case <-s.dead:
		return true
	case <-s.closed:
		return true
	default:
	}
	return false
}

func (s *Session) Wait() error {
	defer close(s.closed)

	return s.client.Wait()
}

func (s *Session) WaitClose() {
	defer s.client.Close()

	select {
	case <-s.dead:
		s.log.Debugf("session is dead")
	case <-s.closed:
		s.log.Debugf("session is closed")
	}
}

func (s *Session) Keepalive(interval, timeout time.Duration, retries int) {
	if interval <= 0 {
		interval = defaultKeepaliveInterval
	}
	if timeout <= 0 {
		timeout = defaultKeepaliveTimeout
	}
	if retries <= 0 {
		retries = defaultkeepaliveRetries
	}

	s.log.Debugf("keepalive is enabled, interval: %v, timeout: %v, retries: %d", interval, timeout, retries)
	defer close(s.dead)

	t := time.NewTicker(interval)
	defer t.Stop()

	count := retries
	for {
		select {
		case <-t.C:
			start := time.Now()
			err := func() error {
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()

				select {
				case err := <-s.ping():
					return err
				case <-ctx.Done():
					return ctx.Err()
				}
			}()
			if err != nil {
				s.log.Debugf("ssh ping: %v", err)
				count--
				if count == 0 {
					return
				}
				continue
			}
			s.log.Debugf("ssh ping OK, RTT: %v", time.Since(start))
			count = retries
		case <-s.closed:
			return
		}
	}
}

func (s *Session) ping() <-chan error {
	ch := make(chan error, 1)
	go func() {
		defer close(ch)
		if _, _, err := s.client.SendRequest("ping", true, nil); err != nil {
			ch <- err
		}
	}()
	return ch
}
