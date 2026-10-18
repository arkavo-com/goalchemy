package apidoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/driver"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// AddContent attaches reviewed examples and native adapter declarations to known APIs.
func (c *Catalog) AddContent(data []byte) error {
	var content Content
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&content); err != nil {
		return fmt.Errorf("documentation content: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("documentation content must contain one JSON object")
	}
	methods := c.RPC.methods()
	seen := map[string]bool{}
	for _, entry := range content.Bindings {
		method := methods[entry.Method]
		if method == nil || seen[entry.Method] {
			return fmt.Errorf("unknown or duplicate RPC binding %q", entry.Method)
		}
		seen[entry.Method] = true
		method.Bindings = entry.Bindings
		sort.Slice(method.Bindings, func(i, j int) bool { return method.Bindings[i].Target < method.Bindings[j].Target })
	}
	c.Examples = content.Examples
	sort.Slice(c.Examples, func(i, j int) bool { return c.Examples[i].ID < c.Examples[j].ID })
	if err := c.Check(); err != nil {
		return err
	}
	for _, example := range c.Examples {
		method := methods[example.For]
		if method == nil {
			continue
		}
		for _, body := range []struct {
			name, message string
			value         json.RawMessage
		}{
			{"request", method.Request, example.Request}, {"response", method.Response, example.Response},
		} {
			if len(body.value) == 0 {
				continue
			}
			if c.RPC.files == nil {
				return fmt.Errorf("example %s: load protobuf descriptors before validating JSON examples", example.ID)
			}
			descriptor, err := c.RPC.files.FindDescriptorByName(protoreflect.FullName(body.message))
			if err != nil {
				return err
			}
			options := protojson.UnmarshalOptions{Resolver: dynamicpb.NewTypes(c.RPC.files)}
			if err := options.Unmarshal(body.value, dynamicpb.NewMessage(descriptor.(protoreflect.MessageDescriptor))); err != nil {
				return fmt.Errorf("example %s %s: %w", example.ID, body.name, err)
			}
		}
	}
	return nil
}

func knownLanguage(target string) bool {
	for _, name := range driver.Targets() {
		if name == target {
			return true
		}
	}
	return false
}

func (c *Catalog) checkContent() error {
	owners := map[string]bool{}
	for _, s := range c.Symbols {
		owners[s.ID] = true
	}
	methods := c.RPC.methods()
	if c.RPC != nil {
		types := map[string]bool{}
		messages := map[string]bool{}
		for _, m := range c.RPC.Messages {
			if m.ID == "" || types[m.ID] {
				return fmt.Errorf("invalid or duplicate RPC type %q", m.ID)
			}
			types[m.ID] = true
			messages[m.ID] = true
		}
		for _, e := range c.RPC.Enums {
			if e.ID == "" || types[e.ID] {
				return fmt.Errorf("invalid or duplicate RPC type %q", e.ID)
			}
			types[e.ID] = true
		}
		services, methodIDs := map[string]bool{}, map[string]bool{}
		for _, s := range c.RPC.Services {
			if s.ID == "" || services[s.ID] {
				return fmt.Errorf("invalid or duplicate RPC service %q", s.ID)
			}
			services[s.ID] = true
			for _, m := range s.Methods {
				if m.Name == "" || m.ID != s.ID+"/"+m.Name || methodIDs[m.ID] || !messages[m.Request] || !messages[m.Response] {
					return fmt.Errorf("invalid RPC method %q", m.ID)
				}
				methodIDs[m.ID], owners[m.ID] = true, true
				seen := map[string]bool{}
				for _, b := range m.Bindings {
					if !knownLanguage(b.Target) || seen[b.Target] || b.Name == "" || b.Signature == "" {
						return fmt.Errorf("%s has invalid host binding for %s", m.ID, b.Target)
					}
					seen[b.Target] = true
				}
			}
		}
		primitives := " double float int64 uint64 int32 fixed64 fixed32 bool string bytes uint32 sfixed32 sfixed64 sint32 sint64 "
		for _, m := range c.RPC.Messages {
			seenNames, seenNumbers := map[string]bool{}, map[int32]bool{}
			oneofs := map[string]bool{}
			for _, oneof := range m.Oneofs {
				if oneof.Name == "" || oneofs[oneof.Name] {
					return fmt.Errorf("%s has invalid oneof %q", m.ID, oneof.Name)
				}
				oneofs[oneof.Name] = true
			}
			for _, f := range m.Fields {
				if f.Name == "" || f.JSONName == "" || f.JSONType == "" || f.Number <= 0 || seenNames[f.Name] || seenNumbers[f.Number] || (!types[f.Type] && !strings.Contains(primitives, " "+f.Type+" ")) {
					return fmt.Errorf("%s has invalid RPC field %q", m.ID, f.Name)
				}
				if f.Oneof != "" && !oneofs[f.Oneof] {
					return fmt.Errorf("%s.%s references missing oneof %s", m.ID, f.Name, f.Oneof)
				}
				seenNames[f.Name], seenNumbers[f.Number] = true, true
			}
		}
	}
	seen := map[string]bool{}
	for _, e := range c.Examples {
		if e.ID == "" || e.Title == "" || seen[e.ID] || (e.For != "" && !owners[e.For]) {
			return fmt.Errorf("invalid example identity or API reference %q (%s)", e.ID, e.For)
		}
		seen[e.ID] = true
		if len(e.Snippets) == 0 && len(e.Request) == 0 && len(e.Response) == 0 {
			return fmt.Errorf("example %s has no code or JSON", e.ID)
		}
		if (len(e.Request) > 0 || len(e.Response) > 0) && methods[e.For] == nil {
			return fmt.Errorf("example %s: JSON bodies require an RPC method", e.ID)
		}
		languages := map[string]bool{}
		for _, snippet := range e.Snippets {
			if !knownLanguage(snippet.Target) || languages[snippet.Target] || strings.TrimSpace(snippet.Code) == "" {
				return fmt.Errorf("example %s has invalid snippet for %s", e.ID, snippet.Target)
			}
			languages[snippet.Target] = true
		}
	}
	return nil
}
