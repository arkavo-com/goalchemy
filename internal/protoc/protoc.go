// Package protoc generates Goalchemy-subset Go from protobuf descriptors: one
// struct per message with proto3 JSON encoding through std/encoding/protojson.
package protoc

import (
	"fmt"
	"path"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/pluginpb"
)

const (
	protojsonPkg = protogen.GoImportPath("github.com/eugenioenko/goalchemy/std/encoding/protojson")
	jsonvaluePkg = protogen.GoImportPath("github.com/eugenioenko/goalchemy/std/encoding/jsonvalue")
	strconvPkg   = protogen.GoImportPath("github.com/eugenioenko/goalchemy/std/strconv")
	connectPkg   = protogen.GoImportPath("github.com/eugenioenko/goalchemy/std/connect")
	contextPkg   = protogen.GoImportPath("github.com/eugenioenko/goalchemy/lib/context")
)

// Options are the plugin parameters.
type Options struct {
	// ImportPrefix places each file's package at ImportPrefix/<proto directory>
	// instead of its go_package.
	ImportPrefix string
	// Include limits generation to these messages, enums, services and
	// methods (full names) and the types they reference. Empty generates
	// everything.
	Include []string
	// NoGetters omits the nil-safe GetX accessors.
	NoGetters bool
}

// Set parses one name=value plugin parameter.
func (o *Options) Set(name, value string) error {
	switch name {
	case "go_import_prefix":
		o.ImportPrefix = strings.TrimSuffix(value, "/")
	case "include":
		for _, n := range strings.Split(value, ";") {
			if n = strings.TrimSpace(n); n != "" {
				o.Include = append(o.Include, n)
			}
		}
	case "getters":
		switch value {
		case "true":
			o.NoGetters = false
		case "false":
			o.NoGetters = true
		default:
			return fmt.Errorf("getters must be true or false, got %q", value)
		}
	default:
		return fmt.Errorf("unknown parameter %q", name)
	}
	return nil
}

type wkt int

const (
	notWKT wkt = iota
	wktTimestamp
	wktDuration
	wktEmpty
	wktFieldMask
	wktAny
	wktValue
	wktStruct
	wktListValue
	wktWrapper
)

var wellKnown = map[protoreflect.FullName]wkt{
	"google.protobuf.Timestamp":   wktTimestamp,
	"google.protobuf.Duration":    wktDuration,
	"google.protobuf.Empty":       wktEmpty,
	"google.protobuf.FieldMask":   wktFieldMask,
	"google.protobuf.Any":         wktAny,
	"google.protobuf.Value":       wktValue,
	"google.protobuf.Struct":      wktStruct,
	"google.protobuf.ListValue":   wktListValue,
	"google.protobuf.DoubleValue": wktWrapper,
	"google.protobuf.FloatValue":  wktWrapper,
	"google.protobuf.Int64Value":  wktWrapper,
	"google.protobuf.UInt64Value": wktWrapper,
	"google.protobuf.Int32Value":  wktWrapper,
	"google.protobuf.UInt32Value": wktWrapper,
	"google.protobuf.BoolValue":   wktWrapper,
	"google.protobuf.StringValue": wktWrapper,
	"google.protobuf.BytesValue":  wktWrapper,
}

var wktTypes = map[wkt]string{
	wktTimestamp: "Timestamp",
	wktDuration:  "Duration",
	wktEmpty:     "Empty",
	wktFieldMask: "FieldMask",
	wktAny:       "Any",
}

func wellKnownOf(m *protogen.Message) wkt {
	if m == nil {
		return notWKT
	}
	return wellKnown[m.Desc.FullName()]
}

const nullValue = "google.protobuf.NullValue"

// Run generates every requested file.
func Run(plugin *protogen.Plugin, opts Options) error {
	plugin.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)
	g := &generator{plugin: plugin, opts: opts}
	if err := g.selectTypes(); err != nil {
		return err
	}
	for _, f := range plugin.Files {
		if !f.Generate {
			continue
		}
		g.file(f)
	}
	return nil
}

type generator struct {
	plugin   *protogen.Plugin
	opts     Options
	selected map[protoreflect.FullName]bool
}

func unary(m *protogen.Method) bool {
	return !m.Desc.IsStreamingClient() && !m.Desc.IsStreamingServer()
}

