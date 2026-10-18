package apidoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"strings"
)

func (r *renderer) rpcFiles() {
	if r.catalog.RPC == nil {
		return
	}
	ext := ".md"
	if r.site {
		ext = ".mdx"
	}
	for _, service := range r.catalog.RPC.Services {
		r.files[service.ID] = "rpc/services/" + slug(service.ID) + ".md"
		for _, method := range service.Methods {
			r.files[method.ID] = "rpc/methods/" + slug(method.ID) + ext
		}
	}
	for _, m := range r.catalog.RPC.Messages {
		r.files[m.ID] = "rpc/types/" + slug(m.ID) + ext
	}
	for _, e := range r.catalog.RPC.Enums {
		r.files[e.ID] = "rpc/types/" + slug(e.ID) + ".md"
	}
}

func (r *renderer) protoProse(b *bytes.Buffer, text string) {
	if text == "" {
		return
	}
	text = html.EscapeString(text)
	if r.site {
		text = escapeMDX(text)
	}
	b.WriteString(text + "\n\n")
}

func (r *renderer) protoType(file, name string) string {
	if r.files[name] != "" {
		return fmt.Sprintf("[%s](%s)", label(name), r.link(file, name))
	}
	return "`" + name + "`"
}

func (r *renderer) rpcFields(b *bytes.Buffer, file, id string) {
	if r.site {
		encoded, _ := json.Marshal(id)
		fmt.Fprintf(b, "<RpcFields type={%s} />\n\n", encoded)
		return
	}
	for _, m := range r.catalog.RPC.Messages {
		if m.ID != id {
			continue
		}
		if m.JSONEncoding != "" {
			fmt.Fprintf(b, "JSON encoding: %s. Fields below describe its protobuf structure.\n\n", m.JSONEncoding)
		}
		if len(m.Options) > 0 {
			b.WriteString("Message options and validation rules:\n\n")
			code(b, "json", string(m.Options))
		}
		for _, oneof := range m.Oneofs {
			fmt.Fprintf(b, "Oneof `%s`: choose at most one member.\n\n", oneof.Name)
			r.protoProse(b, oneof.Description)
			if len(oneof.Options) > 0 {
				code(b, "json", string(oneof.Options))
			}
		}
		if len(m.Fields) == 0 {
			b.WriteString("No fields.\n\n")
			return
		}
		b.WriteString("| JSON field | Protobuf field | Type | JSON encoding | Shape |\n| --- | --- | --- | --- | --- |\n")
		for _, f := range m.Fields {
			typeName := r.protoType(file, f.Type)
			shape := "singular"
			if f.Repeated {
				shape = "list"
			}
			if f.MapKey != "" {
				shape = "map with `" + f.MapKey + "` keys"
			}
			if f.Required {
				shape += "; required"
			} else if f.Presence {
				shape += "; explicit presence"
			}
			if f.Oneof != "" {
				shape += "; oneof `" + f.Oneof + "`"
			}
			if f.Deprecated {
				shape += "; deprecated"
			}
			fmt.Fprintf(b, "| `%s` | `%s` (%d) | %s | %s | %s |\n", f.JSONName, f.Name, f.Number, typeName, f.JSONType, shape)
		}
		b.WriteByte('\n')
		for _, f := range m.Fields {
			if f.Description == "" && len(f.Options) == 0 {
				continue
			}
			fmt.Fprintf(b, "### %s\n\n", f.JSONName)
			r.protoProse(b, f.Description)
			if len(f.Options) > 0 {
				code(b, "json", string(f.Options))
			}
		}
		return
	}
}

