package swift

import (
	"fmt"
	"go/token"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/ir"
)

type LibraryBoundaryError struct{ Message string }

func (e *LibraryBoundaryError) Error() string { return e.Message }
func boundary(format string, args ...any) error {
	return &LibraryBoundaryError{fmt.Sprintf(format, args...)}
}

func sourceContext(t *ir.Type) bool {
	return t.U().Kind == ir.KOpaque && t.U().Name == "_context.Context"
}
func sourceError(t *ir.Type) bool { return t.Name == "error" }
func cryptoKey(t *ir.Type) bool {
	return t.U().Kind == ir.KPointer && t.U().Elem.U().Kind == ir.KOpaque && t.U().Elem.U().Name == "crypto.Key"
}
func libraryValue(t *ir.Type, seen map[*ir.Type]bool) bool {
	if seen[t] {
		return true
	}
	seen[t] = true
	u := t.U()
	switch u.Kind {
	case ir.KBool, ir.KInt, ir.KFloat, ir.KString:
		return true
	case ir.KArray, ir.KSlice:
		return libraryValue(u.Elem, seen)
	case ir.KStruct:
		for _, f := range u.Fields {
			if !token.IsExported(f.Name) || f.Embedded || !libraryValue(f.Type, seen) {
				return false
			}
		}
		return true
	}
	return cryptoKey(t)
}
func swiftPublic(name string) string { return "`" + name + "`" }
func (e *emitter) library() (string, error) {
	var b strings.Builder
	names := map[*ir.Type]string{}
	used := map[string]bool{}
	reserved := map[string]bool{}
	for _, n := range strings.Fields("GoString GoalchemyFailure GoalchemyKey CallOptions CancellationToken CallbackProvider CallbackRequest Operation GValue GTypes GFrame GOwner GNative GCell Array String Bool Int Int8 Int16 Int32 Int64 UInt UInt8 UInt16 UInt32 UInt64 Float Double Error Result Data Date UUID Task Thread DispatchQueue NSLock NSRecursiveLock NSCondition Set Dictionary Optional Never Void Foundation Swift Self Any AnyObject Locale ProcessInfo FileHandle ObjectIdentifier DispatchTime DispatchTimeInterval TimeInterval Unicode UTF8 UnsafeBufferPointer UnsafeMutableBufferPointer UnsafePointer UnsafeMutablePointer UnsafeRawPointer UnsafeMutableRawPointer OpaquePointer CallOptions CallbackProvider CallbackRequest CancellationToken GAction GAggregate GBounds GBuffer GCallback GCell GChannel GChannelWait GContext GCrypto GCryptoInput GCryptoKey GCryptoOutput GError GFatal GFault GFrame GFunction GHTTP GHTTPExchange GHTTPExchanges GHTTPPayload GHTTPInput GHTTPOutput GHTTPTLS GHeap GHostOperation GInterface GLibrary GLibraryRetention GMailbox GManaged GMap GMapEntry GMapIterator GMutex GNative GNumeric GOpaque GOwner GPanic GPointer GProviderLease GReject GSelectCase GSelection GSlice GString GTask GTimer GType GTypes GValue GWaitGroup GWeak GWeakCryptoKey GoString GoalchemyFailure GoalchemyKey Operation GoalchemyLogRecord GLog") {
		reserved[n] = true
	}
	roots := map[string]bool{}
	for _, f := range e.p.Exports {
		roots[f.Pkg] = true
	}
	abi, err := e.p.LibraryHandles(func(t *ir.Type) bool { return libraryValue(t, map[*ir.Type]bool{}) })
	if err != nil {
		return "", &LibraryBoundaryError{err.Error()}
	}
	// A public name that Swift, Foundation or the runtime already uses gets a
	// trailing underscore, as Rust renames a method named Self to Self_.
	public := func(name string) string {
		for used[name] || reserved[name] {
			name += "_"
		}
		used[name] = true
		return name
	}
	handleElems := map[*ir.Type]bool{}
	handleNames := map[*ir.Handle]string{}
	e.handleNames = handleNames
	for _, h := range abi.Handles {
		handleNames[h] = public(h.Name)
		handleElems[h.Elem] = true
	}
	for _, t := range e.p.Types.All {
		if t.Kind == ir.KNamed && roots[t.Pkg] && token.IsExported(t.Obj) && t.U().Kind == ir.KStruct && !handleElems[t] && libraryValue(t, map[*ir.Type]bool{}) {
			names[t.U()] = public(t.Obj)
		}
	}
	var typ func(*ir.Type) string
	typ = func(t *ir.Type) string {
		u := t.U()
		switch u.Kind {
		case ir.KBool:
			return "Bool"
		case ir.KInt:
			prefix := "Int"
			if !u.Int.Signed() {
				prefix = "UInt"
			}
			return fmt.Sprintf("%s%d", prefix, u.Int.Bits())
		case ir.KFloat:
			if u.FloatBits == 32 {
				return "Float"
			}
			return "Double"
		case ir.KString:
			return "GoString"
		case ir.KArray:
			return "[" + typ(u.Elem) + "]"
		case ir.KSlice:
			return "[" + typ(u.Elem) + "]?"
		case ir.KStruct:
			// Module-qualified, so a field named like its type (Options.TokenExchange
			// of type TokenExchange) does not shadow the type in the struct's
			// initializer and default values.
			if n := names[u]; n != "" {
				return "GoalchemyGenerated." + swiftPublic(n)
			}
			n := fmt.Sprintf("Value%d", u.ID)
			for used[n] || reserved[n] {
				n += "_"
			}
			used[n] = true
			names[u] = n
			return "GoalchemyGenerated." + swiftPublic(n)
		}
		if cryptoKey(t) {
			return "GoalchemyKey?"
		}
		panic("swift public type " + t.String())
	}
	var defaultValue func(*ir.Type) string
	defaultValue = func(t *ir.Type) string {
		u := t.U()
		switch u.Kind {
		case ir.KBool:
			return "false"
		case ir.KInt, ir.KFloat:
			return "0"
		case ir.KString:
			return "GoString(\"\")"
		case ir.KArray:
			return fmt.Sprintf("Array(repeating: %s, count: %d)", defaultValue(u.Elem), u.Len)
		case ir.KSlice:
			return "nil"
		case ir.KStruct:
			return typ(t) + "()"
		}
		return "nil"
	}
	seen := map[*ir.Type]bool{}
	var convert func(*ir.Type)
	convert = func(t *ir.Type) {
		u := t.U()
		if seen[u] {
			return
		}
		seen[u] = true
		if u.Kind == ir.KStruct {
			typ(t)
			n := swiftPublic(names[u])
			for _, f := range u.Fields {
				convert(f.Type)
			}
			fmt.Fprintf(&b, "public struct %s {\n", n)
			var init []string
			for _, f := range u.Fields {
				fmt.Fprintf(&b, "    public var %s: %s\n", swiftPublic(f.Name), typ(f.Type))
				init = append(init, fmt.Sprintf("%s: %s = %s", swiftPublic(f.Name), typ(f.Type), defaultValue(f.Type)))
			}
			fmt.Fprintf(&b, "    public init(%s) {\n", strings.Join(init, ", "))
			for _, f := range u.Fields {
				fmt.Fprintf(&b, "        self.%s = %s\n", swiftPublic(f.Name), swiftPublic(f.Name))
			}
			b.WriteString("    }\n}\n")
		} else if u.Kind == ir.KArray || u.Kind == ir.KSlice {
			convert(u.Elem)
		}
		fmt.Fprintf(&b, "private func _gInput%d(_ _v: %s) throws -> GValue {\n", u.ID, typ(t))
		switch u.Kind {
		case ir.KBool:
			b.WriteString("    return .bool(_v)\n")
		case ir.KInt:
			fmt.Fprintf(&b, "    return GNumeric.normalize(UInt64(truncatingIfNeeded: _v), %d, %t)\n", u.Int.Bits(), u.Int.Signed())
		case ir.KFloat:
			fmt.Fprintf(&b, "    return .float(Double(_v), %d)\n", u.FloatBits)
		case ir.KString:
			b.WriteString("    return .string(_v.bytes)\n")
		case ir.KStruct:
			fmt.Fprintf(&b, "    let _result = GAggregate(%d)\n", u.ID)
			for i, f := range u.Fields {
				fmt.Fprintf(&b, "    _result.fields[%d].value = try _gInput%d(_v.%s)\n", i, f.Type.U().ID, swiftPublic(f.Name))
			}
			b.WriteString("    return .aggregate(_result)\n")
		case ir.KSlice, ir.KArray:
			if u.Kind == ir.KSlice {
				fmt.Fprintf(&b, "    guard let _v = _v else { return .slice(GSlice(nil, 0, 0, 0, %d)) }\n", u.Elem.ID)
			} else {
				fmt.Fprintf(&b, "    if _v.count != %d { throw GoalchemyFailure(\"invalid_argument\", \"array length must be %d\") }\n", u.Len, u.Len)
			}
			if elem := u.Elem.U(); elem.Kind == ir.KInt && elem.Int.Bits() == 8 && !elem.Int.Signed() {
				fmt.Fprintf(&b, "    let _buffer = GBuffer(bytes: _v, elem: %d)\n", u.Elem.ID)
			} else {
				fmt.Fprintf(&b, "    let _buffer = GBuffer(_v.count, %d)\n    for _i in _v.indices { _buffer.write(_i, try _gInput%d(_v[_i])) }\n", u.Elem.ID, u.Elem.U().ID)
			}
			if u.Kind == ir.KSlice {
				fmt.Fprintf(&b, "    return .slice(GSlice(_buffer, 0, _v.count, _v.count, %d))\n", u.Elem.ID)
			} else {
				fmt.Fprintf(&b, "    let _result = GAggregate(%d); _result.buffer = _buffer; return .aggregate(_result)\n", u.ID)
			}
		default:
			if cryptoKey(t) {
				b.WriteString("    guard let _v = _v else { return .nilValue }; return .pointer(_v.cell)\n")
			}
		}
		b.WriteString("}\n")
		fmt.Fprintf(&b, "private func _gOutput%d(_ _v: GValue) throws -> %s {\n", u.ID, typ(t))
		switch u.Kind {
		case ir.KBool:
			b.WriteString("    return _v.boolValue\n")
		case ir.KInt:
			fmt.Fprintf(&b, "    return %s(truncatingIfNeeded: _v.unsigned)\n", typ(t))
		case ir.KFloat:
			fmt.Fprintf(&b, "    guard case .float(let _number, _) = _v else { throw GFault(\"float boundary\") }; return %s(_number)\n", typ(t))
		case ir.KString:
			b.WriteString("    return GoString(bytes: _v.bytes)\n")
		case ir.KStruct:
			fmt.Fprintf(&b, "    guard case .aggregate(let _a) = _v else { throw GFault(\"struct boundary\") }; return %s(\n", typ(t))
			var fs []string
			for i, f := range u.Fields {
				fs = append(fs, fmt.Sprintf("        %s: try _gOutput%d(_a.fields[%d].value)", swiftPublic(f.Name), f.Type.U().ID, i))
			}
			b.WriteString(strings.Join(fs, ",\n") + "\n    )\n")
		case ir.KSlice, ir.KArray:
			if u.Kind == ir.KSlice {
				b.WriteString("    if case .slice(let _s) = _v, _s.storage == nil { return nil }\n")
			}
			if elem := u.Elem.U(); elem.Kind == ir.KInt && elem.Int.Bits() == 8 && !elem.Int.Signed() {
				b.WriteString("    guard let _bytes = GSlice.byteSource(_v) else { throw GFault(\"byte boundary\") }; return Array(_bytes)\n")
			} else {
				fmt.Fprintf(&b, "    return try GElements(_v).map { try _gOutput%d($0) }\n", u.Elem.U().ID)
			}
		default:
			if cryptoKey(t) {
				b.WriteString("    if case .nilValue = _v { return nil }; guard case .pointer(let _p) = _v, case .opaque(let _k) = _p.value, let _key = _k as? GCryptoKey else { throw GFault(\"_key boundary\") }; return GoalchemyKey(_key, _p)\n")
			}
		}
		b.WriteString("}\n")
	}
	funcs := append([]*ir.Func{}, e.p.Exports...)
	for _, h := range abi.Handles {
		funcs = append(funcs, h.Methods...)
	}
	for _, f := range funcs {
		params := f.Sig.Params
		if f.Recv != nil {
			params = params[1:]
		}
		for _, t := range params {
			if !sourceContext(t) && abi.Handle(t) == nil && ir.StreamKind(t) == "" {
				convert(t)
			}
		}
		for i, t := range f.Sig.Results {
			if (i == len(f.Sig.Results)-1 && sourceError(t)) || abi.Handle(t) != nil || ir.StreamKind(t) != "" {
				continue
			}
			convert(t)
		}
	}
	if e.p.HostIOFunc("ReadView") != nil {
		for _, name := range []string{"ReadView", "WriteView", "SeekView"} {
			f := e.p.HostIOFunc(name)
			for _, t := range f.Sig.Params[1:] {
				convert(t)
			}
			for _, t := range f.Sig.Results[:len(f.Sig.Results)-1] {
				convert(t)
			}
		}
	}
	var errorTypes []*ir.Type
	for _, t := range e.p.Types.All {
		if t.U().Kind == ir.KPointer && t.U().Elem.U().Kind == ir.KStruct && t.Boxed && libraryValue(t.U().Elem, map[*ir.Type]bool{}) {
			convert(t.U().Elem)
			errorTypes = append(errorTypes, t)
		}
	}
	b.WriteString("private func _gSourceError(_ _value: GValue) throws -> GoalchemyFailure? {\n    if case .nilValue = _value { return nil }\n    guard case .interface(let _box) = _value else { return GoalchemyFailure(\"source\", \"source error\") }\n")
	for _, t := range errorTypes {
		fmt.Fprintf(&b, "    if _box.type == %d, case .pointer(let _cell) = _box.value {\n        let _v = try _gOutput%d(_cell.value)\n", t.ID, t.U().Elem.U().ID)
		var fs []string
		for _, f := range t.U().Elem.U().Fields {
			fs = append(fs, fmt.Sprintf("            %s: _v.%s as Any", quote(f.Name), swiftPublic(f.Name)))
		}
		if len(fs) == 0 {
			b.WriteString("        _ = _v\n        return GoalchemyFailure(\"source\", \"source error\", [:])\n    }\n")
		} else {
			b.WriteString("        return GoalchemyFailure(\"source\", \"source error\", [\n" + strings.Join(fs, ",\n") + "\n        ])\n    }\n")
		}
	}
	b.WriteString("    return GoalchemyFailure(\"source\", String(decoding: GFormat(_value), as: UTF8.self))\n}\nprivate func _gLibraryReset() {\n    _ = _gTypeRegistration\n    _gSetupMethods()\n")
	for _, g := range e.p.Globals {
		fmt.Fprintf(&b, "    %s.value = GTypes.zero(%d)\n", e.names.Symbol(g.Sym), g.Type.ID)
	}
	b.WriteString("}\nprivate func _gLibrarySave() -> [GValue] {\n    return [")
	var saves []string
	for _, g := range e.p.Globals {
		saves = append(saves, e.names.Symbol(g.Sym)+".value")
	}
	b.WriteString(strings.Join(saves, ", "))
	b.WriteString("]\n}\nprivate func _gLibraryLoad(_ _v: [GValue]) {\n    _ = _gTypeRegistration\n    _gSetupMethods()\n")
	for i, g := range e.p.Globals {
		fmt.Fprintf(&b, "    %s.value = _v[%d]\n", e.names.Symbol(g.Sym), i)
	}
	b.WriteString("}\nprivate let _gLibraryState = GLibraryState(reset: _gLibraryReset, save: _gLibrarySave, load: _gLibraryLoad)\n")
	initFrame := "{ " + e.names.Symbol(e.p.Init.Sym) + "([], []) }"
	streamOps := func(name string, ops ir.StreamOps, obj string, closeStart string) {
		op := func(fn string, params []string, out string) string {
			f := e.p.HostIOFunc(fn)
			var inputs, args []string
			args = append(args, obj)
			for i, p := range params {
				inputs = append(inputs, fmt.Sprintf("try _gInput%d(%s)", f.Sig.Params[i+1].U().ID, p))
				args = append(args, fmt.Sprintf("_args[%d]", i))
			}
			last := len(f.Sig.Results) - 1
			return fmt.Sprintf("{ handle, %s in\n        let _args = try GLibrary.prepare { [%s] }\n        return try GLibrary.submitHandles(_args, CallOptions(), _gLibraryState, [handle], nil, [], GStreamIDs(0), %s, { _args, _context, _objects in %s([%s], []) }, { _rv, _ in\n            if let error = try _gSourceError(_rv[%d]) { throw error }\n            return %s\n        }).wait()\n    }",
				strings.Join(params, ", "), strings.Join(inputs, ", "), initFrame, e.names.Symbol(f.Sym), strings.Join(args, ", "), last, out)
		}
		res := func(fn string, i int) string {
			return fmt.Sprintf("try _gOutput%d(_rv[%d])", e.p.HostIOFunc(fn).Sig.Results[i].U().ID, i)
		}
		read, write, seek := "nil", "nil", "nil"
		if ops.Read {
			read = op("ReadView", []string{"_n"}, "("+res("ReadView", 0)+" ?? [], "+res("ReadView", 1)+")")
		}
		if ops.Write {
			write = op("WriteView", []string{"_d"}, "Int64("+res("WriteView", 0)+")")
		}
		if ops.Seek {
			seek = op("SeekView", []string{"_off", "_wh"}, "Int64("+res("SeekView", 0)+")")
		}
		fmt.Fprintf(&b, "private let %s = GStreamOps(\n    read: %s,\n    write: %s,\n    seek: %s,\n    close: { handle in try GLibrary.close(handle, _gLibraryState, %s, %s, { _rv, _ in if let error = try _gSourceError(_rv[0]) { throw error } }) })\n",
			name, read, write, seek, initFrame, closeStart)
	}
	unwrap := "{ if case .pointer(let _cell) = _objects[0] { return _cell.value }; return .nilValue }()"
	kinds := map[string]bool{}
	for _, f := range funcs {
		for _, t := range f.Sig.Results {
			if k := ir.StreamKind(t); k != "" && !kinds[k] {
				kinds[k] = true
				start := "nil"
				if ir.StreamCloses(k) {
					start = fmt.Sprintf("{ _args, _context, _objects in %s([%s], []) }", e.names.Symbol(e.p.HostIOFunc("CloseView").Sym), unwrap)
				}
				streamOps("_gStreamOps"+k, ir.KindOps(k), unwrap, start)
			}
		}
	}
	for _, h := range abi.Handles {
		if h.Stream.Any() {
			start := "nil"
			if h.Close != nil {
				start = fmt.Sprintf("{ _args, _context, _objects in %s([%s], []) }", e.names.Symbol(h.Close.Sym), handleRecv(h, h.Close))
			}
			streamOps("_gStreamOps"+h.Name, h.Stream, fmt.Sprintf(".interface(GInterface(%d, _objects[0]))", h.Ptr.ID), start)
		}
	}
	for _, h := range abi.Handles {
		fmt.Fprintf(&b, "/// Handle to a source *%s owned by a library instance. Call close() when\n/// done; deinit releases it without running Close.\npublic final class %s {\n    let handle: GHandle\n    init(_ handle: GHandle) { self.handle = handle }\n    deinit { GLibrary.finalize(handle) }\n", h.Name, swiftPublic(handleNames[h]))
		start := "nil"
		if h.Close != nil {
			start = fmt.Sprintf("{ _args, _context, _objects in %s([%s], []) }", e.names.Symbol(h.Close.Sym), handleRecv(h, h.Close))
		}
		fmt.Fprintf(&b, "    /// Runs the source Close, if any, and releases the handle. Queued calls\n    /// fail and an active call is canceled first.\n    public func close() throws {\n        try GLibrary.close(handle, _gLibraryState, %s, %s, { _rv, _ in if let error = try _gSourceError(_rv[0]) { throw error } })\n    }\n", initFrame, start)
		if h.Stream.Any() {
			fmt.Fprintf(&b, "    /// The handle as a stream; each operation is a call on the handle and\n    /// closing the stream closes the handle.\n    public func io() -> GoalchemySourceStream { GoalchemySourceStream(handle, _gStreamOps%s, owned: false) }\n", h.Name)
		}
		for _, f := range h.Methods {
			if err := e.libraryWrapper(&b, abi, ir.MethodName(f), h, f, typ, initFrame, used, reserved); err != nil {
				return "", err
			}
		}
		b.WriteString("}\n")
	}
	for _, f := range e.p.Exports {
		name := f.Name[strings.LastIndex(f.Name, ".")+1:]
		if used[name] || reserved[name] {
			return "", boundary("Swift public export collision %s", name)
		}
		used[name] = true
		if err := e.libraryWrapper(&b, abi, name, nil, f, typ, initFrame, used, reserved); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

// handleRecv is the source receiver argument of method f of h; a value
// receiver gets a copy of the object the handle points to.
func handleRecv(h *ir.Handle, f *ir.Func) string {
	if h.ValueReceiver(f) {
		return "{ if case .pointer(let _cell) = _objects[0] { return GCopy(_cell.value) }; return .nilValue }()"
	}
	return "_objects[0]"
}

func (e *emitter) libraryWrapper(b *strings.Builder, abi *ir.LibraryABI, name string, recv *ir.Handle, f *ir.Func, typ func(*ir.Type) string, initFrame string, used, reserved map[string]bool) error {
	ptyp := func(t *ir.Type) string {
		if h := abi.Handle(t); h != nil {
			// Qualified: inside a handle class a method may shadow the type name.
			return "GoalchemyGenerated." + swiftPublic(e.handleNames[h]) + "?"
		}
		if ir.StreamKind(t) != "" {
			return "GoalchemySourceStream?"
		}
		return typ(t)
	}
	results := f.Sig.Results
	n := len(results)
	hasErr := n > 0 && sourceError(results[n-1])
	if hasErr {
		n--
	}
	result := "Void"
	if n == 1 {
		result = ptyp(results[0])
	} else if n > 1 {
		var ts []string
		for _, t := range results[:n] {
			ts = append(ts, ptyp(t))
		}
		result = "(" + strings.Join(ts, ", ") + ")"
	}
	params := f.Sig.Params
	var ps, inputs, args, hs, streams, adapters, zeros []string
	if recv != nil {
		params = params[1:]
		hs = append(hs, "handle")
		args = append(args, handleRecv(recv, f))
	}
	inputIndex := 0
	for i, t := range params {
		if sourceContext(t) {
			args = append(args, "_context")
			continue
		}
		if k := ir.StreamKind(t); k != "" {
			ps = append(ps, fmt.Sprintf("_ a%d: GoalchemyStream?", i))
			n := len(streams)
			streams = append(streams, fmt.Sprintf("try GLibrary.stream(a%d, %q)", i, k))
			adapters = append(adapters, fmt.Sprintf("_ids.values[%d] == 0 ? nil : { %s([.integer(UInt64(bitPattern: _ids.values[%d]), 64, true)], []) }", n, e.names.Symbol(e.p.StreamAdapter(k).Sym), n))
			zeros = append(zeros, fmt.Sprintf("GTypes.zero(%d)", t.ID))
			args = append(args, fmt.Sprintf("_streams[%d]", n))
			continue
		}
		ps = append(ps, fmt.Sprintf("_ a%d: %s", i, ptyp(t)))
		if abi.Handle(t) != nil {
			hs = append(hs, fmt.Sprintf("a%d?.handle", i))
			args = append(args, fmt.Sprintf("_objects[%d]", len(hs)-1))
			continue
		}
		inputs = append(inputs, fmt.Sprintf("try _gInput%d(a%d)", t.U().ID, i))
		args = append(args, fmt.Sprintf("_args[%d]", inputIndex))
		inputIndex++
	}
	ps = append(ps, "_ options: CallOptions = CallOptions()")
	indent, prefix := "", "public func"
	if recv != nil {
		indent, prefix = "    ", "public func"
	}
	fmt.Fprintf(b, "%s%s %s(%s) -> Operation<%s> {\n%s    do {\n%s        let _args = try GLibrary.prepare {\n%s            _ = _gTypeRegistration\n%s            return [%s]\n%s        }\n", indent, prefix, swiftPublic(name), strings.Join(ps, ", "), result, indent, indent, indent, indent, strings.Join(inputs, ", "), indent)
	call := fmt.Sprintf("%s([%s], [])", e.names.Symbol(f.Sym), strings.Join(args, ", "))
	if len(streams) > 0 {
		call = fmt.Sprintf("GLibrary.streamFrame([%s], [%s]) { _streams in %s }", strings.Join(adapters, ", "), strings.Join(zeros, ", "), call)
	}
	fmt.Fprintf(b, "%s        let _ids = GStreamIDs(%d)\n%s        let _streams: [CallbackProvider?] = [%s]\n", indent, len(streams), indent, strings.Join(streams, ", "))
	fmt.Fprintf(b, "%s        return GLibrary.submitHandles(_args, options, _gLibraryState, [%s], nil, _streams, _ids, %s, { _args, _context, _objects in %s }, { _rv, _instance in\n", indent, strings.Join(hs, ", "), initFrame, call)
	if hasErr {
		fmt.Fprintf(b, "%s            if let error = try _gSourceError(_rv[%d]) { throw error }\n", indent, n)
	}
	out := func(i int) string {
		if h := abi.Handle(results[i]); h != nil {
			return fmt.Sprintf("GLibrary.wrap(_instance, _rv[%d], %q, GoalchemyGenerated.%s.init)", i, h.Name, swiftPublic(e.handleNames[h]))
		}
		if k := ir.StreamKind(results[i]); k != "" {
			return fmt.Sprintf("{ if case .nilValue = _rv[%d] { return nil }; return GLibrary.wrap(_instance, .pointer(GCell(_rv[%d])), \"io.%s\", { GoalchemySourceStream($0, _gStreamOps%s) }) }()", i, i, k, k)
		}
		return fmt.Sprintf("try _gOutput%d(_rv[%d])", results[i].U().ID, i)
	}
	if n == 0 {
		fmt.Fprintf(b, "%s            return ()\n", indent)
	} else {
		var xs []string
		for i := 0; i < n; i++ {
			xs = append(xs, out(i))
		}
		ret := strings.Join(xs, ", ")
		if n > 1 {
			ret = "(" + ret + ")"
		}
		fmt.Fprintf(b, "%s            return %s\n", indent, ret)
	}
	fmt.Fprintf(b, "%s        })\n%s    } catch { return Operation(options.cancellation) { throw error } }\n%s}\n", indent, indent, indent)
	return nil
}
