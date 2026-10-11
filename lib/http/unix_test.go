package http

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func unixServer(t *testing.T, handler http.Handler) (*httptest.Server, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "gxhttp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewUnstartedServer(handler)
	s.Listener.Close()
	s.Listener = l
	return s, path
}

func TestUnixSocket(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(r.Host + " " + r.URL.Path)) })
	plain, path := unixServer(t, handler)
	plain.Start()
	defer plain.Close()
	status, _, body, err := DoWith(context.Background(), "GET", "http://platform.local/v1/x", nil, nil, 100, 5000, []string{UnixSocket, path})
	if err != nil || status != 200 || string(body) != "platform.local /v1/x" {
		t.Fatal(status, string(body), err)
	}
	if _, _, _, err := DoWith(context.Background(), "GET", "http://platform.local/", nil, nil, 100, 5000, []string{UnixSocket, path + "-missing"}); err == nil {
		t.Fatal("missing socket")
	}
	ca := issue(t, "test CA", nil, false)
	serverCert := issue(t, "server", ca, true)
	pair, _ := tls.X509KeyPair([]byte(serverCert.certPEM), []byte(serverCert.keyPEM))
	secure, spath := unixServer(t, handler)
	secure.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	secure.StartTLS()
	defer secure.Close()
	if _, _, body, err := DoWith(context.Background(), "GET", "https://localhost/s", nil, nil, 100, 5000, []string{UnixSocket, spath, RootCAs, ca.certPEM}); err != nil || string(body) != "localhost /s" {
		t.Fatal(string(body), err)
	}
	if _, _, _, err := DoWith(context.Background(), "GET", "https://other.example/s", nil, nil, 100, 5000, []string{UnixSocket, spath, RootCAs, ca.certPEM}); err == nil {
		t.Fatal("host name not verified over a unix socket")
	}
	for _, socket := range []string{"", "a\x00b", strings.Repeat("a", MaxSocketPath+1)} {
		if _, _, _, err := DoWith(context.Background(), "GET", "http://platform.local/", nil, nil, 100, 5000, []string{UnixSocket, socket}); err != invalid {
			t.Fatal(len(socket), err)
		}
	}
}