func (g *generator) selectTypes() error {
	if len(g.opts.Include) == 0 {
		return nil
	}
	messages := map[protoreflect.FullName]*protogen.Message{}
	enums := map[protoreflect.FullName]*protogen.Enum{}
	var walk func(ms []*protogen.Message)
	walk = func(ms []*protogen.Message) {
		for _, m := range ms {
			messages[m.Desc.FullName()] = m
			for _, e := range m.Enums {
				enums[e.Desc.FullName()] = e
			}
			walk(m.Messages)
		}
	}
	services := map[protoreflect.FullName]*protogen.Service{}
	methods := map[protoreflect.FullName]*protogen.Method{}
	for _, f := range g.plugin.Files {
		walk(f.Messages)
		for _, e := range f.Enums {
			enums[e.Desc.FullName()] = e
		}
		for _, sv := range f.Services {
			services[sv.Desc.FullName()] = sv
			for _, m := range sv.Methods {
				methods[m.Desc.FullName()] = m
			}
		}
	}
	g.selected = map[protoreflect.FullName]bool{}
	var visit func(m *protogen.Message)
	visit = func(m *protogen.Message) {
		name := m.Desc.FullName()
		if g.selected[name] || wellKnownOf(m) != notWKT {
			return
		}
		if !m.Desc.IsMapEntry() {
			g.selected[name] = true
		}
		for _, f := range m.Fields {
			if f.Enum != nil {
				g.selected[f.Enum.Desc.FullName()] = true
			}
			if f.Message != nil {
				visit(f.Message)
			}
		}
	}
	method := func(m *protogen.Method) {
		if !unary(m) {
			return
		}
		g.selected[m.Desc.FullName()] = true
		visit(m.Input)
		visit(m.Output)
	}
	for _, n := range g.opts.Include {
		name := protoreflect.FullName(n)
		switch {
		case messages[name] != nil:
			visit(messages[name])
		case enums[name] != nil:
			g.selected[name] = true
		case services[name] != nil:
			for _, m := range services[name].Methods {
				method(m)
			}
		case methods[name] != nil:
			if !unary(methods[name]) {
				return fmt.Errorf("include: %q is a streaming method; only unary methods are generated", n)
			}
			method(methods[name])
		default:
			return fmt.Errorf("include: no message, enum, service or method named %q", n)
		}
	}
	return nil
}

func (g *generator) wanted(name protoreflect.FullName) bool {
	return g.selected == nil || g.selected[name]
}

func (g *generator) importPath(f *protogen.File) protogen.GoImportPath {
	if g.opts.ImportPrefix == "" {
		return f.GoImportPath
	}
	dir := path.Dir(f.Desc.Path())
	if dir == "." {
		return protogen.GoImportPath(g.opts.ImportPrefix)
	}
	return protogen.GoImportPath(g.opts.ImportPrefix + "/" + dir)
}

func (g *generator) fileOf(d protoreflect.Descriptor) *protogen.File {
	return g.plugin.FilesByPath[d.ParentFile().Path()]
}

func (g *generator) ident(d protoreflect.Descriptor, ident protogen.GoIdent) protogen.GoIdent {
	return protogen.GoIdent{GoName: ident.GoName, GoImportPath: g.importPath(g.fileOf(d))}
}

type fileGen struct {
	*generator
	g *protogen.GeneratedFile
}

func (g *generator) file(f *protogen.File) {
	var messages []*protogen.Message
	var enums []*protogen.Enum
	var walk func(ms []*protogen.Message)
	walk = func(ms []*protogen.Message) {
		for _, m := range ms {
			if m.Desc.IsMapEntry() {
				continue
			}
			if g.wanted(m.Desc.FullName()) {
				messages = append(messages, m)
			}
			for _, e := range m.Enums {
				if g.wanted(e.Desc.FullName()) {
					enums = append(enums, e)
				}
			}
			walk(m.Messages)
		}
	}
	for _, e := range f.Enums {
		if g.wanted(e.Desc.FullName()) {
			enums = append(enums, e)
		}
	}
	walk(f.Messages)
	prefix := f.GeneratedFilenamePrefix
	if g.opts.ImportPrefix != "" {
		prefix = strings.TrimSuffix(f.Desc.Path(), ".proto")
	}
	g.services(f, prefix)
	if len(messages) == 0 && len(enums) == 0 {
		return
	}
	fg := &fileGen{generator: g, g: g.plugin.NewGeneratedFile(prefix+".pb.go", g.importPath(f))}
	fg.P("// Code generated by protoc-gen-goalchemy. DO NOT EDIT.")
	fg.P("// source: ", f.Desc.Path())
	fg.P()
	fg.P("package ", f.GoPackageName)
	for _, e := range enums {
		fg.enum(e)
	}
	for _, m := range messages {
		fg.message(m)
	}
	fg.register(messages)
}

