package contracts

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/testutil"
)

func unixHTTPServer(t *testing.T, handler http.Handler, config *tls.Config) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gxunix")
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
	if config != nil {
		s.TLS = config
		s.StartTLS()
	} else {
		s.Start()
	}
	t.Cleanup(s.Close)
	return path
}

// TestHTTPUnixSocket checks the unix-socket option of lib/http.DoWith over
// plain HTTP and TLS on every target against native Go. Java's HttpClient
// cannot connect to Unix sockets, so it reports them unsupported.
func TestHTTPUnixSocket(t *testing.T) {
	ca := issueTLS(t, "goalchemy test CA", nil, false, "pkcs8")
	serverID := issueTLS(t, "server", ca, true, "pkcs8")
	pair, err := tls.X509KeyPair([]byte(serverID.certPEM), []byte(serverID.keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Method + " " + r.Host + " " + r.URL.RequestURI()))
	})
	plain := unixHTTPServer(t, handler, nil)
	secure := unixHTTPServer(t, handler, &tls.Config{Certificates: []tls.Certificate{pair}})
	q := strconv.Quote
	program := fmt.Sprintf(`package main

import (
	"github.com/eugenioenko/goalchemy/lib/context"
	"github.com/eugenioenko/goalchemy/lib/http"
	"github.com/eugenioenko/goalchemy/std/strings"
)

const plain = %s
const secure = %s
const ca = %s

func show(name, method, url string, body []byte, options []string) {
	status, _, reply, err := http.DoWith(context.Background(), method, url, nil, body, 1024, 10000, options)
	if err != nil {
		kind := "failed"
		if strings.Contains(err.Error(), "not supported") {
			kind = "unsupported"
		}
		println(name, "error", kind, status, reply == nil)
		return
	}
	println(name, status, string(reply))
}

func main() {
	show("unix-get", "GET", "http://platform.local/a/b?c=d", nil, []string{http.UnixSocket, plain})
	show("unix-post", "POST", "http://platform.local/post", []byte("payload"), []string{http.UnixSocket, plain})
	show("unix-tls", "GET", "https://localhost/tls", nil, []string{http.UnixSocket, secure, http.RootCAs, ca})
	show("unix-tls-untrusted", "GET", "https://localhost/tls", nil, []string{http.UnixSocket, secure})
	show("unix-tls-wrong-host", "GET", "https://other.example/tls", nil, []string{http.UnixSocket, secure, http.RootCAs, ca})
	show("unix-missing", "GET", "http://platform.local/", nil, []string{http.UnixSocket, plain + "-missing"})
	show("unix-empty", "GET", "http://platform.local/", nil, []string{http.UnixSocket, ""})
}
`, q(plain), q(secure), q(ca.certPEM))
	unixLine := regexp.MustCompile(`(?m)^(unix-(?:get|post|tls|tls-untrusted|tls-wrong-host|missing)) .*$`)
	runAcrossTargets(t, "httpunixprobe", program, []string{
		"unix-get 200 GET platform.local /a/b?c=d", "unix-post 200 POST platform.local /post", "unix-tls 200 GET localhost /tls",
		"unix-tls-untrusted error failed 0 true", "unix-tls-wrong-host error failed 0 true", "unix-missing error failed 0 true",
		"unix-empty error failed 0 true",
	}, func(target string, want testutil.Observation) testutil.Observation {
		if target == "java" {
			want.Stderr = unixLine.ReplaceAllString(want.Stderr, "$1 error unsupported 0 true")
		}
		return want
	})
}
