// Package net provides the host and port helpers of Go's net package:
// JoinHostPort, SplitHostPort and AddrError. Dialing, listening and name
// resolution are not provided.
package net

import "github.com/eugenioenko/goalchemy/std/strings"

// AddrError reports an error with an address, as net.AddrError does.
type AddrError struct {
	Err  string
	Addr string
}

func (e *AddrError) Error() string {
	if e == nil {
		return "<nil>"
	}
	s := e.Err
	if e.Addr != "" {
		s = "address " + e.Addr + ": " + s
	}
	return s
}

func (e *AddrError) Timeout() bool   { return false }
func (e *AddrError) Temporary() bool { return false }

// SplitHostPort splits a network address of the form "host:port",
// "host%zone:port", "[host]:port" or "[host%zone]:port" into host or
// host%zone and port.
//
// A literal IPv6 address in hostport must be enclosed in square
// brackets, as in "[::1]:80", "[::1%lo0]:80".
func SplitHostPort(hostport string) (host, port string, err error) {
	const (
		missingPort   = "missing port in address"
		tooManyColons = "too many colons in address"
	)
	j, k := 0, 0

	i := strings.LastIndexByte(hostport, ':')
	if i < 0 {
		return "", "", &AddrError{Err: missingPort, Addr: hostport}
	}

	if hostport[0] == '[' {
		end := strings.IndexByte(hostport, ']')
		if end < 0 {
			return "", "", &AddrError{Err: "missing ']' in address", Addr: hostport}
		}
		switch end + 1 {
		case len(hostport):
			return "", "", &AddrError{Err: missingPort, Addr: hostport}
		case i:
		default:
			if hostport[end+1] == ':' {
				return "", "", &AddrError{Err: tooManyColons, Addr: hostport}
			}
			return "", "", &AddrError{Err: missingPort, Addr: hostport}
		}
		host = hostport[1:end]
		j, k = 1, end+1
	} else {
		host = hostport[:i]
		if strings.IndexByte(host, ':') >= 0 {
			return "", "", &AddrError{Err: tooManyColons, Addr: hostport}
		}
	}
	if strings.IndexByte(hostport[j:], '[') >= 0 {
		return "", "", &AddrError{Err: "unexpected '[' in address", Addr: hostport}
	}
	if strings.IndexByte(hostport[k:], ']') >= 0 {
		return "", "", &AddrError{Err: "unexpected ']' in address", Addr: hostport}
	}

	port = hostport[i+1:]
	return host, port, nil
}

// JoinHostPort combines host and port into a network address of the
// form "host:port". If host contains a colon, as found in literal
// IPv6 addresses, then JoinHostPort returns "[host]:port".
func JoinHostPort(host, port string) string {
	if strings.IndexByte(host, ':') >= 0 {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}
