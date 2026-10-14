package protoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/pluginpb"
)

var update = os.Getenv("GOALCHEMY_UPDATE_PROTO") != ""

func loadSet(t testing.TB) *descriptorpb.FileDescriptorSet {
	t.Helper()
	b, err := os.ReadFile("testdata/platform.binpb")
	if err != nil {
		t.Fatal(err)
	}
	set := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(b, set); err != nil {
		t.Fatal(err)
	}
	return set
}

func platformFiles(set *descriptorpb.FileDescriptorSet) []string {
	var names []string
	for _, f := range set.File {
		n := f.GetName()
		if strings.HasPrefix(n, "google/") || strings.HasPrefix(n, "buf/") {
			continue
		}
		names = append(names, n)
	}
	return names
}

func generate(t testing.TB, set *descriptorpb.FileDescriptorSet, param string) map[string]string {
	t.Helper()
	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: platformFiles(set),
		Parameter:      proto.String(param),
		ProtoFile:      set.File,
	}
	var opts Options
	plugin, err := protogen.Options{ParamFunc: opts.Set}.New(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := Run(plugin, opts); err != nil {
		t.Fatal(err)
	}
	resp := plugin.Response()
	if resp.Error != nil {
		t.Fatal(resp.GetError())
	}
	out := map[string]string{}
	for _, f := range resp.File {
		out[f.GetName()] = f.GetContent()
	}
	return out
}

const fixture = "../../tests/language/testdata/proto_platform"

const fixtureImport = "github.com/eugenioenko/goalchemy/tests/language/testdata/proto_platform"

var fixtureTypes = []string{
	"policy.attributes.ListAttributesResponse",
	"policy.attributes.GetAttributeRequest",
	"policy.attributes.GetAttributeResponse",
	"kas.RewrapRequest",
	"kas.UnsignedRewrapRequest",
	"kas.RewrapResponse",
	"kas.PublicKeyRequest",
	"kas.PublicKeyResponse",
	"authorization.v2.GetDecisionRequest",
	"authorization.v2.GetDecisionResponse",
	"authorization.GetDecisionsRequest",
	"authorization.GetDecisionsResponse",
}

var corpusExtraTypes = []string{"policy.Attribute", "policy.Value", "common.Metadata", "entity.Entity", "kas.KeyAccess", "kas.PolicyRewrapResult",
	"kas.InfoRequest", "authorization.GetEntitlementsRequest", "authorization.v2.GetEntitlementsRequest"}

func fixtureParam() string {
	return "go_import_prefix=" + fixtureImport + ",include=" + strings.Join(append(append([]string{}, fixtureTypes...), corpusExtraTypes...), ";")
}

