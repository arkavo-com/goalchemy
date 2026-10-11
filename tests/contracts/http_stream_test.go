package contracts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

const streamSize = 96 << 20

func streamPattern(n int) []byte {
	block := make([]byte, 65521)
	for i := range block {
		block[i] = byte(i*31 + i>>8)
	}
	out := make([]byte, 0, n)
	for len(out) < n {
		out = append(out, block[:min(len(block), n-len(out))]...)
	}
	return out
}

// TestHTTPStreams sends and receives bodies larger than lib/http.MaxBytes
// through std/http on every target: a request body from
// an io.Reader with a known and an unknown length, and a response body
// relayed as the next request's body. The server checks the bytes.
func TestHTTPStreams(t *testing.T) {
	data := streamPattern(streamSize)
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/upload":
			h := sha256.New()
			n, err := io.Copy(h, r.Body)
			ok := err == nil && n == streamSize && hex.EncodeToString(h.Sum(nil)) == want
			fmt.Fprintf(w, "%s %d %v chunked=%v", r.Header.Get("X-Case"), n, ok, len(r.TransferEncoding) > 0)
		case "/download":
			w.Header().Set("Content-Length", strconv.Itoa(streamSize))
			w.Write(data)
		case "/status":
			w.Header().Add("X-Multi", "a")
			w.Header().Add("X-Multi", "b")
			w.Header()["Date"] = nil
			w.WriteHeader(http.StatusTeapot)
			w.Write([]byte("short"))
		case "/slow":
			w.WriteHeader(200)
			w.Write([]byte("partial"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}
	}))
	defer server.Close()
	program := fmt.Sprintf(`package main

import (
	"github.com/eugenioenko/goalchemy/lib/context"
	"github.com/eugenioenko/goalchemy/std/http"
	"github.com/eugenioenko/goalchemy/std/io"
	"github.com/eugenioenko/goalchemy/std/strings"
)

const base = %s
const size = %d

type pattern struct {
	block []byte
	off   int
	left  int
}

func newPattern() *pattern {
	block := make([]byte, 65521)
	for i := range block {
		block[i] = byte(i*31 + i>>8)
	}
	return &pattern{block: block, left: size}
}

func (p *pattern) Read(b []byte) (int, error) {
	if p.left == 0 {
		return 0, io.EOF
	}
	n := copy(b, p.block[p.off:])
	if n > p.left {
		n = p.left
	}
	p.off = (p.off + n) %% len(p.block)
	p.left -= n
	return n, nil
}

func text(resp *http.Response, err error) string {
	if err != nil {
		return "error"
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return "body error"
	}
	return string(b)
}

func main() {
	ctx := context.Background()
	println(text(http.Do(ctx, &http.Request{Method: "POST", URL: base + "/upload", Headers: []string{"X-Case", "known"}, Body: newPattern(), ContentLength: size, TimeoutMillis: 120000})))
	println(text(http.Do(ctx, &http.Request{Method: "POST", URL: base + "/upload", Headers: []string{"X-Case", "chunked"}, Body: newPattern(), TimeoutMillis: 120000})))
	down, err := http.Do(ctx, &http.Request{Method: "GET", URL: base + "/download", TimeoutMillis: 120000})
	if err != nil {
		println("download error")
		return
	}
	println("download", down.Status)
	println(text(http.Do(ctx, &http.Request{Method: "POST", URL: base + "/upload", Headers: []string{"X-Case", "relay"}, Body: down.Body, TimeoutMillis: 120000})))
	resp, err := http.Do(ctx, &http.Request{Method: "GET", URL: base + "/status", TimeoutMillis: 5000})
	if err == nil {
		println("status", resp.Status, strings.Join(resp.Headers, ","))
		println(text(resp, nil))
	}
	resp, err = http.Do(ctx, &http.Request{Method: "GET", URL: base + "/slow", TimeoutMillis: 500})
	if err == nil {
		buf := make([]byte, 7)
		n, err := io.ReadFull(resp.Body, buf)
		println("slow", resp.Status, string(buf[:n]), err == nil)
		_, err = io.ReadAll(resp.Body)
		println("slow timeout", err != nil)
		resp.Body.Close()
	}
	_, err = http.Do(ctx, &http.Request{Method: "PUT", URL: base + "/status", TimeoutMillis: 5000})
	println("put", err != nil)
	_, err = http.Do(ctx, &http.Request{Method: "GET", URL: base + "/status", TimeoutMillis: 5000, Options: []string{"odd"}})
	println("odd options", err != nil)
}
`, strconv.Quote(server.URL), streamSize)
	runAcrossTargets(t, "httpstreamprobe", program, []string{
		"known 100663296 true chunked=false", "chunked 100663296 true chunked=true", "download 200", "relay 100663296 true chunked=true",
		"status 418 Content-Length,5,Content-Type,text/plain; charset=utf-8,X-Multi,a,X-Multi,b", "short", "slow 200 partial true", "slow timeout true",
		"put true", "odd options true",
	}, nil)
}
