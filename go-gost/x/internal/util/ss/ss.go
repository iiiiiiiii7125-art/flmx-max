package ss

import (
	"bytes"
	"errors"
	"net"
	"sync"

	"github.com/shadowsocks/go-shadowsocks2/core"
	ss "github.com/shadowsocks/shadowsocks-go/shadowsocks"
)

type shadowCipher struct {
	cipher *ss.Cipher
}

func (c *shadowCipher) StreamConn(conn net.Conn) net.Conn {
	return ss.NewConn(conn, c.cipher.Copy())
}

func (c *shadowCipher) PacketConn(conn net.PacketConn) net.PacketConn {
	return ss.NewSecurePacketConn(conn, c.cipher.Copy())
}

// ShadowCipher builds the shadowsocks cipher for the given method/password.
// It fails closed: an empty method or password is a configuration error, not
// a request for plaintext. Returning (nil, nil) here previously made every
// caller silently run unencrypted (D1-H1).
func ShadowCipher(method, password string, key string) (core.Cipher, error) {
	if method == "" || password == "" {
		return nil, errors.New("shadowsocks: cipher method and password are required (refusing to run unencrypted)")
	}

	c, _ := ss.NewCipher(method, password)
	if c != nil {
		return &shadowCipher{cipher: c}, nil
	}

	return core.PickCipher(method, []byte(key), password)
}

// Due to in/out byte length is inconsistent of the shadowsocks.Conn.Write,
// we wrap around it to make io.Copy happy.
type shadowConn struct {
	net.Conn
	wbuf bytes.Buffer
	once sync.Once
	mu   sync.Mutex
}

func ShadowConn(conn net.Conn, header []byte) net.Conn {
	c := &shadowConn{
		Conn: conn,
	}
	c.wbuf.Write(header)
	return c
}

func (c *shadowConn) Write(b []byte) (n int, err error) {
	n = len(b) // force byte length consistent

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.wbuf.Len() > 0 {
		c.wbuf.Write(b) // append the data to the cached header
		_, err = c.Conn.Write(c.wbuf.Bytes())
		c.wbuf.Reset()
		return
	}
	_, err = c.Conn.Write(b)
	return
}

// Read flushes a cached header before the first read (F-212-B):
// the header carries the target address, so in delayed mode the
// server cannot start the session — let alone let a server-first
// target speak — until it arrives. When it only rode on the first
// payload Write, a consumer that read first (any server-first
// protocol through an ss leg) waited on a peer that had never
// been told where to connect. All other ShadowConn users pass a
// nil header, so the flush is a no-op for them.
func (c *shadowConn) Read(b []byte) (n int, err error) {
	c.once.Do(func() {
		err = c.flushHeader()
	})
	if err != nil {
		return
	}
	return c.Conn.Read(b)
}

func (c *shadowConn) flushHeader() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wbuf.Len() > 0 {
		if _, err := c.Conn.Write(c.wbuf.Bytes()); err != nil {
			return err
		}
		c.wbuf.Reset()
	}
	return nil
}
