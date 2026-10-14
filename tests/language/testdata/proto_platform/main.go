package main

import (
	"github.com/eugenioenko/goalchemy/std/encoding/json"
	"github.com/eugenioenko/goalchemy/std/encoding/jsonvalue"
	"github.com/eugenioenko/goalchemy/std/encoding/protojson"
	"github.com/eugenioenko/goalchemy/tests/language/testdata/proto_platform/authorization"
	authorizationv2 "github.com/eugenioenko/goalchemy/tests/language/testdata/proto_platform/authorization/v2"
	"github.com/eugenioenko/goalchemy/tests/language/testdata/proto_platform/common"
	"github.com/eugenioenko/goalchemy/tests/language/testdata/proto_platform/entity"
	"github.com/eugenioenko/goalchemy/tests/language/testdata/proto_platform/kas"
	"github.com/eugenioenko/goalchemy/tests/language/testdata/proto_platform/policy"
	"github.com/eugenioenko/goalchemy/tests/language/testdata/proto_platform/policy/attributes"
)

type testCase struct {
	typ, input, want string
}

func message(typ string) protojson.Message {
	switch typ {
	case "policy.attributes.ListAttributesResponse":
		return &attributes.ListAttributesResponse{}
	case "policy.attributes.GetAttributeRequest":
		return &attributes.GetAttributeRequest{}
	case "policy.attributes.GetAttributeResponse":
		return &attributes.GetAttributeResponse{}
	case "policy.Attribute":
		return &policy.Attribute{}
	case "kas.RewrapRequest":
		return &kas.RewrapRequest{}
	case "kas.UnsignedRewrapRequest":
		return &kas.UnsignedRewrapRequest{}
	case "kas.RewrapResponse":
		return &kas.RewrapResponse{}
	case "kas.PublicKeyRequest":
		return &kas.PublicKeyRequest{}
	case "kas.PublicKeyResponse":
		return &kas.PublicKeyResponse{}
	case "kas.PolicyBinding":
		return &kas.PolicyBinding{}
	case "kas.KeyAccess":
		return &kas.KeyAccess{}
	case "kas.PolicyRewrapResult":
		return &kas.PolicyRewrapResult{}
	case "policy.Value":
		return &policy.Value{}
	case "authorization.v2.GetDecisionRequest":
		return &authorizationv2.GetDecisionRequest{}
	case "authorization.v2.GetDecisionResponse":
		return &authorizationv2.GetDecisionResponse{}
	case "authorization.GetDecisionsRequest":
		return &authorization.GetDecisionsRequest{}
	case "authorization.GetDecisionsResponse":
		return &authorization.GetDecisionsResponse{}
	case "common.Metadata":
		return &common.Metadata{}
	case "entity.Entity":
		return &entity.Entity{}
	case "kas.InfoRequest":
		return &kas.InfoRequest{}
	case "authorization.GetEntitlementsRequest":
		return &authorization.GetEntitlementsRequest{}
	case "authorization.v2.GetEntitlementsRequest":
		return &authorizationv2.GetEntitlementsRequest{}
	}
	panic("unknown type " + typ)
}

func roundTrip(c testCase) string {
	m := message(c.typ)
	if err := protojson.Unmarshal([]byte(c.input), m); err != nil {
		return "error: " + err.Error()
	}
	out, err := protojson.Marshal(m)
	if err != nil {
		return "error: " + err.Error()
	}
	return string(out)
}

func main() {
	failures := 0
	for i, c := range cases {
		got := roundTrip(c)
		ok := got == c.want
		if c.want == "" {
			ok = len(got) > 7 && got[:7] == "error: "
			println(i, c.typ, got)
		}
		if !ok {
			failures++
			println("MISMATCH", i, c.typ)
			println("  got:", got)
			println(" want:", c.want)
		}
	}

	active := true
	attr := &policy.Attribute{
		Id:        "a1",
		Name:      "classification",
		Rule:      policy.AttributeRuleTypeEnum_ATTRIBUTE_RULE_TYPE_ENUM_HIERARCHY,
		Values:    []*policy.Value{{Value: "secret"}, {Value: "top", Active: &active}},
		Namespace: &policy.Namespace{Name: "example.com", Metadata: &common.Metadata{Labels: map[string]string{"z": "1", "a": "2"}}},
		Active:    &active,
	}
	out, err := protojson.Marshal(attr)
	println(string(out), err == nil)
	viaJSON, err := json.Marshal(attr)
	println(string(viaJSON) == string(out), err == nil)

	req := &authorizationv2.GetDecisionRequest{EntityIdentifier: &authorizationv2.EntityIdentifier{
		Identifier: &authorizationv2.EntityIdentifier_Token{Token: &entity.Token{Jwt: "eyJ"}},
	}}
	out, _ = protojson.Marshal(req)
	println(string(out))
	println(req.GetEntityIdentifier().GetToken().GetJwt(), req.GetAction().GetName() == "", attr.GetNamespace().GetMetadata().GetLabels()["a"])

	resp := &kas.RewrapResponse{Metadata: map[string]jsonvalue.Value{"b": jsonvalue.Number("2.50"), "a": jsonvalue.Object("z", jsonvalue.Null(), "y", jsonvalue.Array(jsonvalue.Bool(true)))}}
	out, err = protojson.Marshal(resp)
	println(string(out), err == nil)

	ts := &protojson.Timestamp{Seconds: 1715000000, Nanos: 120000000}
	md := &common.Metadata{CreatedAt: ts}
	out, _ = protojson.Marshal(md)
	println(string(out), md.GetCreatedAt().AsTime().Format("2006-01-02T15:04:05Z07:00"))

	any, err := protojson.NewAny("type.googleapis.com/common.Metadata", md)
	ent := &entity.Entity{EntityType: &entity.Entity_Claims{Claims: any}, Category: entity.Entity_CATEGORY_SUBJECT}
	out, _ = protojson.Marshal(ent)
	println(string(out), err == nil)
	var back common.Metadata
	err = ent.GetClaims().UnmarshalTo(&back)
	println(back.GetCreatedAt().Seconds, back.GetCreatedAt().Nanos, err == nil)

	println("cases", len(cases), "failures", failures)
}