func (fg *fileGen) register(messages []*protogen.Message) {
	if len(messages) == 0 {
		return
	}
	msg := fg.q(protojsonPkg, "Message")
	fg.P()
	fg.P("func init() {")
	fg.P(fg.q(protojsonPkg, "Register"), "([]string{")
	for _, m := range messages {
		fg.P(fmt.Sprintf("%q", m.Desc.FullName()), ",")
	}
	fg.P("}, func(name string) ", msg, " {")
	fg.P("switch name {")
	for _, m := range messages {
		fg.P("case ", fmt.Sprintf("%q", m.Desc.FullName()), ":")
		fg.P("return &", m.GoIdent.GoName, "{}")
	}
	fg.P("}")
	fg.P("return nil")
	fg.P("})")
	fg.P("}")
}

func (fg *fileGen) P(v ...any) { fg.g.P(v...) }

func (fg *fileGen) q(path protogen.GoImportPath, name string) string {
	return fg.g.QualifiedGoIdent(path.Ident(name))
}

func (fg *fileGen) enumType(e *protogen.Enum) string {
	if e.Desc.FullName() == nullValue {
		return fg.q(protojsonPkg, "NullValue")
	}
	return fg.g.QualifiedGoIdent(fg.ident(e.Desc, e.GoIdent))
}

func (fg *fileGen) enum(e *protogen.Enum) {
	name := e.GoIdent.GoName
	fg.P()
	fg.P("type ", name, " int32")
	fg.P()
	fg.P("const (")
	for _, v := range e.Values {
		fg.P(v.GoIdent.GoName, " ", name, " = ", v.Desc.Number())
	}
	fg.P(")")
	fg.P()
	fg.P("var ", name, "_name = map[int32]string{")
	seen := map[protoreflect.EnumNumber]bool{}
	for _, v := range e.Values {
		if seen[v.Desc.Number()] {
			continue
		}
		seen[v.Desc.Number()] = true
		fg.P(v.Desc.Number(), ": ", fmt.Sprintf("%q", v.Desc.Name()), ",")
	}
	fg.P("}")
	fg.P()
	fg.P("var ", name, "_value = map[string]int32{")
	for _, v := range e.Values {
		fg.P(fmt.Sprintf("%q", v.Desc.Name()), ": ", v.Desc.Number(), ",")
	}
	fg.P("}")
	fg.P()
	fg.P("func (x ", name, ") String() string {")
	fg.P("if s, ok := ", name, "_name[int32(x)]; ok {")
	fg.P("return s")
	fg.P("}")
	fg.P("return ", fg.q(strconvPkg, "Itoa"), "(int(x))")
	fg.P("}")
}

func (fg *fileGen) messageType(m *protogen.Message) string {
	return fg.g.QualifiedGoIdent(fg.ident(m.Desc, m.GoIdent))
}

func isList(f *protogen.Field) bool { return f.Desc.IsList() }

func isMap(f *protogen.Field) bool { return f.Desc.IsMap() }

func optional(f *protogen.Field) bool {
	return f.Desc.HasOptionalKeyword() && f.Desc.Kind() != protoreflect.MessageKind && f.Desc.Kind() != protoreflect.GroupKind
}

// scalar returns the Go type of a non-message, non-enum field kind.
func scalar(k protoreflect.Kind) string {
	switch k {
	case protoreflect.BoolKind:
		return "bool"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return "int32"
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return "int64"
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return "uint32"
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return "uint64"
	case protoreflect.FloatKind:
		return "float32"
	case protoreflect.DoubleKind:
		return "float64"
	case protoreflect.StringKind:
		return "string"
	case protoreflect.BytesKind:
		return "[]byte"
	}
	panic("unsupported kind " + k.String())
}

// valueField returns the field describing a list element or map value.
func valueField(f *protogen.Field) *protogen.Field {
	if isMap(f) {
		return f.Message.Fields[1]
	}
	return f
}

// wrapperValue returns the value field of a wrapper message.
func wrapperValue(m *protogen.Message) *protogen.Field { return m.Fields[0] }

// elemType returns the Go type of one value of f, ignoring repetition and
// proto3 optional presence.
func (fg *fileGen) elemType(f *protogen.Field) string {
	switch f.Desc.Kind() {
	case protoreflect.EnumKind:
		return fg.enumType(f.Enum)
	case protoreflect.MessageKind, protoreflect.GroupKind:
		switch w := wellKnownOf(f.Message); w {
		case notWKT:
			return "*" + fg.messageType(f.Message)
		case wktValue, wktStruct, wktListValue:
			return fg.q(jsonvaluePkg, "Value")
		case wktWrapper:
			return scalar(wrapperValue(f.Message).Desc.Kind())
		default:
			return "*" + fg.q(protojsonPkg, wktTypes[w])
		}
	}
	return scalar(f.Desc.Kind())
}