func (r *renderer) rpcPages(files map[string][]byte) {
	rpc := r.catalog.RPC
	if rpc == nil {
		return
	}
	var index bytes.Buffer
	r.header(&index, "RPC services", "rpc/index.md", false, false)
	index.WriteString("Browse service methods and their request and response schemas. Code examples and host bindings appear where explicitly supplied.\n\n")
	for _, service := range rpc.Services {
		fmt.Fprintf(&index, "- [%s](%s) (%d methods)\n", label(service.ID), r.href("rpc/index.md", r.files[service.ID]), len(service.Methods))
		file := r.files[service.ID]
		var page bytes.Buffer
		r.header(&page, service.ID, file, false, false)
		fmt.Fprintf(&page, "[All RPC services](%s)\n\n", r.href(file, "rpc/index.md"))
		if service.Deprecated {
			page.WriteString("**Deprecated service.**\n\n")
		}
		r.protoProse(&page, service.Description)
		for _, method := range service.Methods {
			fmt.Fprintf(&page, "- [%s](%s)", label(method.Name), r.link(file, method.ID))
			if method.Deprecated {
				page.WriteString(" (deprecated)")
			}
			page.WriteByte('\n')
			r.rpcMethod(files, service, method)
		}
		files[file] = page.Bytes()
	}
	files["rpc/index.md"] = index.Bytes()
	for _, message := range rpc.Messages {
		file := r.files[message.ID]
		var page bytes.Buffer
		r.header(&page, message.ID, file, true, true)
		if message.Deprecated {
			page.WriteString("**Deprecated message.**\n\n")
		}
		r.protoProse(&page, message.Description)
		page.WriteString("## Fields\n\n")
		r.rpcFields(&page, file, message.ID)
		if strings.HasPrefix(message.ID, "google.protobuf.") {
			page.WriteString("Standard protobuf well-known types can use a special JSON representation. The fields above describe the protobuf message structure.\n\n")
		}
		page.WriteString("## Used by\n\n")
		for _, service := range rpc.Services {
			for _, method := range service.Methods {
				if method.Request == message.ID || method.Response == message.ID {
					fmt.Fprintf(&page, "- [%s](%s)\n", label(method.ID), r.link(file, method.ID))
				}
			}
		}
		for _, owner := range rpc.Messages {
			for _, f := range owner.Fields {
				if f.Type == message.ID {
					fmt.Fprintf(&page, "- [%s.%s](%s)\n", label(owner.ID), label(f.JSONName), r.link(file, owner.ID))
					break
				}
			}
		}
		files[file] = page.Bytes()
	}
	for _, enum := range rpc.Enums {
		file := r.files[enum.ID]
		var page bytes.Buffer
		r.header(&page, enum.ID, file, true, false)
		if enum.Deprecated {
			page.WriteString("**Deprecated enum.**\n\n")
		}
		r.protoProse(&page, enum.Description)
		for _, value := range enum.Values {
			fmt.Fprintf(&page, "## %s\n\nValue: `%d`.\n\n", value.Name, value.Number)
			if value.Deprecated {
				page.WriteString("**Deprecated value.**\n\n")
			}
			r.protoProse(&page, value.Description)
		}
		files[file] = page.Bytes()
	}
}

func (r *renderer) rpcMethod(files map[string][]byte, service RPCService, method RPCMethod) {
	file := r.files[method.ID]
	var page bytes.Buffer
	r.header(&page, method.Name, file, true, true)
	fmt.Fprintf(&page, "[%s](%s)\n\n", label(service.ID), r.link(file, service.ID))
	if method.Deprecated {
		page.WriteString("**Deprecated method.**\n\n")
	}
	r.protoProse(&page, method.Description)
	request, response := method.Request, method.Response
	if method.ClientStreaming {
		request = "stream " + request
	}
	if method.ServerStreaming {
		response = "stream " + response
	}
	code(&page, "protobuf", fmt.Sprintf("rpc %s(%s) returns (%s);", method.Name, request, response))
	if method.ClientStreaming || method.ServerStreaming {
		page.WriteString("This is a streaming RPC. Its schema does not establish that a particular SDK transport implements streaming.\n\n")
	} else {
		fmt.Fprintf(&page, "Unary RPC procedure: `/%s`.\n\n", method.ID)
	}
	if len(method.Bindings) > 0 {
		page.WriteString("## Native client API\n\n")
		r.bindings(&page, method.Bindings)
		page.WriteString("These declarations are supplied by the SDK's host adapter documentation.\n\n")
	}
	for _, body := range []struct{ title, id string }{{"Request", method.Request}, {"Response", method.Response}} {
		fmt.Fprintf(&page, "## %s\n\n%s\n\n", body.title, r.protoType(file, body.id))
		r.rpcFields(&page, file, body.id)
	}
	page.WriteString("Field names use protobuf JSON spelling; native message properties can differ. Explicit presence is distinct from server-required validation. Maps, lists, and oneof alternatives are identified in the schema.\n\n")
	r.examplesFor(&page, method.ID)
	files[file] = page.Bytes()
}
