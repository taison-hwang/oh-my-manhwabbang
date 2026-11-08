package oidc

import (
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDirectClient_overTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Content-Type")+" "+string(body))
	}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	t.Run("an untrusted certificate is refused", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/x", nil)
		if resp, err := (directClient{timeout: 5 * time.Second}).Do(req); err == nil {
			_ = resp.Body.Close()
			t.Fatal("a self-signed certificate was accepted against the system roots")
		}
	})

	t.Run("a POST with a form body", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/token?a=1", strings.NewReader("code=abc"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := (directClient{timeout: 5 * time.Second, roots: pool}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		got, _ := io.ReadAll(resp.Body)
		if want := "POST /token?a=1 application/x-www-form-urlencoded code=abc"; resp.StatusCode != 200 || string(got) != want {
			t.Fatalf("got %d %q, want 200 %q", resp.StatusCode, got, want)
		}
	})
}

func TestDirectClient_timesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/slow", nil)
	start := time.Now()
	if resp, err := (directClient{timeout: 200 * time.Millisecond}).Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("a hung provider did not time out")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("the timeout took %s", d)
	}
}

// The response head is bounded too, not only the body (U-B verification
// follow-up): a provider that streams one endless header line is cut off at
// the cap instead of being read until the timeout.
func TestDirectClient_boundsTheResponseHead(t *testing.T) {
	ln := httptest.NewUnstartedServer(nil).Listener
	defer func() { _ = ln.Close() }()
	done := make(chan struct{})
	defer close(done)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nX-Pad: ")
		pad := strings.Repeat("a", 64<<10)
		for range 64 { // 4 MiB of one header line, never finished
			if _, err := io.WriteString(c, pad); err != nil {
				return
			}
		}
		<-done
	}()
	req, _ := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
	start := time.Now()
	resp, err := (directClient{timeout: 5 * time.Second}).Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("an unbounded response head was accepted")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the head was read until the timeout (%s): %v", took, err)
	}
}