func (fg *fileGen) fieldType(f *protogen.Field) string {
	switch {
	case isMap(f):
		return "map[" + scalar(f.Message.Fields[0].Desc.Kind()) + "]" + fg.elemType(f.Message.Fields[1])
	case isList(f):
		return "[]" + fg.elemType(f)
	}
	t := fg.elemType(f)
	if optional(f) || isWrapper(f) && t != "[]byte" {
		return "*" + t
	}
	return t
}

func isWrapper(f *protogen.Field) bool {
	return f.Desc.Kind() == protoreflect.MessageKind && wellKnownOf(f.Message) == wktWrapper
}

func oneofIface(m *protogen.Message, o *protogen.Oneof) string {
	return "is" + m.GoIdent.GoName + "_" + o.GoName
}

func realOneof(f *protogen.Field) *protogen.Oneof {
	if f.Oneof != nil && !f.Oneof.Desc.IsSynthetic() {
		return f.Oneof
	}
	return nil
}

func (fg *fileGen) message(m *protogen.Message) {
	name := m.GoIdent.GoName
	fg.P()
	fg.P("type ", name, " struct {")
	for _, f := range m.Fields {
		if o := realOneof(f); o != nil {
			if f == o.Fields[0] {
				fg.P(o.GoName, " ", oneofIface(m, o))
			}
			continue
		}
		fg.P(f.GoName, " ", fg.fieldType(f))
	}
	fg.P("}")
	for _, o := range m.Oneofs {
		if o.Desc.IsSynthetic() {
			continue
		}
		iface := oneofIface(m, o)
		fg.P()
		fg.P("type ", iface, " interface {")
		fg.P(iface, "()")
		fg.P("}")
		for _, f := range o.Fields {
			fg.P()
			fg.P("type ", f.GoIdent.GoName, " struct {")
			fg.P(f.GoName, " ", fg.fieldType(f))
			fg.P("}")
			fg.P()
			fg.P("func (*", f.GoIdent.GoName, ") ", iface, "() {}")
		}
	}
	if !fg.opts.NoGetters {
		fg.getters(m)
	}
	fg.encode(m)
	fg.decode(m)
	fg.P()
	fg.P("func (m *", name, ") MarshalJSON() ([]byte, error) { return ", fg.q(protojsonPkg, "Marshal"), "(m) }")
	fg.P()
	fg.P("func (m *", name, ") UnmarshalJSON(b []byte) error { return ", fg.q(protojsonPkg, "Unmarshal"), "(b, m) }")
}

func (fg *fileGen) zero(f *protogen.Field) string {
	t := fg.fieldType(f)
	switch {
	case strings.HasPrefix(t, "*"), strings.HasPrefix(t, "[]"), strings.HasPrefix(t, "map["):
		return "nil"
	case t == "string":
		return `""`
	case t == "bool":
		return "false"
	case t == fg.q(jsonvaluePkg, "Value"):
		return t + "{}"
	}
	if f.Desc.Kind() == protoreflect.EnumKind {
		return t + "(0)"
	}
	return "0"
}

func (fg *fileGen) getters(m *protogen.Message) {
	name := m.GoIdent.GoName
	for _, o := range m.Oneofs {
		if o.Desc.IsSynthetic() {
			continue
		}
		fg.P()
		fg.P("func (m *", name, ") Get", o.GoName, "() ", oneofIface(m, o), " {")
		fg.P("if m != nil {")
		fg.P("return m.", o.GoName)
		fg.P("}")
		fg.P("return nil")
		fg.P("}")
	}
	for _, f := range m.Fields {
		fg.P()
		fg.P("func (m *", name, ") Get", f.GoName, "() ", fg.fieldType(f), " {")
		if o := realOneof(f); o != nil {
			fg.P("if x, ok := m.Get", o.GoName, "().(*", f.GoIdent.GoName, "); ok {")
			fg.P("return x.", f.GoName)
			fg.P("}")
		} else {
			fg.P("if m != nil {")
			fg.P("return m.", f.GoName)
			fg.P("}")
		}
		fg.P("return ", fg.zero(f))
		fg.P("}")
	}
}

