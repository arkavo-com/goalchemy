package apidoc

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func descriptorFixture(t *testing.T) []byte {
	t.Helper()
	field := func(name string, number int32, kind descriptorpb.FieldDescriptorProto_Type, typeName string) *descriptorpb.FieldDescriptorProto {
		f := &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(number), Type: kind.Enum()}
		if typeName != "" {
			f.TypeName = proto.String(typeName)
		}
		return f
	}
	request := &descriptorpb.DescriptorProto{Name: proto.String("ListNotesRequest"), OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: proto.String("identifier")}, {Name: proto.String("_active")}}, Field: []*descriptorpb.FieldDescriptorProto{
		field("scope", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, ""),
		field("pagination", 2, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, ".example.v1.Page"),
		field("labels", 3, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, ".example.v1.ListNotesRequest.LabelsEntry"),
		field("name", 4, descriptorpb.FieldDescriptorProto_TYPE_STRING, ""),
		field("id", 5, descriptorpb.FieldDescriptorProto_TYPE_STRING, ""),
		field("active", 6, descriptorpb.FieldDescriptorProto_TYPE_BOOL, ""),
		field("state", 7, descriptorpb.FieldDescriptorProto_TYPE_ENUM, ".example.v1.State"),
	}, NestedType: []*descriptorpb.DescriptorProto{{Name: proto.String("LabelsEntry"), Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)}, Field: []*descriptorpb.FieldDescriptorProto{
		field("key", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, ""), field("value", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING, ""),
	}}}}
	request.Field[2].Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	request.Field[3].OneofIndex, request.Field[4].OneofIndex = proto.Int32(0), proto.Int32(0)
	request.Field[5].OneofIndex, request.Field[5].Proto3Optional = proto.Int32(1), proto.Bool(true)
	response := &descriptorpb.DescriptorProto{Name: proto.String("ListNotesResponse"), Field: []*descriptorpb.FieldDescriptorProto{field("notes", 1, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, ".example.v1.Note")}}
	response.Field[0].Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	note := &descriptorpb.DescriptorProto{Name: proto.String("Note"), Field: []*descriptorpb.FieldDescriptorProto{field("title", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, ""), field("child", 2, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, ".example.v1.Note")}}
	page := &descriptorpb.DescriptorProto{Name: proto.String("Page"), Field: []*descriptorpb.FieldDescriptorProto{field("page_size", 1, descriptorpb.FieldDescriptorProto_TYPE_INT32, "")}}
	page.Field[0].Options = &descriptorpb.FieldOptions{Deprecated: proto.Bool(true)}
	fd := &descriptorpb.FileDescriptorProto{Name: proto.String("example/v1/notes.proto"), Package: proto.String("example.v1"), Syntax: proto.String("proto3"), MessageType: []*descriptorpb.DescriptorProto{request, response, note, page}, EnumType: []*descriptorpb.EnumDescriptorProto{{Name: proto.String("State"), Value: []*descriptorpb.EnumValueDescriptorProto{{Name: proto.String("STATE_UNSPECIFIED"), Number: proto.Int32(0)}, {Name: proto.String("STATE_ACTIVE"), Number: proto.Int32(1)}}}}, Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("NoteService"), Method: []*descriptorpb.MethodDescriptorProto{
		{Name: proto.String("ListNotes"), InputType: proto.String(".example.v1.ListNotesRequest"), OutputType: proto.String(".example.v1.ListNotesResponse")},
		{Name: proto.String("WatchNotes"), InputType: proto.String(".example.v1.ListNotesRequest"), OutputType: proto.String(".example.v1.ListNotesResponse"), ServerStreaming: proto.Bool(true), Options: &descriptorpb.MethodOptions{Deprecated: proto.Bool(true)}},
	}}}, SourceCodeInfo: &descriptorpb.SourceCodeInfo{Location: []*descriptorpb.SourceCodeInfo_Location{
		{Path: []int32{6, 0}, Span: []int32{4, 0, 20}, LeadingComments: proto.String("Manage notes.")},
		{Path: []int32{6, 0, 2, 0}, Span: []int32{5, 2, 30}, LeadingComments: proto.String("List notes in the caller's scope. Literal <script> and {value} are text.")},
		{Path: []int32{4, 0, 2, 0}, Span: []int32{10, 2, 30}, LeadingComments: proto.String("Scope must identify an existing workspace.")},
	}}}
	data, err := proto.Marshal(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fd}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func rpcFixture(t *testing.T) *Catalog {
	t.Helper()
	c := &Catalog{SchemaVersion: 1, Title: "Notes SDK", Packages: []Package{}, Symbols: []*Symbol{}, Targets: []Target{}}
	if err := c.AddDescriptors(descriptorFixture(t), []string{"example.v1.NoteService"}); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRPCSchemasAndPortableOutput(t *testing.T) {
	c := rpcFixture(t)
	r := c.RPC
	if len(r.Services) != 1 || len(r.Messages) != 4 || len(r.Enums) != 1 {
		t.Fatalf("unexpected reachable graph: %+v", r)
	}
	s := r.Services[0]
	if s.Description != "Manage notes." || s.Location.Line != 5 || !s.Methods[1].ServerStreaming || !s.Methods[1].Deprecated {
		t.Fatal("comments, locations, or streaming metadata lost")
	}
	var req RPCMessage
	for _, m := range r.Messages {
		if m.ID == "example.v1.ListNotesRequest" {
			req = m
		}
	}
	if req.Fields[2].MapKey != "string" || req.Fields[2].Repeated || req.Fields[3].Oneof != "identifier" || req.Fields[5].Oneof != "" || !req.Fields[5].Presence {
		t.Fatalf("incorrect map or presence metadata: %+v", req.Fields)
	}
	for _, m := range r.Messages {
		if m.ID == "example.v1.Page" && (m.Fields[0].JSONName != "pageSize" || !m.Fields[0].Deprecated || len(m.Fields[0].Options) == 0) {
			t.Fatal("field metadata lost")
		}
	}
	pages := c.Markdown()
	assertLinks(t, pages)
	joined := string(bytes.Join(mapValues(pages), nil))
	if strings.Contains(joined, "<script>") || !strings.Contains(joined, "oneof `identifier`") || !strings.Contains(joined, "stream example.v1.ListNotesResponse") {
		t.Fatal("unsafe prose or missing schema shape")
	}
	if strings.Contains(joined, "Example request") {
		t.Fatal("generator invented example data")
	}
	raw, _ := json.Marshal(c)
	var restored Catalog
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err := restored.Check(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c.Markdown()["rpc/index.md"], restored.Markdown()["rpc/index.md"]) {
		t.Fatal("JSON roundtrip changed RPC reference")
	}
	out := t.TempDir()
	if err := c.Write(out, "site"); err != nil {
		t.Fatal(err)
	}
	for file, body := range c.pages(true) {
		if strings.Contains(file, "methods/") && (!bytes.Contains(body, []byte("<RpcFields")) || bytes.Contains(body, []byte("{value}"))) {
			t.Fatal("missing viewer or unsafe MDX expression")
		}
	}
	if _, err := os.Stat(filepath.Join(out, "src/components/RpcFields.astro")); err != nil {
		t.Fatal(err)
	}
}

func TestRPCContentValidation(t *testing.T) {
	valid := `{"examples":[{"id":"list-notes","title":"List notes","for":"example.v1.NoteService/ListNotes","snippets":[{"target":"go","code":"response, err := client.ListNotes(ctx, request)"},{"target":"typescript","code":"const response = await client.listNotes(request);"}],"request":{"scope":"personal","pagination":{"pageSize":10},"labels":{"team":"docs"},"state":"STATE_ACTIVE"},"response":{"notes":[{"title":"Example note"}]}}],"rpc_bindings":[{"method":"example.v1.NoteService/ListNotes","bindings":[{"target":"go","name":"ListNotes","signature":"ListNotes(ctx context.Context, request *ListNotesRequest) (*ListNotesResponse, error)"}]}]}`
	c := rpcFixture(t)
	if err := c.AddContent([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	assertLinks(t, c.Markdown())
	for _, invalid := range []string{
		strings.Replace(valid, `"scope":"personal"`, `"unknown":"personal"`, 1),
		strings.Replace(valid, `"pageSize":10`, `"pageSize":"many"`, 1),
		strings.Replace(valid, `"STATE_ACTIVE"`, `"INVALID_STATE"`, 1),
		strings.Replace(valid, `"scope":"personal"`, `"name":"one","id":"two"`, 1),
		strings.ReplaceAll(valid, "example.v1.NoteService/ListNotes", "example.v1.NoteService/Missing"),
		strings.Replace(valid, `"target":"typescript"`, `"target":"go"`, 1),
		strings.Replace(valid, `"target":"typescript"`, `"target":"kotlin"`, 1),
		strings.Replace(valid, `"title":"List notes"`, `"titel":"List notes"`, 1),
		valid + `{}`,
	} {
		if err := rpcFixture(t).AddContent([]byte(invalid)); err == nil {
			t.Fatalf("accepted invalid content: %s", invalid)
		}
	}
	pages := c.pages(true)
	for file, body := range pages {
		if strings.Contains(file, "methods/") && strings.Contains(string(body), "List notes") && (!strings.Contains(string(body), "<LanguageTabs") || !strings.Contains(string(body), "Example response")) {
			t.Fatal("usage examples missing from RPC method")
		}
	}
}

func TestRPCDescriptorFailures(t *testing.T) {
	for _, selected := range [][]string{{"missing.Service"}, {""}} {
		c := &Catalog{SchemaVersion: 1}
		if err := c.AddDescriptors(descriptorFixture(t), selected); err == nil {
			t.Fatal("unknown service accepted")
		}
	}
	c := &Catalog{SchemaVersion: 1}
	if err := c.AddDescriptors([]byte("not protobuf"), nil); err == nil {
		t.Fatal("malformed descriptor accepted")
	}
	set := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(descriptorFixture(t), set); err != nil {
		t.Fatal(err)
	}
	set.File[0].Dependency = []string{"missing.proto"}
	data, _ := proto.Marshal(set)
	if err := c.AddDescriptors(data, nil); err == nil {
		t.Fatal("missing descriptor imports accepted")
	}
}

func TestMDXPreservesCodeAndEscapesProse(t *testing.T) {
	input := "Text {value}.\n\n`{inline}`\n\n```go\nreturn Item{}\n```\n\nimport literal text\nexport literal text\n"
	output := escapeMDX(input)
	for _, text := range []string{"Text &#123;value&#125;.", "`{inline}`", "return Item{}", "&#105;mport literal text", "&#101;xport literal text"} {
		if !strings.Contains(output, text) {
			t.Fatalf("lost literal code or unsafe MDX prose: %s", output)
		}
	}
}

func TestSourceAndTaskUsageExamples(t *testing.T) {
	c := &Catalog{SchemaVersion: 1, Title: "SDK", Packages: []Package{{ID: "example/sdk", Name: "sdk", Root: true, Symbols: []string{"example/sdk#Encrypt"}}}, Symbols: []*Symbol{{ID: "example/sdk#Encrypt", Package: "example/sdk", Name: "Encrypt", Kind: "function", Signature: "func Encrypt()", Root: true}}}
	content := []byte(`{"examples":[{"id":"encrypt","title":"Encrypt bytes","for":"example/sdk#Encrypt","snippets":[{"target":"go","code":"sdk.Encrypt()"}]},{"id":"setup","title":"Configure your application","snippets":[{"target":"typescript","code":"const endpoint = 'https://example.com';"}]}]}`)
	if err := c.AddContent(content); err != nil {
		t.Fatal(err)
	}
	assertLinks(t, c.Markdown())
	for file, body := range c.pages(true) {
		if strings.HasPrefix(file, "symbols/") && (!strings.Contains(string(body), "Encrypt bytes") || strings.Contains(string(body), "Configure your application")) {
			t.Fatal("examples were not attached to their intended API")
		}
	}
	invalid := strings.Replace(string(content), `"for":"example/sdk#Encrypt"`, `"for":"example/sdk#Missing"`, 1)
	if err := c.AddContent([]byte(invalid)); err == nil {
		t.Fatal("unknown source symbol accepted")
	}
}

func TestRPCMessageValidationExtensions(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "protoc", "testdata", "platform.binpb"))
	if err != nil {
		t.Fatal(err)
	}
	c := &Catalog{SchemaVersion: 1}
	if err := c.AddDescriptors(data, []string{"policy.attributes.AttributesService"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range c.RPC.Messages {
		if m.ID != "policy.attributes.GetAttributeRequest" {
			continue
		}
		if !strings.Contains(string(m.Options), "exclusive_fields") || !strings.Contains(string(m.Options), "required_fields") || len(m.Oneofs) != 1 || m.Oneofs[0].Name != "identifier" {
			t.Fatalf("message validation or oneof metadata lost: %+v", m)
		}
		pages := c.Markdown()
		r := newRenderer(c)
		r.rpcFiles()
		body := pages[r.files[m.ID]]
		if !strings.Contains(string(body), "required_fields") {
			t.Fatal("portable reference lost message validation")
		}
		return
	}
	t.Fatal("expected request missing")
}
