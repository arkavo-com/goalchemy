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
	"sync"
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
	req, err := newRequest(ctx, method, u, headers, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	client, tr := newClient(config, socket, false)
	defer tr.CloseIdleConnections()
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
	return resp.StatusCode, responseHeaders(resp), out, nil
}

func newRequest(ctx context.Context, method string, u *url.URL, headers []string, body io.Reader) (*stdhttp.Request, error) {
	req, err := stdhttp.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, invalid
	}
	total := 0
	for i := 0; i < len(headers); i += 2 {
		name, value := headers[i], headers[i+1]
		total += len(name) + len(value) + 4
		if total > MaxHeaderBytes || !token(name) || badValue(value) || forbidden(name) {
			return nil, invalid
		}
		req.Header.Add(name, value)
	}
	return req, nil
}

// newClient returns a dedicated transport, which avoids unbounded global
// pooled resources and host default-client mutations; proxies follow the
// standard trusted host config.
func newClient(config *tls.Config, socket string, raw bool) (*stdhttp.Client, *stdhttp.Transport) {
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	proxy, dial := stdhttp.ProxyFromEnvironment, dialer.DialContext
	if socket != "" {
		proxy = nil
		dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		}
	}
	tr := &stdhttp.Transport{Proxy: proxy, DialContext: dial, TLSHandshakeTimeout: 10 * time.Second, TLSClientConfig: config, DisableKeepAlives: true, MaxResponseHeaderBytes: MaxHeaderBytes, DisableCompression: raw}
	return &stdhttp.Client{Transport: tr, CheckRedirect: func(*stdhttp.Request, []*stdhttp.Request) error { return stdhttp.ErrUseLastResponse }}, tr
}

func responseHeaders(resp *stdhttp.Response) []string {
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
	return result
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

// MaxChunk bounds the bytes of one Exchange Write or Read (1MiB).
const MaxChunk = 1 << 20

// MaxExchangeMillis bounds the timeout of an Exchange (24 hours).
const MaxExchangeMillis = 24 * 60 * 60 * 1000

var errClosed = errors.New("http: exchange closed")
var errState = errors.New("http: invalid exchange operation")

// Exchange is one HTTP exchange whose request body is written and whose
// response body is read in chunks, so neither is held in memory whole.
type Exchange struct {
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	pipe      *io.PipeWriter
	length    int64
	written   int64
	done      chan struct{}
	resp      *stdhttp.Response
	err       error
	tr        *stdhttp.Transport
	responded bool
	closed    bool
}

// Open starts a GET or POST exchange. contentLength is the exact request
// body length, or -1 for an unknown length sent chunked; GET takes 0.
// timeoutMillis (1..MaxExchangeMillis) bounds the whole exchange, including
// reading the response body, and ctx cancellation ends it. Options are those
// of DoWith. Exchanges neither request nor decode compression: response
// bytes are returned as received. Close releases the exchange.
func Open(ctx context.Context, method, rawURL string, headers []string, contentLength int64, timeoutMillis int64, options []string) (*Exchange, error) {
	if ctx == nil || (method != "GET" && method != "POST") || contentLength < -1 || (method == "GET" && contentLength != 0) || timeoutMillis < 1 || timeoutMillis > MaxExchangeMillis || len(headers)%2 != 0 || len(rawURL) > 8192 {
		return nil, invalid
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, invalid
	}
	config, socket, err := transportOptions(options)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMillis)*time.Millisecond)
	e := &Exchange{ctx: ctx, cancel: cancel, length: contentLength, done: make(chan struct{})}
	var body io.Reader
	if contentLength != 0 {
		r, w := io.Pipe()
		body, e.pipe = r, w
	}
	req, err := newRequest(ctx, method, u, headers, body)
	if err != nil {
		cancel()
		return nil, err
	}
	req.ContentLength = contentLength
	var client *stdhttp.Client
	client, e.tr = newClient(config, socket, true)
	go func() {
		defer close(e.done)
		e.resp, e.err = client.Do(req)
	}()
	return e, nil
}

func (e *Exchange) failure(err error) error {
	if e.ctx.Err() != nil {
		return e.ctx.Err()
	}
	return err
}

// Write sends up to MaxChunk bytes of the request body. It blocks until the
// transport takes them.
func (e *Exchange) Write(p []byte) error {
	if e == nil || len(p) > MaxChunk {
		return invalid
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return errClosed
	}
	if e.responded || e.pipe == nil && len(p) != 0 || e.length >= 0 && int64(len(p)) > e.length-e.written {
		e.mu.Unlock()
		return errState
	}
	e.written += int64(len(p))
	e.mu.Unlock()
	if len(p) == 0 {
		return nil
	}
	if _, err := e.pipe.Write(p); err != nil {
		return e.failure(err)
	}
	return nil
}

// Response ends the request body and waits for the response status and
// headers, which are sorted and canonicalized as for Do. A known request
// length must have been written in full.
func (e *Exchange) Response() (int, []string, error) {
	if e == nil {
		return 0, nil, invalid
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return 0, nil, errClosed
	}
	first := !e.responded
	e.responded = true
	short := e.length >= 0 && e.written != e.length
	e.mu.Unlock()
	if first && e.pipe != nil {
		if short {
			e.pipe.CloseWithError(errState)
			e.cancel()
		} else {
			e.pipe.Close()
		}
	}
	if short {
		return 0, nil, errState
	}
	<-e.done
	if e.err != nil {
		return 0, nil, e.failure(e.err)
	}
	return e.resp.StatusCode, responseHeaders(e.resp), nil
}

// Read returns up to max (1..MaxChunk) bytes of the response body after
// Response. eof reports the end of the body; data is then empty.
func (e *Exchange) Read(max int) ([]byte, bool, error) {
	if e == nil || max < 1 || max > MaxChunk {
		return nil, false, invalid
	}
	e.mu.Lock()
	closed, responded := e.closed, e.responded
	e.mu.Unlock()
	if closed {
		return nil, false, errClosed
	}
	if !responded {
		return nil, false, errState
	}
	<-e.done
	if e.err != nil {
		return nil, false, e.failure(e.err)
	}
	p := make([]byte, max)
	for {
		n, err := e.resp.Body.Read(p)
		if n > 0 {
			return p[:n], false, nil
		}
		if err == io.EOF {
			return nil, true, nil
		}
		if err != nil {
			return nil, false, e.failure(err)
		}
	}
}

// Close ends the exchange and releases its connection. It accepts nil and
// is idempotent.
func (e *Exchange) Close() {
	if e == nil {
		return
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	e.mu.Unlock()
	e.cancel()
	if e.pipe != nil {
		e.pipe.CloseWithError(errClosed)
	}
	<-e.done
	if e.resp != nil {
		e.resp.Body.Close()
	}
	e.tr.CloseIdleConnections()
}
