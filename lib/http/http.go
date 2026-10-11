// SPDX-License-Identifier: Apache-2.0
// Package http provides bounded HTTP exchanges, retaining TLS verification
// and returning redirect responses without following them.
package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	stdhttp "net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// MaxBytes is the largest request or response body Do accepts (64MiB).
const MaxBytes = 64 << 20

// MaxHeaderBytes is the largest request or response header size Do accepts (64KiB).
const MaxHeaderBytes = 64 << 10

// MaxOptionBytes is the largest total size of the options DoWith accepts (1MiB).
const MaxOptionBytes = 1 << 20

// Option names for DoWith. Values are PEM text, except InsecureSkipVerify,
// which is "true" or "false".
const (
	ClientCert         = "client-cert"
	ClientKey          = "client-key"
	RootCAs            = "root-cas"
	InsecureSkipVerify = "insecure-skip-verify"
	UnixSocket         = "unix-socket"
)

var invalid = errors.New("http: invalid request or limit")
var badTLS = errors.New("http: invalid TLS option")
var tooLarge = errors.New("http: response body exceeds limit")

// Do performs one bounded HTTP GET or POST exchange. It takes alternating header names/values. timeoutMillis uses real elapsed
// host time; it is required (1..300000). A parent deadline can shorten it.
// Result headers preserve duplicate values, with names sorted, canonicalized.
// Non-2xx/redirect statuses are ordinary results; transport/limit failures
// return zero status and nil headers/body, never a partial successful body.
func Do(ctx context.Context, method, rawURL string, headers []string, body []byte, maxResponseBytes int, timeoutMillis int64) (int, []string, []byte, error) {
	return DoWith(ctx, method, rawURL, headers, body, maxResponseBytes, timeoutMillis, nil)
}

// DoWith is Do with transport options as alternating names and values:
// ClientCert and ClientKey (both or neither) present a client certificate,
// RootCAs trusts more certificate authorities in addition to the system's,
// InsecureSkipVerify "true" disables server certificate verification, and
// UnixSocket connects to the Unix domain socket at its path instead of the
// URL's host, which still names the Host header and TLS server. Each name
// appears at most once. TLS options apply to https URLs.
func DoWith(ctx context.Context, method, rawURL string, headers []string, body []byte, maxResponseBytes int, timeoutMillis int64, options []string) (int, []string, []byte, error) {
	if ctx == nil || (method != "GET" && method != "POST") || len(body) > MaxBytes || (method == "GET" && len(body) != 0) || maxResponseBytes < 0 || maxResponseBytes > MaxBytes || timeoutMillis < 1 || timeoutMillis > 300000 || len(headers)%2 != 0 || len(rawURL) > 8192 {
		return 0, nil, nil, invalid
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return 0, nil, nil, invalid
	}
	config, socket, err := transportOptions(options)
	if err != nil {
		return 0, nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMillis)*time.Millisecond)
	defer cancel()
	req, err := stdhttp.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, invalid
	}
	total := 0
	for i := 0; i < len(headers); i += 2 {
		name, value := headers[i], headers[i+1]
		total += len(name) + len(value) + 4
		if total > MaxHeaderBytes || !token(name) || badValue(value) || forbidden(name) {
			return 0, nil, nil, invalid
		}
		req.Header.Add(name, value)
	}
	// Dedicated transport avoids unbounded global pooled resources and host
	// default-client mutations; proxies follow the standard trusted host config.
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	proxy, dial := stdhttp.ProxyFromEnvironment, dialer.DialContext
	if socket != "" {
		proxy = nil
		dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		}
	}
	tr := &stdhttp.Transport{Proxy: proxy, DialContext: dial, TLSHandshakeTimeout: 10 * time.Second, TLSClientConfig: config, DisableKeepAlives: true, MaxResponseHeaderBytes: MaxHeaderBytes}
	defer tr.CloseIdleConnections()
	client := &stdhttp.Client{Transport: tr, CheckRedirect: func(*stdhttp.Request, []*stdhttp.Request) error { return stdhttp.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxResponseBytes)+1))
	if err != nil {
		return 0, nil, nil, err
	}
	if len(out) > maxResponseBytes {
		return 0, nil, nil, tooLarge
	}
	names := make([]string, 0, len(resp.Header))
	for name := range resp.Header {
		names = append(names, name)
	}
	sort.Strings(names)
	var result []string
	for _, name := range names {
		for _, value := range resp.Header[name] {
			result = append(result, name, value)
		}
	}
	return resp.StatusCode, result, out, nil
}

// MaxSocketPath bounds the UnixSocket option.
const MaxSocketPath = 4096

func transportOptions(options []string) (*tls.Config, string, error) {
	if len(options) == 0 {
		return nil, "", nil
	}
	if len(options)%2 != 0 {
		return nil, "", invalid
	}
	values := map[string]string{}
	total := 0
	for i := 0; i < len(options); i += 2 {
		name, value := options[i], options[i+1]
		total += len(name) + len(value)
		_, seen := values[name]
		if total > MaxOptionBytes || seen || (name != ClientCert && name != ClientKey && name != RootCAs && name != InsecureSkipVerify && name != UnixSocket) {
			return nil, "", invalid
		}
		values[name] = value
	}
	cert, hasCert := values[ClientCert]
	key, hasKey := values[ClientKey]
	skip, hasSkip := values[InsecureSkipVerify]
	socket, hasSocket := values[UnixSocket]
	if hasCert != hasKey || hasSkip && skip != "true" && skip != "false" || hasSocket && (socket == "" || len(socket) > MaxSocketPath || strings.IndexByte(socket, 0) >= 0) {
		return nil, "", invalid
	}
	config := &tls.Config{InsecureSkipVerify: skip == "true"}
	if hasCert {
		pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
		if err != nil {
			return nil, "", badTLS
		}
		config.Certificates = []tls.Certificate{pair}
	}
	if roots, ok := values[RootCAs]; ok {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(roots)) {
			return nil, "", badTLS
		}
		config.RootCAs = pool
	}
	return config, socket, nil
}

func token(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return false
		}
	}
	return true
}
func forbidden(s string) bool {
	switch strings.ToLower(s) {
	case "host", "content-length", "transfer-encoding", "connection", "proxy-authorization", "proxy-connection", "upgrade", "trailer", "te":
		return true
	}
	return false
}

func badValue(s string) bool {
	for _, c := range s {
		if c == 127 || c < 32 && c != '\t' {
			return true
		}
	}
	return false
}