// present returns the condition under which a non-oneof field is encoded.
func (fg *fileGen) present(f *protogen.Field, x string) string {
	t := fg.fieldType(f)
	switch {
	case isWrapper(f) && t == "[]byte":
		return x + " != nil"
	case isMap(f) || isList(f) || t == "[]byte":
		return "len(" + x + ") > 0"
	case strings.HasPrefix(t, "*"):
		return x + " != nil"
	case t == fg.q(jsonvaluePkg, "Value"):
		return x + ".Exists()"
	case t == "string":
		return x + ` != ""`
	case t == "bool":
		return x
	case t == "float32" || t == "float64":
		return x + " != 0 || 1/" + x + " < 0"
	}
	return x + " != 0"
}

// writeValue emits code encoding one value x described by f.
func (fg *fileGen) writeValue(f *protogen.Field, x string) {
	switch f.Desc.Kind() {
	case protoreflect.EnumKind:
		if f.Enum.Desc.FullName() == nullValue {
			fg.P("e.Null()")
			return
		}
		fg.P("e.Enum(int32(", x, "), ", fg.enumType(f.Enum), "_name[int32(", x, ")])")
		return
	case protoreflect.MessageKind, protoreflect.GroupKind:
		switch wellKnownOf(f.Message) {
		case notWKT:
			fg.P("e.Message(", x, ")")
		case wktValue:
			fg.P("e.Value(", x, ")")
		case wktStruct:
			fg.P("e.Struct(", x, ")")
		case wktListValue:
			fg.P("e.ListValue(", x, ")")
		case wktWrapper:
			fg.writeScalar(wrapperValue(f.Message).Desc.Kind(), x)
		default:
			fg.P(x, ".EncodeProtoJSON(e)")
		}
		return
	}
	fg.writeScalar(f.Desc.Kind(), x)
}

func (fg *fileGen) writeScalar(k protoreflect.Kind, x string) {
	switch scalar(k) {
	case "bool":
		fg.P("e.Bool(", x, ")")
	case "int32":
		fg.P("e.Int32(", x, ")")
	case "int64":
		fg.P("e.Int64(", x, ")")
	case "uint32":
		fg.P("e.Uint32(", x, ")")
	case "uint64":
		fg.P("e.Uint64(", x, ")")
	case "float32":
		fg.P("e.Float32(", x, ")")
	case "float64":
		fg.P("e.Float64(", x, ")")
	case "string":
		fg.P("e.String(", x, ")")
	case "[]byte":
		fg.P("e.Bytes(", x, ")")
	}
}

func (fg *fileGen) encode(m *protogen.Message) {
	fg.P()
	fg.P("func (m *", m.GoIdent.GoName, ") EncodeProtoJSON(e *", fg.q(protojsonPkg, "Encoder"), ") {")
	fg.P("e.BeginObject()")
	if len(m.Fields) == 0 {
		fg.P("e.EndObject()")
		fg.P("}")
		return
	}
	fg.P("if m != nil {")
	for _, f := range m.Fields {
		jsonName := f.Desc.JSONName()
		x := "m." + f.GoName
		if o := realOneof(f); o != nil {
			fg.P("if x, ok := m.", o.GoName, ".(*", f.GoIdent.GoName, "); ok {")
			fg.P("e.Key(", fmt.Sprintf("%q", jsonName), ")")
			if isWrapper(f) && fg.elemType(f) != "[]byte" {
				fg.writeValue(f, "*x."+f.GoName)
			} else {
				fg.writeValue(f, "x."+f.GoName)
			}
			fg.P("}")
			continue
		}
		fg.P("if ", fg.present(f, x), " {")
		fg.P("e.Key(", fmt.Sprintf("%q", jsonName), ")")
		switch {
		case isMap(f):
			fg.encodeMap(f, x)
		case isList(f):
			fg.P("e.BeginArray()")
			fg.P("for _, v := range ", x, " {")
			fg.writeValue(f, "v")
			fg.P("}")
			fg.P("e.EndArray()")
		case optional(f) || isWrapper(f) && fg.elemType(f) != "[]byte":
			fg.writeValue(f, "*"+x)
		default:
			fg.writeValue(f, x)
		}
		fg.P("}")
	}
	fg.P("}")
	fg.P("e.EndObject()")
	fg.P("}")
}

