package oidc

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Doer sends one request. *http.Client satisfies it.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// directClient is a one-request-per-connection HTTP/1.1 client. The server
// makes a handful of requests to one host per login, and http.Transport (with
// its HTTP/2 client) costs more binary than the NFR-OPS-001 budget has left,
// so the request is written and the response read by net/http's own
// Request.Write and ReadResponse over a connection this type dials. It does
// not follow redirects or read proxy settings.
type directClient struct {
	timeout time.Duration
	roots   *x509.CertPool // nil: the system roots
}

func (c directClient) Do(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(req.Context(), c.timeout)
	conn, err := c.dial(ctx, req)
	if err != nil {
		cancel()
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	release := func() {
		stop()
		cancel()
		_ = conn.Close()
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	out := req.Clone(ctx)
	out.Close = true
	if err := out.Write(conn); err != nil {
		release()
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), out)
	if err != nil {
		release()
		return nil, err
	}
	resp.Body = &connBody{ReadCloser: resp.Body, release: release}
	return resp, nil
}

func (c directClient) dial(ctx context.Context, req *http.Request) (net.Conn, error) {
	host := req.URL.Hostname()
	port := req.URL.Port()
	d := &net.Dialer{}
	switch req.URL.Scheme {
	case "https":
		if port == "" {
			port = "443"
		}
		td := &tls.Dialer{NetDialer: d, Config: &tls.Config{ServerName: host, RootCAs: c.roots, MinVersion: tls.VersionTLS12}}
		return td.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	case "http":
		if port == "" {
			port = "80"
		}
		return d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	default:
		return nil, fmt.Errorf("oidc: unsupported URL scheme %q", req.URL.Scheme)
	}
}

type connBody struct {
	io.ReadCloser
	release func()
	closed  bool
}

func (b *connBody) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	err := b.ReadCloser.Close()
	b.release()
	return err
}