// checkGenerated compares files with the committed tree under dir, or
// rewrites it when GOALCHEMY_UPDATE_PROTO is set.
func checkGenerated(t *testing.T, dir string, files map[string]string, suffixes ...string) {
	t.Helper()
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		path := filepath.Join(dir, filepath.FromSlash(n))
		if update {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(files[n]), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, []byte(files[n])) {
			t.Errorf("%s is stale; run GOALCHEMY_UPDATE_PROTO=1 go test ./internal/protoc", path)
		}
	}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		generated := false
		for _, s := range suffixes {
			generated = generated || strings.HasSuffix(p, s)
		}
		if !generated {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if _, ok := files[filepath.ToSlash(rel)]; !ok {
			if update {
				return os.Remove(p)
			}
			t.Errorf("%s is no longer generated", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestPlatformFixture checks that the committed generated packages of the
// proto_platform fixture match the generator. Set GOALCHEMY_UPDATE_PROTO=1 to
// rewrite them.
func TestPlatformFixture(t *testing.T) {
	checkGenerated(t, fixture, generate(t, loadSet(t), fixtureParam()), ".pb.go", ".connect.go")
}

const connectGen = "../../tests/integration/testdata/connect/gen"

var connectMethods = []string{
	"policy.attributes.AttributesService.GetAttribute",
	"policy.attributes.AttributesService.ListAttributes",
	"authorization.v2.AuthorizationService.GetDecision",
}

// TestConnectFixture checks the generated clients used by the connect
// integration test.
func TestConnectFixture(t *testing.T) {
	param := "go_import_prefix=connectprobe/gen,include=" + strings.Join(connectMethods, ";")
	files := generate(t, loadSet(t), param)
	for _, want := range []string{"policy/attributes/attributes.connect.go", "authorization/v2/authorization.connect.go"} {
		if _, ok := files[want]; !ok {
			t.Fatalf("%s was not generated", want)
		}
	}
	checkGenerated(t, connectGen, files, ".pb.go", ".connect.go")
}

type corpusCase struct {
	typ, input, want string
}

const (
	minTimestamp = -62135596800
	maxTimestamp = 253402300799
)

type populator struct {
	r     *rand.Rand
	types *dynamicpb.Types
	files *protoregistry.Files
}

var sampleStrings = []string{
	"a", "héllo wörld", "<tag> & \"quoted\" \\ back", "line\nbreak\ttab\x01\x1f", "  ", "emoji 😀", "https://kas.example.com/kas",
	"https://example.com/attr/classification/value/secret", "", "0", "日本語",
}

var sampleFloats = []float64{0.1, -1.5, 1e21, 1e-7, 123456789.125, 5e-324, 3.4028234663852886e38, -0.0, 1, 1e20, 0.000001}

func (p *populator) chance(n int) bool { return p.r.Intn(n) == 0 }

func (p *populator) str() string {
	if p.chance(3) {
		b := make([]rune, p.r.Intn(6))
		for i := range b {
			b[i] = rune(0x20 + p.r.Intn(0x2fff))
			if !utf8.ValidRune(b[i]) {
				b[i] = 'x'
			}
		}
		return string(b)
	}
	return sampleStrings[p.r.Intn(len(sampleStrings))]
}

func (p *populator) scalar(fd protoreflect.FieldDescriptor) protoreflect.Value {
	r := p.r
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(r.Intn(2) == 0)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32([]int32{1, -1, 2147483647, -2147483648, r.Int31()}[r.Intn(5)])
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return protoreflect.ValueOfInt64([]int64{1, -1, 9223372036854775807, -9223372036854775808, 9007199254740993, r.Int63()}[r.Intn(6)])
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return protoreflect.ValueOfUint32([]uint32{1, 4294967295, r.Uint32()}[r.Intn(3)])
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return protoreflect.ValueOfUint64([]uint64{1, 18446744073709551615, r.Uint64()}[r.Intn(3)])
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32(float32(sampleFloats[r.Intn(len(sampleFloats))]))
	case protoreflect.DoubleKind:
		fs := append([]float64{math.NaN(), math.Inf(1), math.Inf(-1)}, sampleFloats...)
		return protoreflect.ValueOfFloat64(fs[r.Intn(len(fs))])
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(p.str())
	case protoreflect.BytesKind:
		b := make([]byte, r.Intn(9))
		r.Read(b)
		return protoreflect.ValueOfBytes(b)
	case protoreflect.EnumKind:
		vals := fd.Enum().Values()
		if p.chance(8) {
			return protoreflect.ValueOfEnum(77)
		}
		return protoreflect.ValueOfEnum(vals.Get(r.Intn(vals.Len())).Number())
	}
	panic(fd.Kind().String())
}

func (p *populator) jsonValue(depth int) *structpb.Value {
	switch k := p.r.Intn(6); {
	case k == 0:
		return structpb.NewNullValue()
	case k == 1:
		return structpb.NewBoolValue(p.r.Intn(2) == 0)
	case k == 2:
		return structpb.NewNumberValue(sampleFloats[p.r.Intn(len(sampleFloats))])
	case k == 3 || depth > 2:
		return structpb.NewStringValue(p.str())
	case k == 4:
		l := &structpb.ListValue{}
		for i := p.r.Intn(3); i > 0; i-- {
			l.Values = append(l.Values, p.jsonValue(depth+1))
		}
		return structpb.NewListValue(l)
	default:
		return structpb.NewStructValue(p.jsonStruct(depth + 1))
	}
}

func (p *populator) jsonStruct(depth int) *structpb.Struct {
	s := &structpb.Struct{Fields: map[string]*structpb.Value{}}
	for i := p.r.Intn(4); i > 0; i-- {
		s.Fields[p.str()] = p.jsonValue(depth)
	}
	return s
}

func (p *populator) wellKnown(m protoreflect.Message, depth int) bool {
	set := func(src proto.Message) {
		b, err := proto.Marshal(src)
		if err != nil {
			panic(err)
		}
		if err := proto.Unmarshal(b, m.Interface()); err != nil {
			panic(err)
		}
	}
	switch m.Descriptor().FullName() {
	case "google.protobuf.Timestamp":
		nanos := []int32{0, 123000000, 123456000, 123456789, 5}[p.r.Intn(5)]
		set(&timestamppb.Timestamp{Seconds: minTimestamp + p.r.Int63n(maxTimestamp-minTimestamp), Nanos: nanos})
	case "google.protobuf.Duration":
		secs := p.r.Int63n(315576000000)
		nanos := []int32{0, 500000000, 1000, 7}[p.r.Intn(4)]
		if p.chance(2) {
			secs, nanos = -secs, -nanos
		}
		set(&durationpb.Duration{Seconds: secs, Nanos: nanos})
	case "google.protobuf.Struct":
		set(p.jsonStruct(depth))
	case "google.protobuf.Value":
		set(p.jsonValue(depth))
	case "google.protobuf.ListValue":
		set(p.jsonValue(3).GetListValue())
	case "google.protobuf.Any":
		inner := []protoreflect.FullName{"common.Metadata", "policy.Namespace", "entity.Entity"}[p.r.Intn(3)]
		mt, err := p.types.FindMessageByName(inner)
		if err != nil {
			panic(err)
		}
		im := mt.New()
		p.populate(im, depth+1)
		b, err := proto.Marshal(im.Interface())
		if err != nil {
			panic(err)
		}
		fields := m.Descriptor().Fields()
		m.Set(fields.ByName("type_url"), protoreflect.ValueOfString("type.googleapis.com/"+string(inner)))
		m.Set(fields.ByName("value"), protoreflect.ValueOfBytes(b))
	default:
		return false
	}
	return true
}

func (p *populator) populate(m protoreflect.Message, depth int) {
	if p.wellKnown(m, depth) {
		return
	}
	fields := m.Descriptor().Fields()
	oneofDone := map[protoreflect.FullName]bool{}
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if o := fd.ContainingOneof(); o != nil && !o.IsSynthetic() {
			if oneofDone[o.FullName()] || !p.chance(2) {
				continue
			}
			oneofDone[o.FullName()] = true
		} else if p.chance(3) {
			continue
		}
		isMsg := fd.Message() != nil && !fd.IsMap()
		if (isMsg || fd.IsMap() && fd.MapValue().Message() != nil) && depth >= 3 {
			continue
		}
		switch {
		case fd.IsList():
			l := m.Mutable(fd).List()
			for n := p.r.Intn(3); n > 0; n-- {
				if isMsg {
					e := l.NewElement()
					p.populate(e.Message(), depth+1)
					l.Append(e)
				} else {
					l.Append(p.scalar(fd))
				}
			}
		case fd.IsMap():
			mp := m.Mutable(fd).Map()
			for n := p.r.Intn(3); n > 0; n-- {
				key := p.scalar(fd.MapKey()).MapKey()
				if fd.MapValue().Message() != nil {
					v := mp.NewValue()
					p.populate(v.Message(), depth+1)
					mp.Set(key, v)
				} else {
					mp.Set(key, p.scalar(fd.MapValue()))
				}
			}
		case isMsg:
			v := m.NewField(fd)
			p.populate(v.Message(), depth+1)
			m.Set(fd, v)
		default:
			m.Set(fd, p.scalar(fd))
		}
	}
}

func compact(t testing.TB, b []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// lenientCases are non-canonical inputs: proto names, numbers in strings,
// unknown fields and enum names, nulls, URL-safe base64 and invalid values.
var lenientCases = []struct{ typ, input string }{
	{"kas.PublicKeyRequest", `{"algorithm":"rsa:2048","fmt":"pkcs8","v":"2","unknownField":[1,{"x":null}]}`},
	{"kas.PublicKeyRequest", `{"algorithm":null,"fmt":"jwk"}`},
	{"kas.PublicKeyRequest", `{"algorithm":7}`},
	{"kas.PolicyBinding", `{"algorithm":"HS256","hash":"abc"}`},
	{"kas.PolicyBinding", `{"alg":"HS256","algorithm":"HS256"}`},
	{"kas.KeyAccess", `{"wrapped_key":"-_-_","header":"AQID","kas_url":"https://kas"}`},
	{"kas.KeyAccess", `{"wrappedKey":"AQ","header":"AQI="}`},
	{"kas.KeyAccess", `{"wrappedKey":"not base64!"}`},
	{"kas.RewrapResponse", `{"metadata":{"b":1.50,"a":[true,null,"x"],"c":{"z":1e2}},"entityWrappedKey":"AA==","sessionPublicKey":"k","schemaVersion":"1"}`},
	{"kas.RewrapResponse", `{"metadata":{"bad":1e400}}`},
	{"kas.RewrapResponse", `{"metadata":null}`},
	{"kas.RewrapResponse", `{"metadata":{"k":null}}`},
	{"policy.Attribute", `{"rule":"ATTRIBUTE_RULE_TYPE_ENUM_HIERARCHY","active":false,"values":[],"namespace":null}`},
	{"policy.Attribute", `{"rule":2,"active":true,"fqn":"https://example.com/attr/a"}`},
	{"policy.Attribute", `{"rule":"NO_SUCH_RULE","name":"n"}`},
	{"policy.Attribute", `{"rule":1.5}`},
	{"policy.Attribute", `{"values":[null]}`},
	{"policy.Attribute", `{"values":{"id":"x"}}`},
	{"policy.Attribute", `{"createdAt":"x"}`},
	{"policy.Attribute", `"not an object"`},
	{"common.Metadata", `{"createdAt":"2024-05-06T07:08:09.123456789+02:00","updatedAt":"2024-05-06T07:08:09Z","labels":{"z":"1","a":"2"}}`},
	{"common.Metadata", `{"created_at":"1970-01-01T00:00:00.1Z","labels":{"a":null}}`},
	{"common.Metadata", `{"createdAt":"2024-05-06T07:08:09.1234567891Z"}`},
	{"common.Metadata", `{"createdAt":"0000-12-31T23:59:59Z"}`},
	{"common.Metadata", `{"labels":{"a":1}}`},
	{"authorization.v2.GetDecisionRequest", `{"entityIdentifier":{"token":{"jwt":"t"}},"action":{"name":"read"},"resource":{"attributeValues":{"fqns":["https://a"]}}}`},
	{"authorization.v2.GetDecisionRequest", `{"entity_identifier":{"entityChain":{"entities":[{"emailAddress":"a@b.c"},{"userName":"u","category":"CATEGORY_SUBJECT"}]}}}`},
	{"authorization.v2.GetDecisionRequest", `{"entityIdentifier":{"token":{"jwt":"t"},"withRequestToken":true}}`},
	{"authorization.v2.GetDecisionRequest", `{"entityIdentifier":{"registeredResourceValueFqn":null,"withRequestToken":true}}`},
	{"authorization.GetDecisionsRequest", `{"decisionRequests":[{"actions":[{"standard":"STANDARD_ACTION_DECRYPT"}],"resourceAttributes":[{"attributeValueFqns":["a"],"resourceAttributesId":"r"}]}]}`},
	{"entity.Entity", `{"claims":{"@type":"type.googleapis.com/common.Metadata","labels":{"k":"v"}},"category":"CATEGORY_ENVIRONMENT","ephemeralId":"e"}`},
	{"entity.Entity", `{"claims":{}}`},
	{"entity.Entity", `{"claims":{"@type":"type.googleapis.com/google.protobuf.Timestamp","value":"2024-01-01T00:00:00.500Z"}}`},
	{"entity.Entity", `{"claims":{"@type":"type.googleapis.com/policy.Namespace","name":"n","metadata":{"labels":{"b":"1","a":"2"}},"active":true}}`},
	{"entity.Entity", `{"claims":{"@type":"type.googleapis.com/google.protobuf.Struct","value":{"b":[1,"x"],"a":null}}}`},
	{"policy.attributes.GetAttributeRequest", `{"id":"0b1f","fqn":"x"}`},
	{"policy.attributes.ListAttributesResponse", `{"attributes":[{"id":"1","values":[{"value":"v","active":{"value":true}}]}],"pagination":{"currentOffset":3,"nextOffset":"4","total":5.0}}`},
	{"policy.attributes.ListAttributesResponse", `{"pagination":{"total":5.5}}`},
	{"policy.attributes.ListAttributesResponse", `{"pagination":{"total":"1e1"}}`},
	{"policy.attributes.ListAttributesResponse", `{"pagination":{"total":" 1"}}`},
	{"policy.attributes.ListAttributesResponse", `{"pagination":{"total":2147483648}}`},
	{"kas.InfoRequest", `{}`},
	{"kas.InfoRequest", `{"unknown":1,"other":{"x":[null]}}`},
	{"kas.InfoRequest", `[]`},
	{"kas.InfoRequest", `null`},
	{"authorization.v2.GetEntitlementsRequest", `{"withComprehensiveHierarchy":false}`},
	{"authorization.v2.GetEntitlementsRequest", `{"with_comprehensive_hierarchy":true}`},
	{"authorization.v2.GetEntitlementsRequest", `{"withComprehensiveHierarchy":null}`},
	{"authorization.v2.GetEntitlementsRequest", `{"withComprehensiveHierarchy":"false"}`},
	{"authorization.GetEntitlementsRequest", `{"scope":{},"withComprehensiveHierarchy":false}`},
	{"authorization.GetEntitlementsRequest", `{"scope":null,"entities":[{"id":"e"}]}`},
	{"authorization.GetEntitlementsRequest", `{"scope":{"attributeValueFqns":["a"]}}`},
}

func protojsonString(t testing.TB, types *dynamicpb.Types, m proto.Message) string {
	t.Helper()
	b, err := protojson.MarshalOptions{Resolver: types}.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return compact(t, b)
}

func buildCorpus(t testing.TB) []corpusCase {
	set := loadSet(t)
	files, err := protodesc.NewFiles(set)
	if err != nil {
		t.Fatal(err)
	}
	types := dynamicpb.NewTypes(files)
	p := &populator{r: rand.New(rand.NewSource(24)), types: types, files: files}
	var cases []corpusCase
	for _, name := range append(append([]string{}, fixtureTypes...), corpusExtraTypes...) {
		mt, err := types.FindMessageByName(protoreflect.FullName(name))
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 8; i++ {
			m := mt.New()
			p.populate(m, 0)
			s := protojsonString(t, types, m.Interface())
			cases = append(cases, corpusCase{name, s, s})
		}
	}
	for _, c := range lenientCases {
		mt, err := types.FindMessageByName(protoreflect.FullName(c.typ))
		if err != nil {
			t.Fatal(err)
		}
		m := mt.New().Interface()
		want := ""
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true, Resolver: types}).Unmarshal([]byte(c.input), m); err == nil {
			want = protojsonString(t, types, m)
		}
		cases = append(cases, corpusCase{c.typ, c.input, want})
	}
	return cases
}