func (fg *fileGen) encodeMap(f *protogen.Field, x string) {
	key, val := f.Message.Fields[0], f.Message.Fields[1]
	fg.P("e.BeginObject()")
	switch kt := scalar(key.Desc.Kind()); kt {
	case "string":
		fg.P("keys := make([]string, 0, len(", x, "))")
		fg.P("for k := range ", x, " {")
		fg.P("keys = append(keys, k)")
		fg.P("}")
		fg.P("for _, k := range ", fg.q(protojsonPkg, "SortedKeys"), "(keys) {")
		fg.P("e.Key(k)")
		fg.writeValue(val, x+"[k]")
		fg.P("}")
	case "bool":
		fg.P("for _, k := range []bool{false, true} {")
		fg.P("if v, ok := ", x, "[k]; ok {")
		fg.P("e.Key(", fg.q(strconvPkg, "FormatBool"), "(k))")
		fg.writeValue(val, "v")
		fg.P("}")
		fg.P("}")
	default:
		wide, sorter, format := "int64", "SortInt64s", "FormatInt"
		if strings.HasPrefix(kt, "uint") {
			wide, sorter, format = "uint64", "SortUint64s", "FormatUint"
		}
		fg.P("keys := make([]", wide, ", 0, len(", x, "))")
		fg.P("for k := range ", x, " {")
		fg.P("keys = append(keys, ", wide, "(k))")
		fg.P("}")
		fg.P("for _, k := range ", fg.q(protojsonPkg, sorter), "(keys) {")
		fg.P("e.Key(", fg.q(strconvPkg, format), "(k, 10))")
		fg.writeValue(val, x+"["+kt+"(k)]")
		fg.P("}")
	}
	fg.P("e.EndObject()")
}

// readValue emits code decoding JSON value v into a new variable named out,
// setting ok to false when the value is skipped (unknown enum names).
func (fg *fileGen) readValue(f *protogen.Field, v, out, label string) {
	switch f.Desc.Kind() {
	case protoreflect.EnumKind:
		t := fg.enumType(f.Enum)
		if f.Enum.Desc.FullName() == nullValue {
			fg.P(out, " := ", t, "(0)")
			return
		}
		fg.P(out, "n, ok := d.Enum(", v, ", ", label, ", ", t, "_value)")
		fg.P(out, " := ", t, "(", out, "n)")
		return
	case protoreflect.MessageKind, protoreflect.GroupKind:
		switch w := wellKnownOf(f.Message); w {
		case notWKT:
			fg.P(out, " := &", fg.messageType(f.Message), "{}")
			fg.P(out, ".DecodeProtoJSON(d, ", v, ")")
		case wktValue:
			fg.P(out, " := d.Value(", v, ", ", label, ")")
		case wktStruct:
			fg.P(out, " := d.Struct(", v, ", ", label, ")")
		case wktListValue:
			fg.P(out, " := d.ListValue(", v, ", ", label, ")")
		case wktWrapper:
			fg.readScalar(wrapperValue(f.Message).Desc.Kind(), v, out, label)
		default:
			fg.P(out, " := &", fg.q(protojsonPkg, wktTypes[w]), "{}")
			fg.P(out, ".DecodeProtoJSON(d, ", v, ")")
		}
		return
	}
	fg.readScalar(f.Desc.Kind(), v, out, label)
}

var scalarMethods = map[string]string{
	"bool": "Bool", "int32": "Int32", "int64": "Int64", "uint32": "Uint32", "uint64": "Uint64",
	"float32": "Float32", "float64": "Float64", "string": "String", "[]byte": "Bytes",
}

func (fg *fileGen) readScalar(k protoreflect.Kind, v, out, label string) {
	fg.P(out, " := d.", scalarMethods[scalar(k)], "(", v, ", ", label, ")")
}

// simpleRead returns a single expression decoding v when f needs no
// temporaries: scalars, wrappers and the JSON-valued well-known types.
func simpleRead(f *protogen.Field, v, label string) (string, bool) {
	k := f.Desc.Kind()
	switch k {
	case protoreflect.EnumKind:
		return "", false
	case protoreflect.MessageKind, protoreflect.GroupKind:
		switch wellKnownOf(f.Message) {
		case wktValue:
			return "d.Value(" + v + ", " + label + ")", true
		case wktStruct:
			return "d.Struct(" + v + ", " + label + ")", true
		case wktListValue:
			return "d.ListValue(" + v + ", " + label + ")", true
		case wktWrapper:
			k = wrapperValue(f.Message).Desc.Kind()
		default:
			return "", false
		}
	}
	return "d." + scalarMethods[scalar(k)] + "(" + v + ", " + label + ")", true
}

func isEnum(f *protogen.Field) bool {
	return f.Desc.Kind() == protoreflect.EnumKind && f.Enum.Desc.FullName() != nullValue
}

