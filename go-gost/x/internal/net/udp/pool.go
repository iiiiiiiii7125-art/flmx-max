package udp

import (
	"sync"
	"time"

	"github.com/go-gost/core/logger"
)

// DefaultTTL is the session idle TTL applied whenever a caller
// supplies a non-positive value. It matches the default the UDP
// listener metadata parsers (listener/udp, listener/rudp) have
// always used; the Bind paths (chain, relay and socks connectors)
// pass BindOptions.UDPConnTTL through with no defaulting of their
// own, so the convergence has to happen here and in NewListener
// (D3-01): a TTL of zero used to reach time.NewTicker in the
// idleCheck goroutine, whose panic killed the whole agent.
const DefaultTTL = 30 * time.Second

type connPool struct {
	m      sync.Map
	ttl    time.Duration
	closed chan struct{}
	logger logger.Logger
}

func newConnPool(ttl time.Duration) *connPool {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	p := &connPool{
		ttl:    ttl,
		closed: make(chan struct{}),
	}
	go p.idleCheck()
	return p
}

func (p *connPool) WithLogger(logger logger.Logger) *connPool {
	p.logger = logger
	return p
}

func (p *connPool) Get(key any) (c *conn, ok bool) {
	if p == nil {
		return
	}

	v, ok := p.m.Load(key)
	if ok {
		c, ok = v.(*conn)
	}
	return
}

func (p *connPool) Set(key any, c *conn) {
	if p == nil {
		return
	}

	p.m.Store(key, c)
}

func (p *connPool) Delete(key any) {
	if p == nil {
		return
	}
	p.m.Delete(key)
}

func (p *connPool) Close() {
	if p == nil {
		return
	}

	select {
	case <-p.closed:
		return
	default:
	}

	close(p.closed)

	p.m.Range(func(k, v any) bool {
		if c, ok := v.(*conn); ok && c != nil {
			c.Close()
		}
		return true
	})
}

func (p *connPool) idleCheck() {
	ticker := time.NewTicker(p.ttl)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			size := 0
			idles := 0
			p.m.Range(func(key, value any) bool {
				c, ok := value.(*conn)
				if !ok || c == nil {
					p.Delete(key)
					return true
				}
				size++

				if c.IsIdle() {
					idles++
					p.Delete(key)
					c.Close()
					return true
				}

				c.SetIdle(true)

				return true
			})

			if idles > 0 {
				p.logger.Debugf("connection pool: size=%d, idle=%d", size, idles)
			}
		case <-p.closed:
			return
		}
	}
}