func casesSource(cases []corpusCase) []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated by internal/protoc tests. DO NOT EDIT.\n\npackage main\n\nvar cases = []testCase{\n")
	for _, c := range cases {
		fmt.Fprintf(&b, "\t{%s, %s, %s},\n", strconv.Quote(c.typ), strconv.Quote(c.input), strconv.Quote(c.want))
	}
	b.WriteString("}\n")
	return b.Bytes()
}

// TestPlatformCorpus checks the committed protojson corpus of the
// proto_platform fixture: canonical round trips of random messages and
// lenient inputs, each with protojson's result ("" for an error).
func TestPlatformCorpus(t *testing.T) {
	src := casesSource(buildCorpus(t))
	path := filepath.Join(fixture, "cases.go")
	if update {
		if err := os.WriteFile(path, src, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, src) {
		t.Errorf("%s is stale; run GOALCHEMY_UPDATE_PROTO=1 go test ./internal/protoc", path)
	}
}

// TestPlatformMatchesProtojson runs the fixture natively. Each target's
// output is compared with this run by the language fixtures, so together they
// compare every target with protojson.
func TestPlatformMatchesProtojson(t *testing.T) {
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = fixture
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "failures 0\n") {
		t.Fatalf("generated code differs from protojson:\n%s", out)
	}
}

