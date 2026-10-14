package integration

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eugenioenko/goalchemy/internal/testutil"
)

const connectProbe = `package main

import (
	"github.com/eugenioenko/goalchemy/lib/context"
	"github.com/eugenioenko/goalchemy/std/connect"
	"github.com/eugenioenko/goalchemy/std/encoding/hex"
	"github.com/eugenioenko/goalchemy/std/encoding/jsonvalue"
	"github.com/eugenioenko/goalchemy/std/encoding/protojson"
	"github.com/eugenioenko/goalchemy/std/errors"
	"github.com/eugenioenko/goalchemy/lib/http"
	authorizationv2 "connectprobe/gen/authorization/v2"
	"connectprobe/gen/entity"
	"connectprobe/gen/policy"
	"connectprobe/gen/policy/attributes"
)

const base = %s

func header(h []string, name string) string {
	for i := 0; i+1 < len(h); i += 2 {
		if h[i] == name {
			return h[i+1]
		}
	}
	return ""
}

type passthrough struct {
	calls int
}

func (p *passthrough) Do(ctx context.Context, method, url string, headers []string, body []byte, maxResponseBytes int, timeoutMillis int64) (int, []string, []byte, error) {
	p.calls++
	return http.Do(ctx, method, url, headers, body, maxResponseBytes, timeoutMillis)
}

func show(label string, m protojson.Message, err error) {
	if err != nil {
		var ce *connect.Error
		if !errors.As(err, &ce) {
			println(label, "not a connect error", err.Error())
			return
		}
		println(label, "error", ce.Code.String(), ce.Error(), "reason="+header(ce.Meta, "X-Reason"), len(ce.Details))
		for _, d := range ce.Details {
			debug, _ := jsonvalue.Encode(d.Debug)
			println("  detail", d.TypeURL, hex.EncodeToString(d.Value), string(debug))
		}
		return
	}
	out, merr := protojson.Marshal(m)
	println(label, string(out), merr == nil)
}

func main() {
	ctx := context.Background()
	client := connect.NewClient(base + "/")
	client.Headers = []string{"X-Probe", "probe-1"}
	attrs := attributes.NewAttributesServiceClient(client)
	authz := authorizationv2.NewAuthorizationServiceClient(client)

	got, err := attrs.GetAttribute(ctx, &attributes.GetAttributeRequest{Identifier: &attributes.GetAttributeRequest_Fqn{Fqn: "https://example.com/attr/classification"}})
	show("get-fqn", got, err)
	if err == nil {
		a := got.GetAttribute()
		println("rule", a.GetRule().String(), "values", len(a.GetValues()), "active", *a.GetActive(), "ns", a.GetNamespace().GetName(),
			"created", a.GetMetadata().GetCreatedAt().AsTime().Format("2006-01-02T15:04:05.999999999Z07:00"), "owner", a.GetMetadata().GetLabels()["owner"])
	}
	got, err = attrs.GetAttribute(ctx, &attributes.GetAttributeRequest{Identifier: &attributes.GetAttributeRequest_AttributeId{AttributeId: "a1"}})
	show("get-id", got, err)
	got, err = attrs.GetAttribute(ctx, &attributes.GetAttributeRequest{Identifier: &attributes.GetAttributeRequest_Fqn{Fqn: "missing"}})
	show("get-missing", got, err)
	println("code-of", connect.CodeOf(err).String(), connect.CodeOf(nil) == 0)
	if ce, ok := err.(*connect.Error); ok && len(ce.Details) == 1 {
		var ns policy.Namespace
		any := &protojson.Any{TypeURL: ce.Details[0].TypeURL, Fields: ce.Details[0].Debug}
		uerr := any.UnmarshalTo(&ns)
		println("detail-namespace", ns.GetName(), ns.GetFqn(), uerr == nil)
	}
	got, err = attrs.GetAttribute(ctx, &attributes.GetAttributeRequest{Identifier: &attributes.GetAttributeRequest_Fqn{Fqn: "forbidden"}})
	show("get-forbidden", got, err)
	got, err = attrs.GetAttribute(ctx, &attributes.GetAttributeRequest{})
	show("get-empty", got, err)

	list, err := attrs.ListAttributes(ctx, &attributes.ListAttributesRequest{Namespace: "example.com", Pagination: &policy.PageRequest{Offset: 2}})
	show("list", list, err)
	list, err = attrs.ListAttributes(ctx, &attributes.ListAttributesRequest{Namespace: "empty.example.com"})
	show("list-empty", list, err)

	read := &policy.Action{Name: "read"}
	permit, err := authz.GetDecision(ctx, &authorizationv2.GetDecisionRequest{
		EntityIdentifier: &authorizationv2.EntityIdentifier{Identifier: &authorizationv2.EntityIdentifier_Token{Token: &entity.Token{Jwt: "permit"}}},
		Action:           read,
		Resource:         &authorizationv2.Resource{EphemeralId: "r1", Resource: &authorizationv2.Resource_AttributeValues_{AttributeValues: &authorizationv2.Resource_AttributeValues{Fqns: []string{"https://example.com/attr/classification/value/secret"}}}},
	})
	show("decision-permit", permit, err)
	if err == nil {
		println("decision", permit.GetDecision().GetDecision().String(), permit.GetDecision().GetDecision() == authorizationv2.Decision_DECISION_PERMIT)
	}
	deny, err := authz.GetDecision(ctx, &authorizationv2.GetDecisionRequest{
		EntityIdentifier: &authorizationv2.EntityIdentifier{Identifier: &authorizationv2.EntityIdentifier_Token{Token: &entity.Token{Jwt: "someone"}}},
		Action:           read,
	})
	show("decision-deny", deny, err)
	deny, err = authz.GetDecision(ctx, &authorizationv2.GetDecisionRequest{
		EntityIdentifier: &authorizationv2.EntityIdentifier{Identifier: &authorizationv2.EntityIdentifier_Token{Token: &entity.Token{Jwt: "expired"}}},
	})
	show("decision-expired", deny, err)
	deny, err = authz.GetDecision(ctx, &authorizationv2.GetDecisionRequest{})
	show("decision-missing", deny, err)

	via := connect.NewClient(base)
	via.Headers = client.Headers
	pass := &passthrough{}
	via.HTTPClient = pass
	for _, path := range []string{"/raw/unavailable", "/raw/badjson", "/raw/nocode", "/raw/unknowncode", "/raw/html", "/raw/proto", "/raw/garbage", "/no.such.Service/Method"} {
		resp := &attributes.GetAttributeResponse{}
		_, err := client.CallUnary(ctx, path, &attributes.GetAttributeRequest{}, resp, nil)
		show(path, resp, err)
		resp = &attributes.GetAttributeResponse{}
		_, err = via.CallUnary(ctx, path, &attributes.GetAttributeRequest{}, resp, nil)
		show("via"+path, resp, err)
	}
	got, err = attributes.NewAttributesServiceClient(via).GetAttribute(ctx, &attributes.GetAttributeRequest{Identifier: &attributes.GetAttributeRequest_Fqn{Fqn: "missing"}})
	show("via-get-missing", got, err)
	println("via-calls", pass.calls)

	meta, err := client.CallUnary(ctx, attributes.AttributesServiceGetAttributeProcedure,
		&attributes.GetAttributeRequest{Identifier: &attributes.GetAttributeRequest_AttributeId{AttributeId: "a1"}},
		&attributes.GetAttributeResponse{}, []string{"X-Call", "call-2"})
	println("echo", header(meta, "X-Echo"), err == nil)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = attrs.GetAttribute(cancelled, &attributes.GetAttributeRequest{})
	println("cancelled", connect.CodeOf(err).String())
}
`

