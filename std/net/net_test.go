package net

import (
	stdnet "net"
	"testing"
)

func TestSplitHostPort(t *testing.T) {
	for _, tt := range []struct {
		hostPort string
		host     string
		port     string
	}{
		{"localhost:http", "localhost", "http"},
		{"localhost:80", "localhost", "80"},

		{"localhost%lo0:http", "localhost%lo0", "http"},
		{"localhost%lo0:80", "localhost%lo0", "80"},
		{"[localhost%lo0]:http", "localhost%lo0", "http"},
		{"[localhost%lo0]:80", "localhost%lo0", "80"},

		{"127.0.0.1:http", "127.0.0.1", "http"},
		{"127.0.0.1:80", "127.0.0.1", "80"},
		{"[::1]:http", "::1", "http"},
		{"[::1]:80", "::1", "80"},

		{"[::1%lo0]:http", "::1%lo0", "http"},
		{"[::1%lo0]:80", "::1%lo0", "80"},

		{":http", "", "http"},
		{":80", "", "80"},

		{"golang.org:", "golang.org", ""},
		{"127.0.0.1:", "127.0.0.1", ""},
		{"[::1]:", "::1", ""},

		{"golang.org:https%foo", "golang.org", "https%foo"},
	} {
		if host, port, err := SplitHostPort(tt.hostPort); host != tt.host || port != tt.port || err != nil {
			t.Errorf("SplitHostPort(%q) = %q, %q, %v; want %q, %q, nil", tt.hostPort, host, port, err, tt.host, tt.port)
		}
	}

	for _, tt := range []struct {
		hostPort string
		err      string
	}{
		{"golang.org", "missing port in address"},
		{"127.0.0.1", "missing port in address"},
		{"[::1]", "missing port in address"},
		{"[fe80::1%lo0]", "missing port in address"},
		{"[localhost%lo0]", "missing port in address"},
		{"localhost%lo0", "missing port in address"},

		{"::1", "too many colons in address"},
		{"fe80::1%lo0", "too many colons in address"},
		{"fe80::1%lo0:80", "too many colons in address"},

		{"[foo:bar]", "missing port in address"},
		{"[foo:bar]baz", "missing port in address"},
		{"[foo]bar:baz", "missing port in address"},

		{"[foo]:[bar]:baz", "too many colons in address"},

		{"[foo]:[bar]baz", "unexpected '[' in address"},
		{"foo[bar]:baz", "unexpected '[' in address"},

		{"foo]bar:baz", "unexpected ']' in address"},
	} {
		if host, port, err := SplitHostPort(tt.hostPort); err == nil {
			t.Errorf("SplitHostPort(%q) should have failed", tt.hostPort)
		} else {
			e := err.(*AddrError)
			if e.Err != tt.err {
				t.Errorf("SplitHostPort(%q) = _, _, %q; want %q", tt.hostPort, e.Err, tt.err)
			}
			if host != "" || port != "" {
				t.Errorf("SplitHostPort(%q) = %q, %q, err; want %q, %q, err on failure", tt.hostPort, host, port, "", "")
			}
		}
	}
}

func TestJoinHostPort(t *testing.T) {
	for _, tt := range []struct {
		host     string
		port     string
		hostPort string
	}{
		{"localhost", "http", "localhost:http"},
		{"localhost", "80", "localhost:80"},

		{"localhost%lo0", "http", "localhost%lo0:http"},
		{"localhost%lo0", "80", "localhost%lo0:80"},

		{"127.0.0.1", "http", "127.0.0.1:http"},
		{"127.0.0.1", "80", "127.0.0.1:80"},
		{"::1", "http", "[::1]:http"},
		{"::1", "80", "[::1]:80"},

		{"::1%lo0", "http", "[::1%lo0]:http"},
		{"::1%lo0", "80", "[::1%lo0]:80"},

		{"", "http", ":http"},
		{"", "80", ":80"},

		{"golang.org", "", "golang.org:"},
		{"127.0.0.1", "", "127.0.0.1:"},
		{"::1", "", "[::1]:"},

		{"golang.org", "https%foo", "golang.org:https%foo"},
	} {
		if hostPort := JoinHostPort(tt.host, tt.port); hostPort != tt.hostPort {
			t.Errorf("JoinHostPort(%q, %q) = %q; want %q", tt.host, tt.port, hostPort, tt.hostPort)
		}
	}
}

func TestSplitHostPortMatchesStd(t *testing.T) {
	inputs := []string{
		"", ":", "::", "[", "]", "[]", "[]:", "[]:80", "[:", "]:", "a:b:c", "[a]:b:c", "[a]b", "[a]:b]", "[a]:[b",
		"a[b]:c", "a]b:c", "kas.example.com:443", "KAS.example.com:8443", "[fe80::1%25en0]:80", "1.2.3.4:5", "x:y:",
		"[[::1]]:80", "[::1]]:80", "[::1]:80:", "é:80", "host:port:", ":::",
	}
	for _, s := range inputs {
		host, port, err := SplitHostPort(s)
		shost, sport, serr := stdnet.SplitHostPort(s)
		if host != shost || port != sport || (err == nil) != (serr == nil) || err != nil && err.Error() != serr.Error() {
			t.Errorf("SplitHostPort(%q) = %q, %q, %v; want %q, %q, %v", s, host, port, err, shost, sport, serr)
		}
		if got, want := JoinHostPort(s, s), stdnet.JoinHostPort(s, s); got != want {
			t.Errorf("JoinHostPort(%q, %q) = %q, want %q", s, s, got, want)
		}
	}
	var nilErr *AddrError
	if got := nilErr.Error(); got != "<nil>" {
		t.Errorf("(*AddrError)(nil).Error() = %q", got)
	}
}