// TestProto3Optional checks that the plugin declares proto3 optional support,
// so protoc accepts such files, and that optional scalars are pointers while
// optional messages stay plain message pointers.
func TestProto3Optional(t *testing.T) {
	set := loadSet(t)
	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: platformFiles(set),
		Parameter:      proto.String("go_import_prefix=example.com/gen,include=authorization.GetEntitlementsRequest"),
		ProtoFile:      set.File,
	}
	var opts Options
	plugin, err := protogen.Options{ParamFunc: opts.Set}.New(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := Run(plugin, opts); err != nil {
		t.Fatal(err)
	}
	resp := plugin.Response()
	if resp.GetSupportedFeatures()&uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL) == 0 {
		t.Errorf("supported features %d lack FEATURE_PROTO3_OPTIONAL", resp.GetSupportedFeatures())
	}
	src := ""
	for _, f := range resp.File {
		if f.GetName() == "authorization/authorization.pb.go" {
			src = f.GetContent()
		}
	}
	for _, want := range []string{"\tScope                      *ResourceAttribute\n", "\tWithComprehensiveHierarchy *bool\n"} {
		if !strings.Contains(src, want) {
			t.Errorf("generated GetEntitlementsRequest lacks %q:\n%s", want, src)
		}
	}
}

// TestPlatformBuilds generates every message and unary client of the
// platform descriptors, including messages without fields, and builds them.
func TestPlatformBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the whole platform")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := generate(t, loadSet(t), "go_import_prefix=platformbuild/gen")
	files["../go.mod"] = fmt.Sprintf("module platformbuild\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => %s\n", strconv.Quote(root))
	for name, content := range files {
		path := filepath.Join(dir, "gen", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