// nullable reports whether JSON null is a value of f rather than "unset".
func nullable(f *protogen.Field) bool {
	if f.Desc.Kind() == protoreflect.EnumKind {
		return f.Enum.Desc.FullName() == nullValue
	}
	return f.Desc.Kind() == protoreflect.MessageKind && wellKnownOf(f.Message) == wktValue
}

func (fg *fileGen) decode(m *protogen.Message) {
	fg.P()
	fg.P("func (m *", m.GoIdent.GoName, ") DecodeProtoJSON(d *", fg.q(protojsonPkg, "Decoder"), ", v ", fg.q(jsonvaluePkg, "Value"), ") {")
	if len(m.Fields) == 0 {
		fg.P("d.Object(v, ", fmt.Sprintf("%q", m.Desc.FullName()), ")")
		fg.P("}")
		return
	}
	fg.P("if !d.Object(v, ", fmt.Sprintf("%q", m.Desc.FullName()), ") {")
	fg.P("return")
	fg.P("}")
	for _, f := range m.Fields {
		if f.Desc.JSONName() != string(f.Desc.Name()) {
			fg.P("seen", f.GoName, " := false")
		}
	}
	oneofs := map[*protogen.Oneof]bool{}
	for _, f := range m.Fields {
		if o := realOneof(f); o != nil && !oneofs[o] {
			oneofs[o] = true
			fg.P("set", o.GoName, " := false")
		}
	}
	fg.P("for _, k := range v.Keys() {")
	fg.P("f := v.Get(k)")
	fg.P("switch k {")
	for _, f := range m.Fields {
		jsonName, protoName := f.Desc.JSONName(), string(f.Desc.Name())
		label := fmt.Sprintf("%q", jsonName)
		if jsonName != protoName {
			fg.P("case ", label, ", ", fmt.Sprintf("%q", protoName), ":")
			fg.P("if seen", f.GoName, " {")
			fg.P("d.Duplicate(k)")
			fg.P("return")
			fg.P("}")
			fg.P("seen", f.GoName, " = true")
		} else {
			fg.P("case ", label, ":")
		}
		if !(nullable(f) && !isList(f) && !isMap(f)) {
			fg.P("if f.IsNull() {")
			fg.P("break")
			fg.P("}")
		}
		x := "m." + f.GoName
		switch {
		case isMap(f):
			fg.decodeMap(f, x, label)
		case isList(f):
			fg.P("if !d.Array(f, ", label, ") {")
			fg.P("return")
			fg.P("}")
			fg.P("for i := 0; i < f.Len(); i++ {")
			fg.P("el := f.Index(i)")
			if !nullable(f) {
				fg.P("if el.IsNull() {")
				fg.P("d.NullElement(", label, ")")
				fg.P("return")
				fg.P("}")
			}
			if expr, ok := simpleRead(f, "el", label); ok {
				fg.P(x, " = append(", x, ", ", expr, ")")
			} else {
				fg.readValue(f, "el", "x", label)
				if isEnum(f) {
					fg.P("if !ok {")
					fg.P("continue")
					fg.P("}")
				}
				fg.P(x, " = append(", x, ", x)")
			}
			fg.P("}")
		default:
			if o := realOneof(f); o != nil {
				fg.P("if set", o.GoName, " {")
				fg.P("d.OneofSet(k, ", fmt.Sprintf("%q", o.Desc.FullName()), ")")
				fg.P("return")
				fg.P("}")
				fg.P("set", o.GoName, " = true")
				fg.readValue(f, "f", "x", label)
				if isEnum(f) {
					fg.P("if !ok {")
					fg.P("break")
					fg.P("}")
				}
				if isWrapper(f) && fg.elemType(f) != "[]byte" {
					fg.P("m.", o.GoName, " = &", f.GoIdent.GoName, "{", f.GoName, ": &x}")
				} else {
					fg.P("m.", o.GoName, " = &", f.GoIdent.GoName, "{", f.GoName, ": x}")
				}
				continue
			}
			pointer := optional(f) || isWrapper(f) && fg.elemType(f) != "[]byte"
			if expr, ok := simpleRead(f, "f", label); ok && !pointer {
				fg.P(x, " = ", expr)
				continue
			}
			fg.readValue(f, "f", "x", label)
			if isEnum(f) {
				fg.P("if !ok {")
				fg.P("break")
				fg.P("}")
			}
			if pointer {
				fg.P(x, " = &x")
			} else {
				fg.P(x, " = x")
			}
		}
	}
	fg.P("}")
	fg.P("if d.Err() != nil {")
	fg.P("return")
	fg.P("}")
	fg.P("}")
	fg.P("}")
}

