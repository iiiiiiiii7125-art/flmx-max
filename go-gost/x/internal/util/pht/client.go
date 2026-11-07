package pht

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"

	"github.com/go-gost/core/logger"
)

type Client struct {
	Host          string
	Client        *http.Client
	AuthorizePath string
	PushPath      string
	PullPath      string
	TLSEnabled    bool
	Logger        logger.Logger
}

// Close releases the underlying HTTP transport: a full close when the
// transport supports it (e.g. *http3.Transport, which owns QUIC
// connections), otherwise idle connections are closed (e.g.
// *http.Transport). Used by dialers when a chain generation is retired
// so cached clients do not outlive their configuration (P1-08).
func (c *Client) Close() error {
	if c == nil || c.Client == nil {
		return nil
	}
	if closer, ok := c.Client.Transport.(io.Closer); ok {
		return closer.Close()
	}
	c.Client.CloseIdleConnections()
	return nil
}

func (c *Client) Dial(ctx context.Context, addr string) (net.Conn, error) {
	raddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		c.Logger.Error(err)
		return nil, err
	}

	if c.Host != "" {
		addr = net.JoinHostPort(c.Host, strconv.Itoa(raddr.Port))
	}

	token, err := c.authorize(ctx, addr)
	if err != nil {
		c.Logger.Error(err)
		return nil, err
	}

	cn := &clientConn{
		client:     c.Client,
		rxc:        make(chan []byte, 128),
		closed:     make(chan struct{}),
		localAddr:  &net.TCPAddr{},
		remoteAddr: raddr,
		logger:     c.Logger,
	}

	scheme := "http"
	if c.TLSEnabled {
		scheme = "https"
	}
	cn.pushURL = fmt.Sprintf("%s://%s%s?token=%s", scheme, addr, c.PushPath, token)
	cn.pullURL = fmt.Sprintf("%s://%s%s?token=%s", scheme, addr, c.PullPath, token)

	go cn.readLoop()

	return cn, nil
}

func (c *Client) authorize(ctx context.Context, addr string) (token string, err error) {
	var url string
	if c.TLSEnabled {
		url = fmt.Sprintf("https://%s%s", addr, c.AuthorizePath)
	} else {
		url = fmt.Sprintf("http://%s%s", addr, c.AuthorizePath)
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}

	if c.Logger.IsLevelEnabled(logger.TraceLevel) {
		dump, _ := httputil.DumpRequest(r, false)
		c.Logger.Trace(string(dump))
	} else if c.Logger.IsLevelEnabled(logger.DebugLevel) {
		c.Logger.Debugf("%s %s", r.Method, r.URL)
	}

	resp, err := c.Client.Do(r)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if c.Logger.IsLevelEnabled(logger.TraceLevel) {
		dump, _ := httputil.DumpResponse(resp, false)
		c.Logger.Trace(string(dump))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	if strings.HasPrefix(string(data), "token=") {
		token = strings.TrimPrefix(string(data), "token=")
	}
	if token == "" {
		err = errors.New("authorize failed")
	}
	return
}
