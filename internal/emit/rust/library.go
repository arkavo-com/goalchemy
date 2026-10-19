package rust

import (
	"fmt"
	"github.com/eugenioenko/goalchemy/internal/emit/artifact"
	"github.com/eugenioenko/goalchemy/internal/ir"
	"go/token"
	"sort"
	"strings"
)

type LibraryBoundaryError struct{ Message string }

func (e *LibraryBoundaryError) Error() string { return e.Message }
func libraryValue(t *ir.Type, seen map[*ir.Type]bool) bool {
	if seen[t] {
		return true
	}
	seen[t] = true
	u := t.U()
	switch u.Kind {
	case ir.KBool, ir.KInt, ir.KString, ir.KFloat:
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
	return false
}
func sourceContext(t *ir.Type) bool { return t.Kind == ir.KOpaque && t.Name == "context.Context" }
func sourceError(t *ir.Type) bool   { return t.Kind == ir.KNamed && t.Name == "error" }
func (e *emitter) library() (string, error) {
	if !e.noStd {
		e.use("core.task.spawn")
		e.use("std.context.with_cancel")
		e.use("lib.crypto.close")
	}
	names := map[*ir.Type]string{}
	roots := map[string]bool{}
	for _, f := range e.p.Exports {
		roots[f.Pkg] = true
	}
	used := map[string]bool{}
	reserved := map[string]bool{}
	for _, name := range strings.Fields("V H Key Code Func TypeDesc PanicObj GoPanicPayload ErrorKind LibraryError Cancellation ProviderRequest ProviderError Provider CallOptions Operation HostFault HostFatal SourceStackFatal HostWire HostRecord HostMailbox HostToken HostBoundary HostPending Step Results Frame Task Timer Sched Obj Heap Deferred FrData Fr TempRoots KeyIndex Entry GoMap MapIter EntryReservation SourceGuard Waiter WaiterData Chan Mutex WaitGroup ContextHook Context ContextHookHandle BlockedPayload HostError Vec Rc Cell RefCell Option Result String Some None Ok Err LogRecord LogHandler") {
		reserved[name] = true
	}
	if e.noStd {
		for _, name := range []string{"RuntimeOptions", "CallResult", "HeapStats", "NoStdError", "run"} {
			reserved[name] = true
		}
	}
	for _, f := range e.p.Exports {
		name := f.Name[strings.LastIndex(f.Name, ".")+1:]
		if reserved[name] {
			return "", &LibraryBoundaryError{fmt.Sprintf("reserved Rust public function name %s", name)}
		}
		if used[name] {
			return "", &LibraryBoundaryError{fmt.Sprintf("duplicate Rust public name %s", name)}
		}
		used[name] = true
	}
	abi, err := e.p.LibraryHandles(func(t *ir.Type) bool { return libraryValue(t, map[*ir.Type]bool{}) })
	if err != nil {
		return "", &LibraryBoundaryError{err.Error()}
	}
	handleElems := map[*ir.Type]bool{}
	for _, h := range abi.Handles {
		if reserved[h.Name] {
			return "", &LibraryBoundaryError{fmt.Sprintf("reserved Rust public type name %s", h.Name)}
		}
		if used[h.Name] {
			return "", &LibraryBoundaryError{fmt.Sprintf("duplicate Rust public type name %s", h.Name)}
		}
		used[h.Name] = true
		handleElems[h.Elem] = true
	}
	for _, t := range e.p.Types.All {
		if t.Kind == ir.KNamed && roots[t.Pkg] && t.U().Kind == ir.KStruct && token.IsExported(t.Obj) && !handleElems[t] && libraryValue(t, map[*ir.Type]bool{}) {
			n := t.Obj
			if reserved[n] {
				return "", &LibraryBoundaryError{fmt.Sprintf("reserved Rust public type name %s", n)}
			}
			if used[n] {
				return "", &LibraryBoundaryError{fmt.Sprintf("duplicate Rust public type name %s", n)}
			}
			used[n] = true
			names[t.U()] = n
		}
	}
	var typ func(*ir.Type) string
	typ = func(t *ir.Type) string {
		u := t.U()
		switch u.Kind {
		case ir.KBool:
			return "bool"
		case ir.KFloat:
			return fmt.Sprintf("f%d", u.FloatBits)
		case ir.KInt:
			return intKind(u)
		case ir.KString:
			return "Vec<u8>"
		case ir.KArray, ir.KSlice:
			return "Vec<" + typ(u.Elem) + ">"
		case ir.KStruct:
			if names[u] == "" {
				n := fmt.Sprintf("Value%d", u.ID)
				for used[n] || reserved[n] {
					n += "_"
				}
				used[n] = true
				names[u] = n
			}
			return names[u]
		}
		return "()"
	}
	var b strings.Builder
	seen := map[*ir.Type]bool{}
	var convert func(*ir.Type)
	convert = func(t *ir.Type) {
		u := t.U()
		if seen[u] {
			return
		}
		seen[u] = true
		switch u.Kind {
		case ir.KStruct:
			typ(u)
			for _, f := range u.Fields {
				convert(f.Type)
			}
		case ir.KArray, ir.KSlice:
			convert(u.Elem)
		}
		declarationStart := b.Len()
		if u.Kind == ir.KStruct {
			fmt.Fprintf(&b, "#[derive(Clone, Debug, Default, PartialEq)]\npub struct %s {\n", typ(u))
			for _, f := range u.Fields {
				fmt.Fprintf(&b, "pub %s: %s,\n", f.Name, typ(f.Type))
			}
			b.WriteString("}\n")
		}
		if u.Kind == ir.KStruct {
			e.api = append(e.api, artifact.APIDeclaration{Type: t, Name: typ(u), Signature: b.String()[declarationStart:]})
		}
		fmt.Fprintf(&b, "fn %s(v: %s) -> V {\n", e.names.Type(u, "input_"), typ(u))
		switch u.Kind {
		case ir.KFloat:
			b.WriteString("V::Float(v as f64)\n")
		case ir.KBool:
			b.WriteString("V::Bool(v)\n")
		case ir.KInt:
			b.WriteString("V::Int(v as i64)\n")
		case ir.KString:
			b.WriteString("s(&v)\n")
		case ir.KStruct:
			var fields []string
			for _, f := range u.Fields {
				fields = append(fields, fmt.Sprintf("%s(v.%s)", e.names.Type(f.Type.U(), "input_"), f.Name))
			}
			fmt.Fprintf(&b, "vals(vec![%s])\n", strings.Join(fields, ","))
		case ir.KArray, ir.KSlice:
			b.WriteString("let n=library_length(v.len());\n")
			if u.Kind == ir.KArray {
				fmt.Fprintf(&b, "if v.len()!=%d { library_invalid(\"invalid native array length\"); }\n", u.Len)
			}
			if byteElem(u.Elem) {
				if u.Kind == ir.KArray {
					b.WriteString("byte_array(v)\n")
				} else {
					b.WriteString("library_bytes(v)\n")
				}
			} else {
				fmt.Fprintf(&b, "let h=vals(v.into_iter().map(%s).collect()).h();\n", e.names.Type(u.Elem.U(), "input_"))
				if u.Kind == ir.KArray {
					b.WriteString("V::Obj(h)\n")
				} else {
					b.WriteString("V::Slice(h,0,n,n)\n")
				}
			}
		}
		b.WriteString("}\n")
		fmt.Fprintf(&b, "fn %s(v: V) -> %s {\n", e.names.Type(u, "output_"), typ(u))
		switch u.Kind {
		case ir.KFloat:
			fmt.Fprintf(&b, "v.f() as f%d\n", u.FloatBits)
		case ir.KBool:
			b.WriteString("v.b()\n")
		case ir.KInt:
			fmt.Fprintf(&b, "v.i() as %s\n", typ(u))
		case ir.KString:
			b.WriteString("v.bytes().to_vec()\n")
		case ir.KStruct:
			fmt.Fprintf(&b, "%s {\n", typ(u))
			for i, f := range u.Fields {
				fmt.Fprintf(&b, "%s:%s(fld(&v,%d)),\n", f.Name, e.names.Type(f.Type.U(), "output_"), i)
			}
			b.WriteString("}\n")
		case ir.KArray, ir.KSlice:
			if byteElem(u.Elem) {
				if u.Kind == ir.KArray {
					b.WriteString("byte_snapshot(v.h(),0,vals_len(v.h()))\n")
				} else {
					b.WriteString("library_byte_output(&v)\n")
				}
			} else {
				if u.Kind == ir.KArray {
					fmt.Fprintf(&b, "(0..%d).map(|i|%s(slot(v.h(),i))).collect()\n", u.Len, e.names.Type(u.Elem.U(), "output_"))
				} else {
					fmt.Fprintf(&b, "let(h,o,n,_,_)=slice_parts(&v);(0..n as usize).map(|i|%s(slot(h,o as usize+i))).collect()\n", e.names.Type(u.Elem.U(), "output_"))
				}
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
		for _, t := range f.Sig.Results {
			if !sourceError(t) && abi.Handle(t) == nil && ir.StreamKind(t) == "" {
				convert(t)
			}
		}
	}
	if e.noStd {
		e.noStdLibrary(&b, typ)
		return b.String(), nil
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
	b.WriteString("pub use rt::{Operation, CallOptions, LibraryError, ErrorKind, Cancellation, Provider, ProviderRequest, ProviderError, HostWire, set_log_handler, LogRecord, LogHandler, set_library_warn, WarningHandler, Stream, SourceStream};\nfn source_failure(v: &V) -> LibraryError {\nif veq(v,&context_canceled()) {return LibraryError::new(ErrorKind::Canceled,\"context canceled\");}\nif veq(v,&context_deadline_exceeded()) {return LibraryError::new(ErrorKind::DeadlineExceeded,\"context deadline exceeded\");}\n")
	var errorTypes []*ir.Type
	for _, t := range e.p.Types.All {
		if t.Kind == ir.KPointer && t.Elem.U().Kind == ir.KStruct && libraryValue(t.Elem, map[*ir.Type]bool{}) && e.tds[t] {
			errorTypes = append(errorTypes, t)
		}
	}
	sort.Slice(errorTypes, func(i, j int) bool { return errorTypes[i].ID < errorTypes[j].ID })
	for _, t := range errorTypes {
		fmt.Fprintf(&b, "if is_type(v, %s) { let x=unboxed(v); let mut err=LibraryError::from_bytes(ErrorKind::Source,format_panic_value(v));\n", e.td(t))
		for i, f := range t.Elem.U().Fields {
			switch f.Type.U().Kind {
			case ir.KString:
				fmt.Fprintf(&b, "err.fields.insert(%q.into(), HostWire::Bytes(fld(&x,%d).bytes().to_vec()));\n", f.Name, i)
			case ir.KInt:
				fmt.Fprintf(&b, "err.fields.insert(%q.into(), HostWire::Int(fld(&x,%d).i()));\n", f.Name, i)
			case ir.KSlice:
				if f.Type.U().Elem.U().Kind == ir.KString {
					fmt.Fprintf(&b, "err.fields.insert(%q.into(), HostWire::List(library_strings(fld(&x,%d)).into_iter().map(HostWire::Bytes).collect()));\n", f.Name, i)
				}
			}
		}
		b.WriteString("return err.normalize_cause(); }\n")
	}
	b.WriteString("LibraryError::from_bytes(ErrorKind::Source,format_panic_value(v))\n}\n")
	init := fmt.Sprintf("%s()", e.symbol(e.p.Init.Sym, "f_"))
	if !e.p.Init.MaySuspend {
		init = fmt.Sprintf("sync_frame(func(-1,%s,vec![]),vec![],0)", e.symbol(e.p.Init.Sym, "w_"))
	}
	call := func(f *ir.Func, args []string) string {
		if !f.MaySuspend {
			return fmt.Sprintf("sync_frame(func(-1,%s,vec![]),vec![%s],%d)", e.symbol(f.Sym, "w_"), strings.Join(args, ","), len(f.Sig.Results))
		}
		return fmt.Sprintf("%s(%s)", e.symbol(f.Sym, "f_"), strings.Join(args, ","))
	}
	owner := func(f *ir.Func, args []string, output string) string {
		c := call(f, args)
		return fmt.Sprintf("library_owner_call(%d, _init_zero_globals, options, &inst, fresh, move |ctx| {\nlet _ = &ctx;\nif fresh { library_sequence(%s, move || %s) } else { library_call(move || %s) }\n}, |rv, inst| {\nlet _ = inst;\n%s\n})", len(e.p.Globals), init, c, c, output)
	}
	streamOps := func(name string, ops ir.StreamOps, obj func(*ir.Type) string, closeBody string) {
		op := func(fn string, params []string, result, out string) string {
			f := e.p.HostIOFunc(fn)
			args := []string{obj(f.Sig.Params[0])}
			var ps []string
			for i, p := range params {
				args = append(args, fmt.Sprintf("%s(%s)", e.names.Type(f.Sig.Params[i+1].U(), "input_"), p))
				ps = append(ps, fmt.Sprintf("%s: %s", p, typ(f.Sig.Params[i+1])))
			}
			last := len(f.Sig.Results) - 1
			fname := fmt.Sprintf("%s_%s", name, strings.ToLower(fn))
			fmt.Fprintf(&b, "fn %s(h: &std::sync::Arc<rt::Handle>, %s) -> Operation<%s> {\nlet h0 = Some(h.clone());\nOperation::submit_handles(CallOptions::default(), vec![h0.clone()], None, vec![], Default::default(), move |options, inst, fresh| %s)\n}\n",
				fname, strings.Join(ps, ", "), result, owner(f, args, fmt.Sprintf("if !rv[%d].is_nil() {return Err(source_failure(&rv[%d]));}\nOk(%s)", last, last, out)))
			return "Some(" + fname + ")"
		}
		res := func(fn string, i int) string {
			t := e.p.HostIOFunc(fn).Sig.Results[i]
			return fmt.Sprintf("%s(rv[%d].clone())", e.names.Type(t.U(), "output_"), i)
		}
		read, write, seek := "None", "None", "None"
		if ops.Read {
			read = op("ReadView", []string{"n"}, "(Vec<u8>, bool)", "("+res("ReadView", 0)+", "+res("ReadView", 1)+")")
		}
		if ops.Write {
			write = op("WriteView", []string{"d"}, "i64", res("WriteView", 0)+" as i64")
		}
		if ops.Seek {
			seek = op("SeekView", []string{"off", "wh"}, "i64", res("SeekView", 0)+" as i64")
		}
		fmt.Fprintf(&b, "fn %s_close(h: &std::sync::Arc<rt::Handle>) -> Operation<()> {\n%s\n}\nstatic %s: rt::StreamOps = rt::StreamOps { read: %s, write: %s, seek: %s, close: %s_close };\n", name, closeBody, strings.ToUpper(name), read, write, seek, name)
	}
	kinds := map[string]bool{}
	for _, f := range funcs {
		for _, t := range f.Sig.Results {
			if k := ir.StreamKind(t); k != "" && !kinds[k] {
				kinds[k] = true
				body := "library_close(h, |_, _, _| Ok(()))"
				if ir.StreamCloses(k) {
					body = fmt.Sprintf("let h0 = Some(h.clone());\nlibrary_close(h, move |options, inst, fresh| %s)", owner(e.p.HostIOFunc("CloseView"), []string{"library_obj(&h0)"}, "if !rv[0].is_nil() {return Err(source_failure(&rv[0]));}\nOk(())"))
				}
				streamOps("stream_ops_"+strings.ToLower(k), ir.KindOps(k), func(*ir.Type) string { return "library_obj(&h0)" }, body)
			}
		}
	}
	for _, h := range abi.Handles {
		if h.Stream.Any() {
			body := "library_close(h, |_, _, _| Ok(()))"
			if h.Close != nil {
				body = fmt.Sprintf("let h0 = Some(h.clone());\nlibrary_close(h, move |options, inst, fresh| %s)", owner(h.Close, []string{e.handleRecv(h, h.Close, "library_obj(&h0)")}, "if !rv[0].is_nil() {return Err(source_failure(&rv[0]));}\nOk(())"))
			}
			td := e.td(h.Ptr)
			streamOps("stream_ops_handle_"+strings.ToLower(h.Name), h.Stream, func(*ir.Type) string { return "boxv(" + td + ", library_obj(&h0))" }, body)
		}
	}
	for _, h := range abi.Handles {
		e.api = append(e.api, artifact.APIDeclaration{Type: h.Elem, Name: h.Name, Signature: "pub struct " + h.Name})
		if h.Close != nil {
			e.api = append(e.api, artifact.APIDeclaration{Function: h.Close, Name: "close", Signature: "pub fn close(&self) -> Operation<()>"})
		}
		fmt.Fprintf(&b, "/// Handle to a source *%s owned by a library instance. Call close when done;\n/// dropping the last clone releases it without running Close.\n#[derive(Clone)]\npub struct %s(std::sync::Arc<rt::Handle>);\nimpl PartialEq for %s { fn eq(&self, other: &Self) -> bool { rt::Handle::same(&self.0, &other.0) } }\nimpl Eq for %s {}\nimpl std::fmt::Debug for %s { fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result { f.write_str(%q) } }\n#[allow(non_snake_case)]\nimpl %s {\n", h.Name, h.Name, h.Name, h.Name, h.Name, h.Name, h.Name)
		b.WriteString("/// Runs the source Close, if any, and releases the handle. Queued calls fail\n/// and an active call is canceled first.\npub fn close(&self) -> Operation<()> {\n")
		if h.Close != nil {
			fmt.Fprintf(&b, "let h0 = Some(self.0.clone());\nlibrary_close(&self.0, move |options, inst, fresh| %s)\n}\n", owner(h.Close, []string{e.handleRecv(h, h.Close, "library_obj(&h0)")}, "if !rv[0].is_nil() {return Err(source_failure(&rv[0]));}\nOk(())"))
		} else {
			b.WriteString("library_close(&self.0, |_, _, _| Ok(()))\n}\n")
		}
		if h.Stream.Any() {
			fmt.Fprintf(&b, "/// The handle as a std::io stream; each operation is a call on the handle and\n/// closing the stream closes the handle.\npub fn io(&self) -> SourceStream {\nSourceStream::new(self.0.clone(), &STREAM_OPS_HANDLE_%s)\n}\n", strings.ToUpper(h.Name))
		}
		for _, f := range h.Methods {
			name := ir.MethodName(f)
			// Self is a Rust keyword that has no raw identifier form.
			if name == "Self" {
				name = "Self_"
			}
			e.libraryWrapper(&b, abi, name, h, f, typ, owner)
		}
		b.WriteString("}\n")
	}
	for _, f := range e.p.Exports {
		name := f.Name[strings.LastIndex(f.Name, ".")+1:]
		e.libraryWrapper(&b, abi, name, nil, f, typ, owner)
	}
	return b.String(), nil
}

// handleRecv is the source receiver argument of method f of h on obj; a value
// receiver gets a copy.
func (e *emitter) handleRecv(h *ir.Handle, f *ir.Func, obj string) string {
	if h.ValueReceiver(f) {
		return e.cloneExpr(f.RecvType, obj, false)
	}
	return obj
}

func (e *emitter) libraryWrapper(b *strings.Builder, abi *ir.LibraryABI, name string, recv *ir.Handle, f *ir.Func, typ func(*ir.Type) string, owner func(*ir.Func, []string, string) string) {
	ptyp := func(t *ir.Type, param bool) string {
		if h := abi.Handle(t); h != nil {
			if param {
				return "Option<&" + h.Name + ">"
			}
			return "Option<" + h.Name + ">"
		}
		if ir.StreamKind(t) != "" {
			return "Option<SourceStream>"
		}
		return typ(t)
	}
	params := f.Sig.Params
	var ps, args, hs, pre, streams []string
	if recv != nil {
		params = params[1:]
		ps = append(ps, "&self")
		pre = append(pre, "let h0 = Some(self.0.clone());")
		hs = append(hs, "h0.clone()")
		args = append(args, e.handleRecv(recv, f, "library_obj(&h0)"))
	}
	for i, t := range params {
		if sourceContext(t) {
			args = append(args, "ctx.clone()")
			continue
		}
		a := fmt.Sprintf("a%d", i)
		if k := ir.StreamKind(t); k != "" {
			ps = append(ps, a+": Option<Stream>")
			pre = append(pre, fmt.Sprintf("let s%d = match library_stream(%s, %q) { Ok(s) => s, Err(e) => return Operation::settled(Err(e)) };", i, a, k))
			args = append(args, fmt.Sprintf("{ let id = ids.lock().unwrap()[%d]; if id == 0 { V::Nil } else { %s(V::Int(id)) } }", len(streams), e.symbol(e.p.StreamAdapter(k).Sym, "f_")))
			streams = append(streams, fmt.Sprintf("s%d", i))
			continue
		}
		ps = append(ps, a+": "+ptyp(t, true))
		if abi.Handle(t) != nil {
			pre = append(pre, fmt.Sprintf("let h%d = %s.map(|x| x.0.clone());", i+1, a))
			hs = append(hs, fmt.Sprintf("h%d.clone()", i+1))
			args = append(args, fmt.Sprintf("library_obj(&h%d)", i+1))
			continue
		}
		args = append(args, fmt.Sprintf("%s(%s)", e.names.Type(t.U(), "input_"), a))
	}
	results := f.Sig.Results
	n := len(results)
	hasError := n > 0 && sourceError(results[n-1])
	if hasError {
		n--
	}
	var rs, outs []string
	for i := 0; i < n; i++ {
		rs = append(rs, ptyp(results[i], false))
		if h := abi.Handle(results[i]); h != nil {
			outs = append(outs, fmt.Sprintf("library_wrap(inst, rv[%d].clone(), %q).map(%s)", i, h.Name, h.Name))
			continue
		}
		if k := ir.StreamKind(results[i]); k != "" {
			outs = append(outs, fmt.Sprintf("library_wrap(inst, rv[%d].clone(), \"io.%s\").map(|h| SourceStream::new(h, &STREAM_OPS_%s))", i, k, strings.ToUpper(k)))
			continue
		}
		outs = append(outs, fmt.Sprintf("%s(rv[%d].clone())", e.names.Type(results[i].U(), "output_"), i))
	}
	result, out := "()", "()"
	if n == 1 {
		result, out = rs[0], outs[0]
	} else if n > 1 {
		result = "(" + strings.Join(rs, ",") + ")"
		out = "(" + strings.Join(outs, ",") + ")"
	}
	ps = append(ps, "options: CallOptions")
	output := ""
	if hasError {
		output = fmt.Sprintf("if !rv[%d].is_nil() {return Err(source_failure(&rv[%d]));}\n", n, n)
	}
	output += "Ok(" + out + ")"
	pre = append(pre, fmt.Sprintf("let ids = std::sync::Arc::new(std::sync::Mutex::new(vec![0i64; %d]));", len(streams)))
	e.api = append(e.api, artifact.APIDeclaration{Function: f, Name: name, Signature: fmt.Sprintf("pub fn %s(%s) -> Operation<%s>", name, strings.Join(ps, ","), result)})
	fmt.Fprintf(b, "pub fn %s(%s) -> Operation<%s> {\n%s\nOperation::submit_handles(options, vec![%s], None, vec![%s], ids.clone(), move |options, inst, fresh| %s)\n}\n",
		name, strings.Join(ps, ","), result, strings.Join(pre, "\n"), strings.Join(hs, ","), strings.Join(streams, ","), owner(f, args, output))
}
