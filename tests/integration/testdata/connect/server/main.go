// Command server serves stub OpenTDF platform Connect handlers for the
// connect integration test. It prints its base URL and serves until stdin
// closes.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"

	"connectrpc.com/connect"
	authorizationv2 "github.com/opentdf/platform/protocol/go/authorization/v2"
	"github.com/opentdf/platform/protocol/go/authorization/v2/authorizationv2connect"
	"github.com/opentdf/platform/protocol/go/common"
	"github.com/opentdf/platform/protocol/go/policy"
	"github.com/opentdf/platform/protocol/go/policy/attributes"
	"github.com/opentdf/platform/protocol/go/policy/attributes/attributesconnect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type attributesServer struct {
	attributesconnect.UnimplementedAttributesServiceHandler
}

func classification() *policy.Attribute {
	ns := &policy.Namespace{Id: "ns1", Name: "example.com", Fqn: "https://example.com", Active: wrapperspb.Bool(true)}
	return &policy.Attribute{
		Id:        "a1",
		Namespace: ns,
		Name:      "classification",
		Rule:      policy.AttributeRuleTypeEnum_ATTRIBUTE_RULE_TYPE_ENUM_HIERARCHY,
		Values: []*policy.Value{
			{Id: "v1", Value: "topsecret", Fqn: "https://example.com/attr/classification/value/topsecret"},
			{Id: "v2", Value: "secret", Fqn: "https://example.com/attr/classification/value/secret", Active: wrapperspb.Bool(false)},
		},
		Fqn:    "https://example.com/attr/classification",
		Active: wrapperspb.Bool(true),
		Metadata: &common.Metadata{
			CreatedAt: &timestamppb.Timestamp{Seconds: 1715000000, Nanos: 120000000},
			Labels:    map[string]string{"owner": "security", "tier": "1"},
		},
	}
}

func (attributesServer) GetAttribute(ctx context.Context, req *connect.Request[attributes.GetAttributeRequest]) (*connect.Response[attributes.GetAttributeResponse], error) {
	key := req.Msg.GetFqn()
	if key == "" {
		key = req.Msg.GetAttributeId()
	}
	switch key {
	case "a1", "https://example.com/attr/classification":
		resp := connect.NewResponse(&attributes.GetAttributeResponse{Attribute: classification()})
		resp.Header().Set("X-Echo", req.Header().Get("X-Probe")+"|"+req.Header().Get("X-Call"))
		_, deadline := ctx.Deadline()
		resp.Header().Set("X-Deadline", strconv.FormatBool(deadline))
		return resp, nil
	case "forbidden":
		err := connect.NewError(connect.CodePermissionDenied, errors.New("caller may not read this attribute"))
		err.Meta().Set("X-Reason", "policy")
		return nil, err
	case "":
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("identifier is required"))
	}
	err := connect.NewError(connect.CodeNotFound, fmt.Errorf("attribute not found: %s", key))
	detail, derr := connect.NewErrorDetail(&policy.Namespace{Name: "example.com", Fqn: "https://example.com"})
	if derr != nil {
		panic(derr)
	}
	err.AddDetail(detail)
	return nil, err
}

func (attributesServer) ListAttributes(_ context.Context, req *connect.Request[attributes.ListAttributesRequest]) (*connect.Response[attributes.ListAttributesResponse], error) {
	attrs := []*policy.Attribute{classification(), {Id: "a2", Name: "releasable", Rule: policy.AttributeRuleTypeEnum_ATTRIBUTE_RULE_TYPE_ENUM_ANY_OF}}
	if req.Msg.GetNamespace() == "empty.example.com" {
		attrs = nil
	}
	return connect.NewResponse(&attributes.ListAttributesResponse{
		Attributes: attrs,
		Pagination: &policy.PageResponse{CurrentOffset: req.Msg.GetPagination().GetOffset(), Total: int32(len(attrs))},
	}), nil
}

type authorizationServer struct {
	authorizationv2connect.UnimplementedAuthorizationServiceHandler
}

func (authorizationServer) GetDecision(_ context.Context, req *connect.Request[authorizationv2.GetDecisionRequest]) (*connect.Response[authorizationv2.GetDecisionResponse], error) {
	token := req.Msg.GetEntityIdentifier().GetToken().GetJwt()
	if token == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("entity_identifier is required"))
	}
	if token == "expired" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("token expired"))
	}
	decision := &authorizationv2.ResourceDecision{EphemeralResourceId: req.Msg.GetResource().GetEphemeralId(), Decision: authorizationv2.Decision_DECISION_DENY}
	if token == "permit" && req.Msg.GetAction().GetName() == "read" {
		decision.Decision = authorizationv2.Decision_DECISION_PERMIT
		decision.RequiredObligations = []string{"https://example.com/obl/watermark/value/visible"}
	}
	return connect.NewResponse(&authorizationv2.GetDecisionResponse{Decision: decision}), nil
}

func main() {
	mux := http.NewServeMux()
	mux.Handle(attributesconnect.NewAttributesServiceHandler(attributesServer{}))
	mux.Handle(authorizationv2connect.NewAuthorizationServiceHandler(authorizationServer{}))
	mux.HandleFunc("/raw/unavailable", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, "maintenance")
	})
	mux.HandleFunc("/raw/badjson", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "not json")
	})
	mux.HandleFunc("/raw/nocode", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"message":"slow down"}`)
	})
	mux.HandleFunc("/raw/unknowncode", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"code":"no_such_code","message":"x"}`)
	})
	mux.HandleFunc("/raw/html", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html></html>")
	})
	mux.HandleFunc("/raw/proto", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/proto")
		io.WriteString(w, "\x0a\x00")
	})
	mux.HandleFunc("/raw/garbage", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"attribute":7}`)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	fmt.Printf("http://%s\n", ln.Addr())
	go http.Serve(ln, mux)
	io.Copy(io.Discard, os.Stdin)
}
