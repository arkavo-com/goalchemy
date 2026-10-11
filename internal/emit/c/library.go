package c

import (
	"bytes"
	"fmt"
	"github.com/eugenioenko/goalchemy/internal/ir"
	"go/token"
	"sort"
	"strings"
)

func cValueType(t *ir.Type, seen map[*ir.Type]bool) bool {
	if seen[t] {
		return true
	}
	seen[t] = true
	u := t.U()
	switch u.Kind {
	case ir.KBool, ir.KInt, ir.KString, ir.KFloat:
		return true
	case ir.KArray, ir.KSlice:
		return cValueType(u.Elem, seen)
	case ir.KStruct:
		for _, f := range u.Fields {
			if !token.IsExported(f.Name) || f.Embedded || !cValueType(f.Type, seen) {
				return false
			}
		}
		return true
	}
	return false
}
func nativeBytes(t *ir.Type) bool { u := t.U(); return u.Kind == ir.KInt && u.Int == ir.U8 }
func cContext(t *ir.Type) bool    { return t.Kind == ir.KOpaque && t.Name == "context.Context" }
func cError(t *ir.Type) bool      { return t.Kind == ir.KNamed && t.Name == "error" }
func (e *emitter) valueLibrary(out *bytes.Buffer) ([]byte, error) {
	e.use("core.task.spawn")
	e.use("std.context.with_cancel")
	e.use("std.context.with_timeout")
	out.WriteString(`#include "library.h"
extern const gxc_options *gxc_active_options;
#include <stdlib.h>
#include <stdio.h>
#include <limits.h>
void goalchemy_wake(void) {gx_library_wake();}
static pthread_mutex_t gxc_serial = PTHREAD_MUTEX_INITIALIZER;
static const gxc_value *gxc_args;
static const gxc_options *gxc_opts;
static gxc_value *gxc_result;
static gxc_error *gxc_failure;
static int gxc_export;
static gx_V gxc_context;
static void gxc_invalid(void) { gxc_failure->kind=5; gx_host_fault("invalid native library value"); }
typedef struct gxc_allocation {void *pointer;struct gxc_allocation *next;} gxc_allocation;
static gxc_allocation *gxc_allocations;
static size_t gxc_output_bytes,gxc_output_nodes;static unsigned gxc_output_depth;
static void gxc_allocation_release(bool failed) {while(gxc_allocations){gxc_allocation *node=gxc_allocations;gxc_allocations=node->next;if(failed)free(node->pointer);free(node);}}
static void *gxc_alloc(size_t n) { if(n>256u*1024u*1024u-gxc_output_bytes)gx_host_fault("native result byte budget");gxc_output_bytes+=n; void *p=calloc(n?n:1,1); if(!p) gx_host_fault("native library allocation");gxc_allocation *node=malloc(sizeof(*node));if(!node){free(p);gx_host_fault("native library allocation tracking");}node->pointer=p;node->next=gxc_allocations;gxc_allocations=node;return p; }
static gxc_value gxc_bytes_out(const void *p,size_t n) {
 gxc_value v={.kind=GXC_BYTES,.length=n}; v.bytes=gxc_alloc(n); if(n) memcpy(v.bytes,p,n); return v;
}
static gxc_value gxc_list_out(gxc_kind kind,size_t n) {
 if(n>SIZE_MAX/sizeof(gxc_value)) gx_host_fault("native result length");
 gxc_value v={.kind=kind,.length=n}; v.items=gxc_alloc(n*sizeof(*v.items));
 if(kind==GXC_RECORD) v.names=gxc_alloc(n*sizeof(*v.names)); return v;
}
static void gxc_name(gxc_value *v,size_t i,const char *name) {
 size_t n=strlen(name); v->names[i]=gxc_alloc(n+1); memcpy(v->names[i],name,n+1);
}
typedef struct gxc_instance {gx_V *globals,*objects;gxc_handle **handles;size_t count,cap,live;bool poisoned;uint64_t domain;gxc_stream *streams;size_t nstreams;} gxc_instance;
struct gxc_handle {gxc_instance *inst;size_t slot;const char *type;int close_entry;atomic_bool closed,cancel;bool released;};
static gxc_instance *gxc_inst;
static bool gxc_fresh,gxc_keep;
static gxc_handle *gxc_self;
static gxc_handle *gxc_hargs[256];
static size_t gxc_left;
static void (*gxc_warn_handler)(void *,const char *);static void *gxc_warn_state;
void gxc_set_warning_handler(void (*handler)(void *state,const char *message),void *state){pthread_mutex_lock(&gxc_serial);gxc_warn_handler=handler;gxc_warn_state=state;pthread_mutex_unlock(&gxc_serial);}
static void gxc_warn(const char *m){if(gxc_warn_handler)gxc_warn_handler(gxc_warn_state,m);else fprintf(stderr,"goalchemy: warning: %s\n",m);}
const char *gxc_handle_type(const gxc_handle *h){return h?h->type:NULL;}
static gx_V gxc_obj(const gxc_value *v){gxc_handle *h=v->kind==GXC_HANDLE?v->handle:NULL;return h&&!h->released?h->inst->objects[h->slot]:gx_nil();}
static gxc_handle *gxc_arg_handle(const gxc_value *v,const char *type){if(v->kind==GXC_NIL)return NULL;if(v->kind!=GXC_HANDLE||!v->handle||strcmp(v->handle->type,type))gxc_invalid();return v->handle;}
static gxc_value gxc_wrap(gx_V v,const char *type,int close_entry){
 if(gx_is_nil(v))return (gxc_value){0};
 gxc_instance *in=gxc_inst;
 for(size_t i=0;i<in->count;i++){gxc_handle *h=in->handles[i];if(h&&!atomic_load(&h->closed)&&in->objects[i].t==v.t&&in->objects[i].u.p==v.u.p&&in->objects[i].l==v.l&&in->objects[i].pad==v.pad)return (gxc_value){.kind=GXC_HANDLE,.handle=h};}
 if(in->count==in->cap){size_t c=in->cap?in->cap*2:8;gx_V *o=GC_MALLOC_UNCOLLECTABLE(c*sizeof(gx_V));gxc_handle **hs=calloc(c,sizeof(*hs));if(!o||!hs)gx_host_fault("handle table allocation");
  if(in->count){memcpy(o,in->objects,in->count*sizeof(gx_V));memcpy(hs,in->handles,in->count*sizeof(*hs));}GC_FREE(in->objects);free(in->handles);in->objects=o;in->handles=hs;in->cap=c;}
 gxc_handle *h=calloc(1,sizeof(*h));if(!h)gx_host_fault("handle allocation");
 h->inst=in;h->slot=in->count;h->type=type;h->close_entry=close_entry;atomic_init(&h->closed,false);atomic_init(&h->cancel,false);
 in->objects[in->count]=v;in->handles[in->count]=h;in->count++;in->live++;
 return (gxc_value){.kind=GXC_HANDLE,.handle=h};
}
static void gxc_retire(gxc_instance *in){
 if(gx_native_domain_release&&in->domain)gx_native_domain_release(in->domain);
 for(size_t i=0;i<in->count;i++)if(in->handles[i])in->handles[i]->inst=NULL;
 GC_FREE(in->globals);GC_FREE(in->objects);free(in->handles);free(in->streams);free(in);
}
/* Requires the serial lock. Returns true when the instance retired. */
static bool gxc_release(gxc_handle *h){
 if(h->released)return false;h->released=true;gxc_instance *in=h->inst;
 in->objects[h->slot]=gx_nil();in->handles[h->slot]=NULL;
 if(--in->live)return false;
 gxc_retire(in);return true;
}
static int64_t gxc_stream_ids[256];
static gxc_options gxc_merged;
static const gxc_options *gxc_user;
static void gxc_put(uint8_t *b,int64_t v){for(int i=7;i>=0;i--){b[i]=(uint8_t)v;v>>=8;}}
static int64_t gxc_get(const uint8_t *b){int64_t v=0;for(int i=0;i<8;i++)v=(int64_t)((uint64_t)v<<8|b[i]);return v;}
/* Serves hostio requests on a worker; the owner thread waits meanwhile. */
static int gxc_serve(const gxc_stream *s,const uint8_t *req,size_t n,gxc_value *response){
 char message[256]={0};int64_t r=-1;size_t len=1;uint8_t *out=NULL;
 if(n==9&&req[0]=='r'&&s->read){int64_t want=gxc_get(req+1);if(want>=0&&want<128*1024){out=calloc(1,(size_t)want+1);if(out){r=s->read(s->state,out+1,(size_t)want,message);if(r>=0&&r<=want){len=1+(size_t)r;out[0]=r==0&&want>0?1:0;}else if(r>=0){r=-1;strcpy(message,"hostio: invalid read count");}}}}
 else if(n>=1&&req[0]=='w'&&s->write){out=calloc(1,9);if(out){r=s->write(s->state,req+1,n-1,message);if(r>=0){len=9;gxc_put(out+1,r);}}}
 else if(n==10&&req[0]=='s'&&s->seek){out=calloc(1,9);if(out){r=s->seek(s->state,gxc_get(req+1),req[9],message);if(r>=0){len=9;gxc_put(out+1,r);}}}
 else if(n==1&&req[0]=='c'){out=calloc(1,1);r=s->close?s->close(s->state,message):0;}
 else strcpy(message,"hostio: malformed stream request");
 if(r<0){free(out);message[255]=0;len=1+strlen(message);out=malloc(len);if(!out)return 3;out[0]=2;memcpy(out+1,message,len-1);}
 if(!out)return 3;
 *response=(gxc_value){.kind=GXC_BYTES,.bytes=out,.length=len};return 0;
}
static int gxc_dispatch(void *state,const uint8_t *name,size_t name_length,const uint8_t *payload,size_t payload_length,const atomic_bool *canceled,gxc_value *response){
 static const char prefix[]="goalchemy.stream.";gxc_instance *in=state;
 if(name_length>sizeof prefix-1&&!memcmp(name,prefix,sizeof prefix-1)){
  size_t id=0;for(size_t i=sizeof prefix-1;i<name_length;i++){if(name[i]<'0'||name[i]>'9')return 1;id=id*10+(name[i]-'0');}
  if(!id||id>in->nstreams)return 1;
  gxc_stream_thread=true;int rc=gxc_serve(&in->streams[id-1],payload,payload_length,response);gxc_stream_thread=false;return rc;
 }
 if(!gxc_user||!gxc_user->provider)return 1;
 return gxc_user->provider(gxc_user->provider_state,name,name_length,payload,payload_length,canceled,response);
}
/* Registers the call's streams on its instance and routes callbacks. */
static bool gxc_streams(const gxc_value *args,size_t count,const gxc_options **opts){
 for(size_t i=0;i<count&&i<256;i++){gxc_stream_ids[i]=0;if(args[i].kind!=GXC_STREAM)continue;
  gxc_stream *grown=realloc(gxc_inst->streams,(gxc_inst->nstreams+1)*sizeof(*grown));if(!grown)return false;
  gxc_inst->streams=grown;grown[gxc_inst->nstreams++]=*args[i].stream;gxc_stream_ids[i]=(int64_t)gxc_inst->nstreams;}
 if(!gxc_inst->nstreams)return true;
 gxc_user=*opts;gxc_merged=*opts?**opts:(gxc_options){0};gxc_merged.provider=gxc_dispatch;gxc_merged.provider_state=gxc_inst;
 *opts=&gxc_merged;return true;
}
static gx_V gxc_stream_arg(size_t i,gx_V (*adapt)(gx_V),gx_V zero){return gxc_stream_ids[i]?adapt(gx_int(gxc_stream_ids[i])):zero;}
static void gxc_poll(void) {
 if((gxc_opts && gxc_opts->canceled && atomic_load(gxc_opts->canceled))||(gxc_self&&atomic_load(&gxc_self->cancel)))
  gx_cancel_ctx(gxc_context,gx_context_canceled());
}
`)
	seen := map[*ir.Type]bool{}
	var convert func(*ir.Type)
	convert = func(t *ir.Type) {
		u := t.U()
		if seen[u] {
			return
		}
		seen[u] = true
		e.localProto("static gx_V gxc_in_%s(const gxc_value *v)", e.names.Type(u, ""))
		e.localProto("static gxc_value gxc_out_%s(gx_V v)", e.names.Type(u, ""))
		e.localProto("static gxc_value gxc_out_body_%s(gx_V v)", e.names.Type(u, ""))
		switch u.Kind {
		case ir.KStruct:
			for _, f := range u.Fields {
				convert(f.Type)
			}
		case ir.KArray, ir.KSlice:
			convert(u.Elem)
		}
		fmt.Fprintf(out, "static gx_V gxc_in_%s(const gxc_value *v) {\n", e.names.Type(u, ""))
		fmt.Fprintf(out, " if(v->kind==GXC_NIL) return %s;\n", e.zero(t))
		switch u.Kind {
		case ir.KFloat:
			fmt.Fprintf(out, " if(v->kind!=GXC_FLOAT) gxc_invalid(); return gx_float(gx_round_float(v->floating,%d));\n", u.FloatBits)
		case ir.KBool:
			out.WriteString(" if(v->kind!=GXC_BOOL || (v->integer!=0 && v->integer!=1)) gxc_invalid(); return gx_bool(v->integer);\n")
		case ir.KInt:
			out.WriteString(" if(v->kind!=GXC_INT) gxc_invalid();\n")
			if u.Int != ir.I64 && u.Int != ir.U64 {
				fmt.Fprintf(out, " if((int64_t)(%s)v->integer!=v->integer) gxc_invalid();\n", fmt.Sprintf("%sint%d_t", map[bool]string{true: "", false: "u"}[u.Int.Signed()], u.Int.Bits()))
			}
			out.WriteString(" return gx_int(v->integer);\n")
		case ir.KString:
			out.WriteString(" if(v->kind!=GXC_BYTES) gxc_invalid(); return gx_str((const char *)v->bytes,v->length);\n")
		case ir.KStruct:
			out.WriteString(" if(v->kind!=GXC_RECORD) gxc_invalid();\n")
			fmt.Fprintf(out, " gx_V r=gx_new_vals(%d,NULL);\n", len(u.Fields))
			for i, f := range u.Fields {
				fmt.Fprintf(out, " gx_fset(r,%d,gxc_in_%s(gxc_field(v,%q)));\n", i, e.names.Type(f.Type.U(), ""), f.Name)
			}
			out.WriteString(" return r;\n")
		case ir.KArray, ir.KSlice:
			if u.Kind == ir.KArray {
				fmt.Fprintf(out, " if(v->length!=%d) gxc_invalid();\n", u.Len)
			}
			if nativeBytes(u.Elem) {
				out.WriteString(" if(v->kind!=GXC_BYTES) gxc_invalid(); uint8_t *b=gx_alloc_bytes(v->length); if(v->length) memcpy(b,v->bytes,v->length);\n")
				if u.Kind == ir.KArray {
					out.WriteString(" gx_V r=gx_obj(b); r.pad=1; return r;\n")
				} else {
					out.WriteString(" return gx_byte_slice(b,(uint32_t)v->length,(uint32_t)v->length);\n")
				}
			} else {
				out.WriteString(" if(v->kind!=GXC_LIST) gxc_invalid(); gx_V *a=gx_alloc_vals(v->length);\n")
				fmt.Fprintf(out, " for(size_t i=0;i<v->length;i++) a[i]=gxc_in_%s(&v->items[i]);\n", e.names.Type(u.Elem.U(), ""))
				if u.Kind == ir.KArray {
					out.WriteString(" return gx_obj(a);\n")
				} else {
					out.WriteString(" return gx_slice(a,(uint32_t)v->length,(uint32_t)v->length);\n")
				}
			}
		}
		out.WriteString("}\n")
		fmt.Fprintf(out, "static gxc_value gxc_out_%s(gx_V v) {if(gxc_output_depth>=64 || ++gxc_output_nodes>1000000)gx_host_fault(\"native result tree budget\");gxc_output_depth++;gxc_value r=gxc_out_body_%s(v);gxc_output_depth--;return r;}\nstatic gxc_value gxc_out_body_%s(gx_V v) {\n", e.names.Type(u, ""), e.names.Type(u, ""), e.names.Type(u, ""))
		switch u.Kind {
		case ir.KFloat:
			out.WriteString(" return (gxc_value){.kind=GXC_FLOAT,.floating=gx_f(v)};\n")
		case ir.KBool:
			out.WriteString(" return (gxc_value){.kind=GXC_BOOL,.integer=gx_b(v)};\n")
		case ir.KInt:
			out.WriteString(" return (gxc_value){.kind=GXC_INT,.integer=gx_i(v)};\n")
		case ir.KString:
			out.WriteString(" return gxc_bytes_out(gx_sbytes(v),gx_slen(v));\n")
		case ir.KStruct:
			fmt.Fprintf(out, " gxc_value r=gxc_list_out(GXC_RECORD,%d);\n", len(u.Fields))
			for i, f := range u.Fields {
				fmt.Fprintf(out, " gxc_name(&r,%d,%q); r.items[%d]=gxc_out_%s(gx_fld(v,%d));\n", i, f.Name, i, e.names.Type(f.Type.U(), ""), i)
			}
			out.WriteString(" return r;\n")
		case ir.KArray, ir.KSlice:
			n := "v.l"
			if u.Kind == ir.KArray {
				n = fmt.Sprint(u.Len)
			}
			if nativeBytes(u.Elem) {
				fmt.Fprintf(out, " return gxc_bytes_out(gx_bytes(v),%s);\n", n)
			} else {
				fmt.Fprintf(out, " gxc_value r=gxc_list_out(GXC_LIST,%s); for(size_t i=0;i<r.length;i++) r.items[i]=gxc_out_%s(gx_vals(v)[i]); return r;\n", n, e.names.Type(u.Elem.U(), ""))
			}
		}
		out.WriteString("}\n")
	}
	abi, err := e.p.LibraryHandles(func(t *ir.Type) bool { return cValueType(t, map[*ir.Type]bool{}) })
	if err != nil {
		return nil, err
	}
	type entry struct {
		name  string
		f     *ir.Func
		recv  *ir.Handle
		close bool
	}
	var entries []entry
	for _, f := range e.p.Exports {
		entries = append(entries, entry{name: f.Name[strings.LastIndex(f.Name, ".")+1:], f: f})
	}
	closeEntry := map[*ir.Handle]int{}
	for _, h := range abi.Handles {
		for _, f := range h.Methods {
			entries = append(entries, entry{name: h.Name + "." + ir.MethodName(f), f: f, recv: h})
		}
	}
	for _, h := range abi.Handles {
		closeEntry[h] = -1
		if h.Close != nil {
			closeEntry[h] = len(entries)
			entries = append(entries, entry{name: "", f: h.Close, recv: h, close: true})
		}
	}
	params := func(en entry) []*ir.Type {
		if en.recv != nil {
			return en.f.Sig.Params[1:]
		}
		return en.f.Sig.Params
	}
	for _, en := range entries {
		for _, t := range params(en) {
			if !cContext(t) && abi.Handle(t) == nil && ir.StreamKind(t) == "" {
				convert(t)
			}
		}
		for _, t := range en.f.Sig.Results {
			if !cError(t) && abi.Handle(t) == nil {
				convert(t)
			}
		}
	}
	var errors []*ir.Type
	for _, t := range e.p.Types.All {
		if t.Kind == ir.KPointer && t.Elem.U().Kind == ir.KStruct && cValueType(t.Elem, map[*ir.Type]bool{}) && e.tds[t] {
			errors = append(errors, t)
			convert(t.Elem)
		}
	}
	sort.Slice(errors, func(i, j int) bool { return errors[i].ID < errors[j].ID })
	out.WriteString("static void gxc_source_error(gx_V v) {\n gxc_failure->kind=1; gx_Buf b={0}; gx_format_panic_value(v,&b); gxc_failure->message=gxc_bytes_out(b.b,b.n);\n")
	for _, t := range errors {
		fmt.Fprintf(out, " if(gx_is_type(v,&td_%s)) { gxc_failure->fields=gxc_out_%s(gx_unboxed(v)); return; }\n", e.names.Type(t, ""), e.names.Type(t.Elem.U(), ""))
	}
	out.WriteString("}\n")
	out.WriteString("static void gxc_save(gxc_instance *in) {\n if(!in->globals){in->globals=GC_MALLOC_UNCOLLECTABLE(sizeof(gx_V)*" + fmt.Sprint(len(e.p.Globals)+1) + ");if(!in->globals)gx_host_fault(\"instance globals allocation\");}\n")
	for i, g := range e.p.Globals {
		fmt.Fprintf(out, " in->globals[%d]=%s;\n", i, e.names.Symbol(g.Sym))
	}
	out.WriteString("}\nstatic void gxc_load(gxc_instance *in) {\n")
	for i, g := range e.p.Globals {
		fmt.Fprintf(out, " %s=in->globals[%d];\n", e.names.Symbol(g.Sym), i)
	}
	out.WriteString("}\n")
	fmt.Fprintf(out, "static void gxc_init_source(void) {\n if(gxc_fresh){init_zero_globals();gxc_inst->domain=gx_domain();gx_sched->domain=gxc_inst->domain;f_%s();}\n else {gxc_load(gxc_inst);gx_sched->domain=gxc_inst->domain;}\n}\n", e.names.Symbol(e.p.Init.Sym))
	out.WriteString("static gx_V gxc_call_source(void) { gx_V c=gx_std_context_with_cancel(gx_background()); gxc_context=gx_at(c,0);\n if(gxc_opts && gxc_opts->timeout_nanoseconds>0) gxc_context=gx_at(gx_std_context_with_timeout(gxc_context,gx_int(gxc_opts->timeout_nanoseconds)),0);\n gxc_poll(); switch(gxc_export) {\n")
	for k, en := range entries {
		f := en.f
		var as []string
		index := 0
		if en.recv != nil {
			as = append(as, "gxc_obj(&gxc_args[0])")
			index++
		}
		for _, t := range params(en) {
			if cContext(t) {
				as = append(as, "gxc_context")
			} else if abi.Handle(t) != nil {
				as = append(as, fmt.Sprintf("gxc_obj(&gxc_args[%d])", index))
				index++
			} else if k := ir.StreamKind(t); k != "" {
				as = append(as, fmt.Sprintf("gxc_stream_arg(%d,f_%s,%s)", index, e.names.Symbol(e.p.StreamAdapter(k).Sym), e.zero(t)))
				index++
			} else {
				as = append(as, fmt.Sprintf("gxc_in_%s(&gxc_args[%d])", e.names.Type(t.U(), ""), index))
				index++
			}
		}
		if f.MaySuspend {
			fmt.Fprintf(out, " case %d: return f_%s(%s);\n", k, e.names.Symbol(f.Sym), strings.Join(as, ","))
		} else {
			vec, n := vec(as)
			fmt.Fprintf(out, " case %d: return gx_sync_frame(gx_func(-1,w_%s,0,NULL),%d,%s,%d);\n", k, e.names.Symbol(f.Sym), n, vec, len(f.Sig.Results))
		}
	}
	out.WriteString(" } gx_host_fault(\"unknown export\"); }\nstatic void gxc_root_step(gx_Task *t,gx_Frame *f) {if(!f->pc){f->pc=1;gx_call(t,gxc_call_source());return;}gx_ret(t,f); }\nstatic gx_V gxc_entry(void) {return gx_vframe(gx_new_frame(0,gxc_root_step,NULL));}\nstatic void gxc_finish(gx_Task *t) { switch(gxc_export) {\n")
	for k, en := range entries {
		f := en.f
		n := len(f.Sig.Results)
		hasError := n > 0 && cError(f.Sig.Results[n-1])
		if hasError {
			n--
		}
		res := func(i int) string {
			if h := abi.Handle(f.Sig.Results[i]); h != nil {
				return fmt.Sprintf("gxc_wrap(gx_rv(t,%d),%q,%d)", i, h.Name, closeEntry[h])
			}
			return fmt.Sprintf("gxc_out_%s(gx_rv(t,%d))", e.names.Type(f.Sig.Results[i].U(), ""), i)
		}
		fmt.Fprintf(out, " case %d:\n", k)
		if hasError {
			fmt.Fprintf(out, " if(!gx_is_nil(gx_rv(t,%d))) {gxc_source_error(gx_rv(t,%d)); break;}\n", n, n)
		}
		if n == 1 {
			fmt.Fprintf(out, " *gxc_result=%s;\n", res(0))
		} else if n > 1 {
			fmt.Fprintf(out, " *gxc_result=gxc_list_out(GXC_LIST,%d);\n", n)
			for i := 0; i < n; i++ {
				fmt.Fprintf(out, " gxc_result->items[%d]=%s;\n", i, res(i))
			}
		}
		out.WriteString(" break;\n")
	}
	out.WriteString(` }
 gxc_keep=!gxc_fresh||gxc_inst->live>0;
 if(gxc_keep){
  gxc_save(gxc_inst);gx_sched->keep_native=true;gxc_left=0;
  for(gx_Task *x=gx_sched->all;x;x=x->next_all)if(x!=t&&!x->done)gxc_left++;
 }
}
/* Runs one entry; requires the serial lock and validated handles. */
static int gxc_run(int which,const gxc_value *args,size_t count,const gxc_options *opts,gxc_value *result,gxc_error *error){
 const gxc_options *user=opts;if(!gxc_streams(args,count,&opts)){error->kind=3;return 3;}
 gxc_args=args;gxc_opts=opts;gxc_active_options=opts;gxc_result=result;gxc_failure=error;gxc_export=which;
 gxc_output_bytes=0;gxc_output_nodes=0;gxc_output_depth=0;gxc_keep=false;gxc_left=0;
 uint8_t *report=NULL;size_t n=0;
 int rc=gx_run_library_host(gxc_init_source,gxc_entry,gxc_finish,gxc_poll,&report,&n);
 gxc_allocation_release(rc!=0);
 if(rc) {int kind=error->kind?error->kind:rc;memset(result,0,sizeof(*result));memset(error,0,sizeof(*error));error->kind=kind;error->message=(gxc_value){.kind=GXC_BYTES,.bytes=report,.length=n};}
 else free(report);
 gxc_user=NULL;
 if(!rc&&!gxc_fresh&&error->kind==1&&((user&&user->canceled&&atomic_load(user->canceled))||(gxc_self&&atomic_load(&gxc_self->cancel)))){gxc_error_free(error);memset(result,0,sizeof(*result));error->kind=6;}
 if(!rc&&gxc_keep&&gxc_left){char m[96];snprintf(m,sizeof m,"%zu goroutine(s) still running when a handle call returned were abandoned",gxc_left);gxc_warn(m);}
 gxc_context=gx_nil();gxc_args=NULL;gxc_opts=NULL;gxc_active_options=NULL;gxc_result=NULL;gxc_failure=NULL;
 return rc;
}
static int gxc_serial_lock(const gxc_options *opts,gxc_handle *self){
 while(pthread_mutex_trylock(&gxc_serial)) {
  if(opts && opts->canceled && atomic_load(opts->canceled)) return 6;
  if(self && atomic_load(&self->closed)) return 7;
  struct timespec pause={0,1000000};nanosleep(&pause,NULL);
 }
 return 0;
}
int goalchemy_invoke(const char *name,const gxc_value *args,size_t count,const gxc_options *opts,gxc_value *result,gxc_error *error) {
 if(!result || !error) return 5;
 memset(result,0,sizeof(*result)); memset(error,0,sizeof(*error));
 if(gxc_stream_thread) return error->kind=10;
 if(gxc_callback_thread) return error->kind=3;
 if(!name || count>256 || (count && !args)) return error->kind=5;
 int which=-1; size_t expected=0; const char *recv=NULL; const char *types[256]={0}; const char *streams[256]={0};
`)
	for k, en := range entries {
		if en.close {
			continue
		}
		n := 0
		var types []string
		if en.recv != nil {
			n++
			types = append(types, fmt.Sprintf("types[0]=%q;", en.recv.Name))
		}
		for _, t := range params(en) {
			if cContext(t) {
				continue
			}
			if h := abi.Handle(t); h != nil {
				types = append(types, fmt.Sprintf("types[%d]=%q;", n, h.Name))
			}
			if k := ir.StreamKind(t); k != "" {
				types = append(types, fmt.Sprintf("streams[%d]=%q;", n, k))
			}
			n++
		}
		r := "NULL"
		if en.recv != nil {
			r = fmt.Sprintf("%q", en.recv.Name)
		}
		fmt.Fprintf(out, " if(!strcmp(name,%q)) {which=%d;expected=%d;recv=%s;%s}\n", en.name, k, n, r, strings.Join(types, ""))
	}
	out.WriteString(` if(which<0 || count!=expected) return error->kind=5;
 gxc_handle *self=NULL;
 for(size_t i=0;i<count;i++) if(types[i]) {
  if(args[i].kind==GXC_NIL) {if(i==0&&recv)return error->kind=5;continue;}
  if(args[i].kind!=GXC_HANDLE||!args[i].handle||strcmp(args[i].handle->type,types[i])) return error->kind=5;
  if(!self) self=args[i].handle;
 } else if(streams[i]) {
  if(args[i].kind==GXC_NIL) continue;
  const gxc_stream *s=args[i].stream;
  if(args[i].kind!=GXC_STREAM||!s||(strstr(streams[i],"Read")&&!s->read)||(strstr(streams[i],"Write")&&!s->write)||(strstr(streams[i],"Seek")&&!s->seek)) return error->kind=5;
 } else if(args[i].kind==GXC_HANDLE||args[i].kind==GXC_STREAM) return error->kind=5;
 if(self&&atomic_load(&self->closed)) return error->kind=7;
 gxc_value *snapshot=calloc(count?count:1,sizeof(*snapshot)); if(!snapshot) return error->kind=3;
 int rc=0; for(size_t i=0;i<count;i++) {rc=gxc_value_copy(&snapshot[i],&args[i]);if(rc)break;}
 if(rc) {for(size_t i=0;i<count;i++) gxc_value_free(&snapshot[i]);free(snapshot);return error->kind=rc==3?3:5;}
 rc=gxc_serial_lock(opts,self);
 if(!rc) {
  gxc_instance *in=NULL;
  for(size_t i=0;i<count&&!rc;i++) if(types[i]&&snapshot[i].kind==GXC_HANDLE) {
   gxc_handle *h=snapshot[i].handle;
   if(h->released||atomic_load(&h->closed)) rc=7;
   else if(in&&h->inst!=in) rc=9;
   else in=h->inst;
  }
  if(!rc&&in&&in->poisoned) rc=8;
  if(!rc&&opts && opts->canceled && atomic_load(opts->canceled)) rc=6;
  if(!rc) {
   gxc_fresh=!in;
   if(!in){in=calloc(1,sizeof(*in));if(!in)rc=3;}
   if(!rc){
    gxc_inst=in;gxc_self=self;
    rc=gxc_run(which,snapshot,count,opts,result,error);
    if(!gxc_fresh&&(rc==2||rc==3||rc==4))in->poisoned=true;
    if(gxc_fresh&&(rc||!gxc_keep))free(in);
    gxc_inst=NULL;gxc_self=NULL;
   }
  }
  pthread_mutex_unlock(&gxc_serial);
 }
 for(size_t i=0;i<count;i++)gxc_value_free(&snapshot[i]);free(snapshot);
 if(rc && !error->kind)error->kind=rc;
 return error->kind;
}
int gxc_handle_close(gxc_handle *h,gxc_error *error){
 gxc_error ignored;if(!error)error=&ignored;
 memset(error,0,sizeof(*error));
 if(!h) return error->kind=5;
 if(atomic_exchange(&h->closed,true)) return 0;
 atomic_store(&h->cancel,true);
 pthread_mutex_lock(&gxc_serial);
 int rc=0;
 if(!h->released) {
  gxc_instance *in=h->inst;
  if(h->close_entry>=0&&!in->poisoned) {
   gxc_value recv={.kind=GXC_HANDLE,.handle=h},result={0};
   atomic_store(&h->cancel,false);
   gxc_fresh=false;gxc_inst=in;gxc_self=NULL;
   rc=gxc_run(h->close_entry,&recv,1,NULL,&result,error);
   if(rc==2||rc==3||rc==4)in->poisoned=true;
   gxc_inst=NULL;gxc_value_free(&result);
  }
  gxc_release(h);
 }
 pthread_mutex_unlock(&gxc_serial);
 if(rc&&!error->kind)error->kind=rc;
 return error->kind;
}
void gxc_handle_free(gxc_handle *h){
 if(!h)return;
 atomic_store(&h->closed,true);atomic_store(&h->cancel,true);
 pthread_mutex_lock(&gxc_serial);
 if(!h->released&&h->inst)gxc_release(h);
 pthread_mutex_unlock(&gxc_serial);
 free(h);
}
`)
	return []byte("/* Generated owned C value library API. */\n#include \"library.h\"\n"), nil
}
