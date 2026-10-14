package main

// goalchemy:gate cooperative

import (
	"github.com/eugenioenko/goalchemy/lib/context"
	"github.com/eugenioenko/goalchemy/std/connect"
	"github.com/eugenioenko/goalchemy/std/encoding/hex"
	"github.com/eugenioenko/goalchemy/std/encoding/jsonvalue"
	"github.com/eugenioenko/goalchemy/std/encoding/protojson"
	"github.com/eugenioenko/goalchemy/std/errors"
	"github.com/eugenioenko/goalchemy/std/strconv"
	"github.com/eugenioenko/goalchemy/std/strings"
)

type Echo struct {
	Name string
}

func (m *Echo) EncodeProtoJSON(e *protojson.Encoder) {
	e.BeginObject()
	if m != nil && m.Name != "" {
		e.Key("name")
		e.String(m.Name)
	}
	e.EndObject()
}

func (m *Echo) DecodeProtoJSON(d *protojson.Decoder, v jsonvalue.Value) {
	if !d.Object(v, "test.Echo") {
		return
	}
	for _, k := range v.Keys() {
		if k == "name" {
			m.Name = d.String(v.Get(k), "name")
		}
	}
}

type reply struct {
	status  int
	headers []string
	body    string
	err     error
}

type fakeTransport struct {
	replies map[string]reply
	calls   int
}

func (t *fakeTransport) Do(ctx context.Context, method, url string, headers []string, body []byte, maxResponseBytes int, timeoutMillis int64) (int, []string, []byte, error) {
	t.calls++
	println("request", method, url, string(body), maxResponseBytes, timeoutMillis)
	println("  headers", strings.Join(headers, "|"))
	r, ok := t.replies[url]
	if !ok {
		return 404, []string{"Content-Type", "text/plain"}, []byte("404 page not found"), nil
	}
	if r.err != nil {
		return 0, nil, nil, r.err
	}
	return r.status, r.headers, []byte(r.body), nil
}

const jsonType = "application/json"

func show(label string, resp *Echo, meta []string, err error) {
	if err == nil {
		out, _ := protojson.Marshal(resp)
		println(label, "ok", string(out), strings.Join(meta, "|"))
		return
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		println(label, "not a connect error", err.Error())
		return
	}
	println(label, "error", ce.Code.String(), ce.Error(), strings.Join(ce.Meta, "|"), len(ce.Details))
	for _, d := range ce.Details {
		debug, _ := jsonvalue.Encode(d.Debug)
		println("  detail", d.TypeURL, hex.EncodeToString(d.Value), string(debug))
	}
}

func main() {
	base := "https://platform.example.com"
	fake := &fakeTransport{replies: map[string]reply{
		base + "/test.EchoService/Ok":      {200, []string{"Content-Type", jsonType, "X-Echo", "e1"}, `{"name":"pong","extra":1}`, nil},
		base + "/test.EchoService/Charset": {200, []string{"Content-Type", "application/json; charset=utf-8"}, `{}`, nil},
		base + "/test.EchoService/NotFound": {404, []string{"Content-Type", jsonType, "X-Reason", "gone"},
			`{"code":"not_found","message":"no such echo","details":[{"type":"common.Metadata","value":"AQI","debug":{"labels":{"k":"v"}}}]}`, nil},
		base + "/test.EchoService/Denied":  {403, []string{"Content-Type", jsonType}, `{"code":"permission_denied","message":"nope"}`, nil},
		base + "/test.EchoService/NoCode":  {429, []string{"Content-Type", jsonType}, `{"message":"slow down"}`, nil},
		base + "/test.EchoService/BadCode": {401, []string{"Content-Type", jsonType}, `{"code":"teapot"}`, nil},
		base + "/test.EchoService/Plain":   {503, []string{"Content-Type", "text/plain"}, "busy", nil},
		base + "/test.EchoService/Html":    {200, []string{"Content-Type", "text/html"}, "<p>", nil},
		base + "/test.EchoService/Proto":   {200, []string{"Content-Type", "application/proto"}, "", nil},
		base + "/test.EchoService/Garbage": {200, []string{"Content-Type", jsonType}, `{"name":7}`, nil},
		base + "/test.EchoService/Down":    {0, nil, "", errors.New("dial tcp: connection refused")},
	}}
	client := connect.NewClient(base + "/")
	client.Headers = []string{"Authorization", "Bearer t0k"}
	client.TimeoutMillis = 1500
	client.Transport = []string{"InsecureSkipVerify", "true"}
	client.HTTPClient = fake
	ctx := context.Background()

	for _, name := range []string{"Ok", "Charset", "NotFound", "Denied", "NoCode", "BadCode", "Plain", "Html", "Proto", "Garbage", "Down", "Missing"} {
		resp := &Echo{}
		meta, err := client.CallUnary(ctx, "/test.EchoService/"+name, &Echo{Name: "ping-" + name}, resp, []string{"X-Call", name})
		show(name, resp, meta, err)
		println("code-of", name, connect.CodeOf(err).String())
	}

	client.MaxResponseBytes = 1024
	client.TimeoutMillis = 0
	resp := &Echo{}
	_, err := client.CallUnary(ctx, "/test.EchoService/Ok", &Echo{}, resp, nil)
	show("defaults", resp, nil, err)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	before := fake.calls
	_, err = client.CallUnary(cancelled, "/test.EchoService/Ok", &Echo{}, &Echo{}, nil)
	println("cancelled", connect.CodeOf(err).String(), "calls", strconv.Itoa(fake.calls-before))
}