// TestConnectClients calls stub OpenTDF platform Connect handlers, served by
// connect-go with the platform's generated code, through clients generated by
// protoc-gen-goalchemy, and compares every target with native Go.
func TestConnectClients(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "connectserver")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = filepath.Join(root, "tests/integration/testdata/connect/server")
	build.Env = append(os.Environ(), "GOFLAGS=")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, out)
	}
	server := exec.Command(bin)
	stdin, err := server.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := server.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	server.Stderr = os.Stderr
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stdin.Close()
		done := make(chan error, 1)
		go func() { done <- server.Wait() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			server.Process.Kill()
		}
	}()
	url, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	gen := filepath.Join(root, "tests/integration/testdata/connect/gen")
	err = filepath.Walk(gen, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(gen, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dst := filepath.Join(source, "gen", rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"main.go": fmt.Sprintf(connectProbe, strconv.Quote(strings.TrimSpace(url))),
		"go.mod":  fmt.Sprintf("module connectprobe\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => %s\n", strconv.Quote(root)),
	} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	native, err := testutil.Native(source, t.TempDir())
	if err != nil || native.Exit != 0 {
		t.Fatalf("native: %v\n%s", err, native)
	}
	want := testutil.Normalize(native)
	for _, line := range []string{
		"rule ATTRIBUTE_RULE_TYPE_ENUM_HIERARCHY values 2 active true ns example.com created 2024-05-06T12:53:20.12Z owner security\n",
		"get-missing error not_found not_found: attribute not found: missing reason= 1\n",
		"detail-namespace example.com https://example.com true\n",
		"get-forbidden error permission_denied permission_denied: caller may not read this attribute reason=policy 0\n",
		"decision DECISION_PERMIT true\n",
		"/raw/unavailable error unavailable unavailable: 503 Service Unavailable reason= 0\n",
		"/raw/badjson error unknown unknown: 500 Internal Server Error reason= 0\n",
		"/raw/nocode error unavailable unavailable: slow down reason= 0\n",
		"/raw/unknowncode error unauthenticated unauthenticated: 401 Unauthorized reason= 0\n",
		"/raw/html error unknown unknown: invalid content-type: \"text/html\"; expecting \"application/json\" reason= 0\n",
		"/raw/proto error internal internal: invalid content-type: \"application/proto\"; expecting \"application/json\" reason= 0\n",
		"/no.such.Service/Method error unimplemented unimplemented: 404 Not Found reason= 0\n",
		"echo probe-1|call-2 true\n",
		"cancelled canceled\n",
		"via-get-missing error not_found not_found: attribute not found: missing reason= 1\n",
		"via-calls 9\n",
	} {
		if !strings.Contains(want.Stderr, line) {
			t.Errorf("native output lacks %q:\n%s", line, want.Stderr)
		}
		if strings.HasPrefix(line, "/") && !strings.Contains(want.Stderr, "via"+line) {
			t.Errorf("native output through HTTPClient lacks %q:\n%s", "via"+line, want.Stderr)
		}
	}
	targets := []string{"go", "typescript", "python", "java", "csharp", "rust", "c", "swift"}
	if only := os.Getenv("GOALCHEMY_TEST_TARGETS"); only != "" {
		targets = strings.Split(only, ",")
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			out := t.TempDir()
			if ds := testutil.CompileGate(source, target, out, "cooperative"); len(ds) > 0 {
				t.Fatal(ds)
			}
			got, err := testutil.Runners[target](out)
			if err != nil {
				t.Fatal(err)
			}
			if g := testutil.Normalize(got); g != want {
				t.Errorf("%s differs from Go\n=== go\n%s=== %s\n%s", target, want, target, g)
			}
		})
	}
}