func (fg *fileGen) decodeMap(f *protogen.Field, x, label string) {
	key, val := f.Message.Fields[0], f.Message.Fields[1]
	fg.P("if !d.Object(f, ", label, ") {")
	fg.P("return")
	fg.P("}")
	fg.P("if ", x, " == nil {")
	fg.P(x, " = ", fg.fieldType(f), "{}")
	fg.P("}")
	fg.P("for _, mk := range f.Keys() {")
	switch kt := scalar(key.Desc.Kind()); kt {
	case "string":
		fg.P("key := mk")
	case "bool":
		fg.P("key := d.MapKeyBool(mk, ", label, ")")
	default:
		bits := strings.TrimLeft(kt, "uint")
		if strings.HasPrefix(kt, "uint") {
			fg.P("key := ", kt, "(d.MapKeyUint64(mk, ", bits, ", ", label, "))")
		} else {
			fg.P("key := ", kt, "(d.MapKeyInt64(mk, ", bits, ", ", label, "))")
		}
	}
	fg.P("el := f.Get(mk)")
	if !nullable(val) {
		fg.P("if el.IsNull() {")
		fg.P("d.NullElement(", label, ")")
		fg.P("return")
		fg.P("}")
	}
	if expr, ok := simpleRead(val, "el", label); ok {
		fg.P(x, "[key] = ", expr)
	} else {
		fg.readValue(val, "el", "x", label)
		if isEnum(val) {
			fg.P("if !ok {")
			fg.P("continue")
			fg.P("}")
		}
		fg.P(x, "[key] = x")
	}
	fg.P("}")
}

func (fg *fileGen) rpcType(m *protogen.Message) string {
	if w := wellKnownOf(m); w != notWKT {
		return "*" + fg.q(protojsonPkg, wktTypes[w])
	}
	return "*" + fg.messageType(m)
}

func (fg *fileGen) newRPC(m *protogen.Message) string {
	return "&" + strings.TrimPrefix(fg.rpcType(m), "*") + "{}"
}

func rpcSupported(m *protogen.Method) bool {
	for _, msg := range []*protogen.Message{m.Input, m.Output} {
		if w := wellKnownOf(msg); w != notWKT && wktTypes[w] == "" {
			return false
		}
	}
	return unary(m)
}

func (g *generator) services(f *protogen.File, prefix string) {
	type service struct {
		sv      *protogen.Service
		methods []*protogen.Method
	}
	var list []service
	for _, sv := range f.Services {
		var methods []*protogen.Method
		for _, m := range sv.Methods {
			if rpcSupported(m) && g.wanted(m.Desc.FullName()) {
				methods = append(methods, m)
			}
		}
		if len(methods) > 0 {
			list = append(list, service{sv, methods})
		}
	}
	if len(list) == 0 {
		return
	}
	fg := &fileGen{generator: g, g: g.plugin.NewGeneratedFile(prefix+".connect.go", g.importPath(f))}
	fg.P("// Code generated by protoc-gen-goalchemy. DO NOT EDIT.")
	fg.P("// source: ", f.Desc.Path())
	fg.P()
	fg.P("package ", f.GoPackageName)
	client := fg.q(connectPkg, "Client")
	ctx := fg.q(contextPkg, "Context")
	for _, s := range list {
		name := s.sv.GoName
		fg.P()
		fg.P("const (")
		for _, m := range s.methods {
			fg.P(name, m.GoName, "Procedure = ", fmt.Sprintf("%q", "/"+string(s.sv.Desc.FullName())+"/"+string(m.Desc.Name())))
		}
		fg.P(")")
		fg.P()
		fg.P("type ", name, "Client struct {")
		fg.P("client *", client)
		fg.P("}")
		fg.P()
		fg.P("func New", name, "Client(client *", client, ") *", name, "Client {")
		fg.P("return &", name, "Client{client: client}")
		fg.P("}")
		for _, m := range s.methods {
			fg.P()
			fg.P("func (c *", name, "Client) ", m.GoName, "(ctx ", ctx, ", req ", fg.rpcType(m.Input), ") (", fg.rpcType(m.Output), ", error) {")
			fg.P("resp := ", fg.newRPC(m.Output))
			fg.P("if _, err := c.client.CallUnary(ctx, ", name, m.GoName, "Procedure, req, resp, nil); err != nil {")
			fg.P("return nil, err")
			fg.P("}")
			fg.P("return resp, nil")
			fg.P("}")
		}
	}
}
