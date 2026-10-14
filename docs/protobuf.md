# Protobuf messages

`protoc-gen-goalchemy` is a `protoc` and `buf` plugin that turns `.proto`
files into Goalchemy-subset Go: one struct per message and proto3 JSON
encoding through `std/encoding/protojson`. The generated code is ordinary
source, so it compiles to every target with the rest of a program.

```bash
go install github.com/eugenioenko/goalchemy/cmd/protoc-gen-goalchemy@latest
```

```yaml
# buf.gen.yaml
version: v2
plugins:
  - local: protoc-gen-goalchemy
    out: gen
    opt:
      - go_import_prefix=example.com/app/gen
      - include=policy.attributes.ListAttributesResponse;kas.RewrapRequest
```

## Parameters

| Parameter | Effect |
| --- | --- |
| `go_import_prefix=<path>` | Places each file's package at `<path>/<proto directory>` instead of its `go_package`. |
| `include=<full.Name>[;<full.Name>...]` | Generates only these messages, enums, services and methods (`pkg.Service.Method`) and the types they reference, across files. Without it every message and unary method in the requested files is generated. |
| `getters=false` | Omits the nil-safe `GetX` accessors. |

Generate only what a program uses: every message adds code, and Swift and
Rust build time grows with the amount of code.

## Generated code

Names follow `protoc-gen-go`: `KeyAccess`, `AttributeRuleTypeEnum_ATTRIBUTE_RULE_TYPE_ENUM_HIERARCHY`,
`EntityIdentifier_Token` for a oneof member, and an `isEntityIdentifier_Identifier`
interface for the oneof field.

| Proto | Go |
| --- | --- |
| scalar | `bool`, `int32`, `int64`, `uint32`, `uint64`, `float32`, `float64`, `string`, `[]byte` |
| `optional` scalar or enum | pointer to the scalar (`*bool`), encoded whenever set, even to zero; `GetX` returns the pointer |
| `optional` message | pointer to the generated struct, as without `optional` |
| enum | named `int32` with `_name` and `_value` maps and `String` |
| message | pointer to the generated struct |
| `repeated T`, `map<K, V>` | `[]T`, `map[K]V` |
| `oneof` | interface field set to a wrapper struct per member |
| `google.protobuf.Timestamp`, `Duration`, `Empty`, `FieldMask`, `Any` | `*protojson.Timestamp` and so on |
| `google.protobuf.Struct`, `Value`, `ListValue` | `jsonvalue.Value` (zero means unset) |
| wrappers such as `BoolValue` | pointer to the scalar (`*bool`); `BytesValue` is `[]byte` |

Every message has `EncodeProtoJSON` and `DecodeProtoJSON` (the
`protojson.Message` interface), `MarshalJSON` and `UnmarshalJSON`, and,
unless disabled, `GetX` accessors. Each generated file registers its
messages with `protojson.Register` so that `Any` payloads of those types are
decoded and re-encoded canonically.

```go
attr := &policy.Attribute{Name: "classification", Rule: policy.AttributeRuleTypeEnum_ATTRIBUTE_RULE_TYPE_ENUM_HIERARCHY}
data, err := protojson.Marshal(attr)

var resp attributes.ListAttributesResponse
err = protojson.Unmarshal(body, &resp)
for _, a := range resp.GetAttributes() {
	println(a.GetFqn())
}
```

## Services

Each service with unary methods gets a `<file>.connect.go` file holding a
client over [`std/connect`](../std/connect), the Connect protocol with the
JSON codec. Streaming methods are skipped. Calls suspend on the host HTTP
client, so programs that make them compile with the cooperative gate.

```go
client := connect.NewClient("https://platform.example.com")
client.Headers = []string{"Authorization", "Bearer " + token}
attrs := attributes.NewAttributesServiceClient(client)

resp, err := attrs.GetAttribute(ctx, &attributes.GetAttributeRequest{
	Identifier: &attributes.GetAttributeRequest_Fqn{Fqn: "https://example.com/attr/classification"},
})
if connect.CodeOf(err) == connect.CodeNotFound {
	// ...
}
```

Each method also has a `<Service><Method>Procedure` constant, and
`Client.CallUnary` calls any procedure with per-call headers and returns the
response headers. Requests are POSTed as `application/json` with
`Connect-Protocol-Version: 1` and `Connect-Timeout-Ms`. Failures are
`*connect.Error` values with the code, message, details (type URL, binary
value and JSON debug form) and response headers that connect-go reports for
the same response, including the HTTP-status fallbacks for non-Connect
error bodies. A context that is already cancelled or past its deadline
yields `canceled` or `deadline_exceeded`; other transport failures are
`unavailable`.

`Client.TimeoutMillis` (default 30 s) bounds each call and is sent as the
Connect deadline; `Client.MaxResponseBytes` (default 16 MiB) bounds the
reply.

`Client.HTTPClient` replaces the transport, like connect-go's `HTTPClient`
and interceptors: set it to add credentials, DPoP proofs or an allowlist, or
to record calls in tests. Its `Do` has the arguments and results of
`lib/http.Do`; it receives the method, the full URL, the Connect and client
headers, the JSON body and the client's response bound and timeout, and a
non-2xx status is a result rather than an error. Its response is decoded
exactly as a `lib/http` response, so status and Connect error mapping is the
same either way. When it is nil, calls go through `lib/http.DoWith` with the
`Client.Transport` options.

```go
type authTransport struct{ token string }

func (t *authTransport) Do(ctx context.Context, method, url string, headers []string, body []byte, maxResponseBytes int, timeoutMillis int64) (int, []string, []byte, error) {
	headers = append(append([]string(nil), headers...), "Authorization", "Bearer "+t.token)
	return http.Do(ctx, method, url, headers, body, maxResponseBytes, timeoutMillis)
}

client.HTTPClient = &authTransport{token: token}
```

## Differences from `google.golang.org/protobuf/encoding/protojson`

Output is `protojson.Marshal`'s with insignificant whitespace removed, and
input follows `protojson.UnmarshalOptions{DiscardUnknown: true}`, the options
Connect clients use. The `proto_platform` language fixture checks both
against protojson on every target, using the OpenTDF platform's messages.

- **Unregistered `Any` payloads** are kept as received instead of failing
  with "unable to resolve". Payloads of generated messages and of the
  well-known types are decoded and re-encoded as protojson does.
- **Error messages** use protojson's wording where practical but do not
  include byte offsets.
- Extensions, groups and proto2 defaults are not supported. proto3
  `optional` is supported, and the plugin declares it to `protoc`.
