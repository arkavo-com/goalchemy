package apidoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// RPC contains protocol services and the message graph reachable from their methods.
type RPC struct {
	Services []RPCService `json:"services"`
	Messages []RPCMessage `json:"messages"`
	Enums    []RPCEnum    `json:"enums"`
	files    *protoregistry.Files
}

type RPCService struct {
	ID          string      `json:"id"`
	Description string      `json:"description,omitempty"`
	Deprecated  bool        `json:"deprecated,omitempty"`
	Location    Location    `json:"location"`
	Methods     []RPCMethod `json:"methods"`
}

type RPCMethod struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Description     string    `json:"description,omitempty"`
	Deprecated      bool      `json:"deprecated,omitempty"`
	Request         string    `json:"request"`
	Response        string    `json:"response"`
	ClientStreaming bool      `json:"client_streaming,omitempty"`
	ServerStreaming bool      `json:"server_streaming,omitempty"`
	Location        Location  `json:"location"`
	Bindings        []Binding `json:"bindings,omitempty"`
}

type RPCMessage struct {
	ID           string          `json:"id"`
	Description  string          `json:"description,omitempty"`
	Deprecated   bool            `json:"deprecated,omitempty"`
	Location     Location        `json:"location"`
	Fields       []RPCField      `json:"fields"`
	JSONEncoding string          `json:"json_encoding,omitempty"`
	Options      json.RawMessage `json:"options,omitempty"`
	Oneofs       []RPCOneof      `json:"oneofs,omitempty"`
}

type RPCOneof struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Options     json.RawMessage `json:"options,omitempty"`
}

type RPCField struct {
	Name        string          `json:"name"`
	JSONName    string          `json:"json_name"`
	Number      int32           `json:"number"`
	Type        string          `json:"type"`
	JSONType    string          `json:"json_type"`
	MapKey      string          `json:"map_key,omitempty"`
	Repeated    bool            `json:"repeated,omitempty"`
	Presence    bool            `json:"presence,omitempty"`
	Required    bool            `json:"required,omitempty"`
	Oneof       string          `json:"oneof,omitempty"`
	Description string          `json:"description,omitempty"`
	Deprecated  bool            `json:"deprecated,omitempty"`
	Options     json.RawMessage `json:"options,omitempty"`
}

type RPCEnum struct {
	ID          string         `json:"id"`
	Description string         `json:"description,omitempty"`
	Deprecated  bool           `json:"deprecated,omitempty"`
	Location    Location       `json:"location"`
	Values      []RPCEnumValue `json:"values"`
}

type RPCEnumValue struct {
	Name        string `json:"name"`
	Number      int32  `json:"number"`
	Description string `json:"description,omitempty"`
	Deprecated  bool   `json:"deprecated,omitempty"`
}

func protoComment(d protoreflect.Descriptor) string {
	s := d.ParentFile().SourceLocations().ByDescriptor(d)
	return strings.TrimSpace(s.LeadingComments + "\n" + s.TrailingComments)
}

func protoLocation(d protoreflect.Descriptor) Location {
	s := d.ParentFile().SourceLocations().ByDescriptor(d)
	line := 0
	if len(s.Path) > 0 {
		line = s.StartLine + 1
	}
	return Location{File: d.ParentFile().Path(), Line: line}
}

func protoDeprecated(d protoreflect.Descriptor) bool {
	f := d.Options().ProtoReflect().Descriptor().Fields().ByName("deprecated")
	return f != nil && d.Options().ProtoReflect().Get(f).Bool()
}

func messageJSONType(name string) string {
	forms := map[string]string{
		"google.protobuf.Timestamp": "RFC 3339 timestamp string", "google.protobuf.Duration": "duration string ending in s",
		"google.protobuf.FieldMask": "comma-separated field-path string", "google.protobuf.Any": "object with an @type member",
		"google.protobuf.Struct": "JSON object", "google.protobuf.Value": "any JSON value", "google.protobuf.ListValue": "JSON array",
		"google.protobuf.DoubleValue": "number or special float string", "google.protobuf.FloatValue": "number or special float string",
		"google.protobuf.Int64Value": "decimal integer string", "google.protobuf.UInt64Value": "decimal integer string",
		"google.protobuf.Int32Value": "number", "google.protobuf.UInt32Value": "number",
		"google.protobuf.BoolValue": "boolean", "google.protobuf.StringValue": "string", "google.protobuf.BytesValue": "base64 string",
	}
	return forms[name]
}

func fieldJSONType(f protoreflect.FieldDescriptor) string {
	if f.IsMap() {
		return "object with string keys and " + fieldJSONType(f.MapValue()) + " values"
	}
	var value string
	switch f.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		value = messageJSONType(string(f.Message().FullName()))
		if value == "" {
			value = "object"
		}
	case protoreflect.EnumKind:
		value = "enum name string or numeric value"
		if f.Enum().FullName() == "google.protobuf.NullValue" {
			value = "null"
		}
	case protoreflect.Int64Kind, protoreflect.Uint64Kind, protoreflect.Sint64Kind, protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind:
		value = "decimal integer string"
	case protoreflect.BytesKind:
		value = "base64 string"
	case protoreflect.StringKind:
		value = "string"
	case protoreflect.BoolKind:
		value = "boolean"
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		value = "number or special float string"
	default:
		value = "number"
	}
	if f.IsList() {
		value = "array of " + value
	}
	return value
}

// AddDescriptors imports an include-imports FileDescriptorSet, optionally selecting services.
// SourceCodeInfo supplies comments; absent comments are left empty rather than invented.
func (c *Catalog) AddDescriptors(data []byte, selected []string) error {
	set := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(data, set); err != nil {
		return fmt.Errorf("protobuf descriptor set: %w", err)
	}
	files, err := protodesc.NewFiles(set)
	if err != nil {
		return fmt.Errorf("protobuf descriptor dependencies (include imports): %w", err)
	}
	resolver := dynamicpb.NewTypes(files)
	if err := (proto.UnmarshalOptions{Resolver: resolver}).Unmarshal(data, set); err != nil {
		return fmt.Errorf("protobuf descriptor options: %w", err)
	}
	files, err = protodesc.NewFiles(set)
	if err != nil {
		return err
	}
	r := &RPC{Services: []RPCService{}, Messages: []RPCMessage{}, Enums: []RPCEnum{}, files: files}
	wanted := map[string]bool{}
	for _, name := range selected {
		wanted[name] = true
	}
	seen := map[string]bool{}
	var optionsErr error
	options := func(d protoreflect.Descriptor) json.RawMessage {
		data, err := (protojson.MarshalOptions{Resolver: resolver}).Marshal(d.Options())
		if err != nil {
			if optionsErr == nil {
				optionsErr = fmt.Errorf("protobuf options for %s: %w", d.FullName(), err)
			}
			return nil
		}
		if string(data) == "{}" {
			return nil
		}
		var value any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			if optionsErr == nil {
				optionsErr = err
			}
			return nil
		}
		canonical, err := json.Marshal(value)
		if err != nil && optionsErr == nil {
			optionsErr = err
		}
		return canonical
	}
	var visitEnum func(protoreflect.EnumDescriptor)
	visitEnum = func(d protoreflect.EnumDescriptor) {
		id := string(d.FullName())
		if seen[id] {
			return
		}
		seen[id] = true
		e := RPCEnum{ID: id, Description: protoComment(d), Deprecated: protoDeprecated(d), Location: protoLocation(d), Values: []RPCEnumValue{}}
		for i := 0; i < d.Values().Len(); i++ {
			v := d.Values().Get(i)
			e.Values = append(e.Values, RPCEnumValue{Name: string(v.Name()), Number: int32(v.Number()), Description: protoComment(v), Deprecated: protoDeprecated(v)})
		}
		r.Enums = append(r.Enums, e)
	}
	var visitMessage func(protoreflect.MessageDescriptor)
	var visitType func(protoreflect.FieldDescriptor) string
	visitType = func(f protoreflect.FieldDescriptor) string {
		if f.Message() != nil {
			visitMessage(f.Message())
			return string(f.Message().FullName())
		}
		if f.Enum() != nil {
			visitEnum(f.Enum())
			return string(f.Enum().FullName())
		}
		return f.Kind().String()
	}
	visitMessage = func(d protoreflect.MessageDescriptor) {
		id := string(d.FullName())
		if seen[id] {
			return
		}
		seen[id] = true
		m := RPCMessage{ID: id, Description: protoComment(d), Deprecated: protoDeprecated(d), Location: protoLocation(d), Fields: []RPCField{}, JSONEncoding: messageJSONType(id), Options: options(d)}
		for i := 0; i < d.Oneofs().Len(); i++ {
			oneof := d.Oneofs().Get(i)
			if !oneof.IsSynthetic() {
				m.Oneofs = append(m.Oneofs, RPCOneof{Name: string(oneof.Name()), Description: protoComment(oneof), Options: options(oneof)})
			}
		}
		for i := 0; i < d.Fields().Len(); i++ {
			f := d.Fields().Get(i)
			field := RPCField{Name: string(f.Name()), JSONName: f.JSONName(), Number: int32(f.Number()), JSONType: fieldJSONType(f), Presence: f.HasPresence(), Required: f.Cardinality() == protoreflect.Required, Repeated: f.IsList(), Description: protoComment(f), Deprecated: protoDeprecated(f)}
			if f.IsMap() {
				field.MapKey, field.Type = f.MapKey().Kind().String(), visitType(f.MapValue())
			} else {
				field.Type = visitType(f)
			}
			if oneof := f.ContainingOneof(); oneof != nil && !oneof.IsSynthetic() {
				field.Oneof = string(oneof.Name())
			}
			field.Options = options(f)
			m.Fields = append(m.Fields, field)
		}
		r.Messages = append(r.Messages, m)
	}
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		for i := 0; i < fd.Services().Len(); i++ {
			d := fd.Services().Get(i)
			id := string(d.FullName())
			if len(selected) > 0 && !wanted[id] {
				continue
			}
			delete(wanted, id)
			s := RPCService{ID: id, Description: protoComment(d), Deprecated: protoDeprecated(d), Location: protoLocation(d), Methods: []RPCMethod{}}
			for j := 0; j < d.Methods().Len(); j++ {
				m := d.Methods().Get(j)
				visitMessage(m.Input())
				visitMessage(m.Output())
				s.Methods = append(s.Methods, RPCMethod{ID: id + "/" + string(m.Name()), Name: string(m.Name()), Description: protoComment(m), Deprecated: protoDeprecated(m), Request: string(m.Input().FullName()), Response: string(m.Output().FullName()), ClientStreaming: m.IsStreamingClient(), ServerStreaming: m.IsStreamingServer(), Location: protoLocation(m)})
			}
			r.Services = append(r.Services, s)
		}
		return true
	})
	if optionsErr != nil {
		return optionsErr
	}
	if len(wanted) > 0 {
		var names []string
		for name := range wanted {
			names = append(names, name)
		}
		sort.Strings(names)
		return fmt.Errorf("protobuf services not found: %s", strings.Join(names, ", "))
	}
	if len(r.Services) == 0 {
		return fmt.Errorf("protobuf descriptor set contains no selected services")
	}
	sort.Slice(r.Services, func(i, j int) bool { return r.Services[i].ID < r.Services[j].ID })
	sort.Slice(r.Messages, func(i, j int) bool { return r.Messages[i].ID < r.Messages[j].ID })
	sort.Slice(r.Enums, func(i, j int) bool { return r.Enums[i].ID < r.Enums[j].ID })
	c.RPC = r
	return c.Check()
}

func (r *RPC) methods() map[string]*RPCMethod {
	out := map[string]*RPCMethod{}
	if r != nil {
		for i := range r.Services {
			for j := range r.Services[i].Methods {
				m := &r.Services[i].Methods[j]
				out[m.ID] = m
			}
		}
	}
	return out
}
