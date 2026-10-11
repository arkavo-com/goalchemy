#define _POSIX_C_SOURCE 200809L
#include "library.h"
#include <stdlib.h>
#include <string.h>
#include <limits.h>
void gxc_value_free(gxc_value *v) {
    if (!v) return;
    if (v->items) for (size_t i=0;i<v->length;i++) gxc_value_free(&v->items[i]);
    if (v->names) for (size_t i=0;i<v->length;i++) free(v->names[i]);
    free(v->names); free(v->items); free(v->bytes); memset(v,0,sizeof(*v));
}
void gxc_error_free(gxc_error *e) {
    if (!e) return;
    gxc_value_free(&e->message); gxc_value_free(&e->fields); e->kind=0;
}
static int copy_value(gxc_value *d,const gxc_value *s,unsigned depth,size_t *budget,size_t *bytes) {
    if (!s || depth>64 || !*budget || s->length>UINT32_MAX) return 1;
    (*budget)--;
    if (s->kind<GXC_NIL || s->kind>GXC_STREAM) return 1;
    d->kind=s->kind; d->integer=s->integer; d->floating=s->floating; d->length=s->length;
    if (s->kind==GXC_BYTES) {
        if (s->length>*bytes || s->length>128u*1024u*1024u || (s->length && !s->bytes)) return 1;
        *bytes-=s->length;
        d->bytes=malloc(s->length?s->length:1); if (!d->bytes) return 3;
        if (s->length) memcpy(d->bytes,s->bytes,s->length);
    } else if (s->kind==GXC_LIST || s->kind==GXC_RECORD) {
        if (s->length>*budget || s->length>SIZE_MAX/sizeof(*d->items) || (s->length && !s->items)) return 1;
        d->items=calloc(s->length?s->length:1,sizeof(*d->items)); if (!d->items) return 3;
        if (s->kind==GXC_RECORD) {
            if (s->length && !s->names) return 1;
            d->names=calloc(s->length?s->length:1,sizeof(*d->names)); if (!d->names) return 3;
        }
        for (size_t i=0;i<s->length;i++) {
            if (s->kind==GXC_RECORD) {
                if (!s->names[i]) return 1;
                size_t n=strnlen(s->names[i],257); if(n>256) return 1;
                for(size_t j=0;j<i;j++) if(!strcmp(s->names[i],s->names[j])) return 1;
                d->names[i]=malloc(n+1); if(!d->names[i]) return 3; memcpy(d->names[i],s->names[i],n+1);
            }
            int rc=copy_value(&d->items[i],&s->items[i],depth+1,budget,bytes); if(rc) return rc;
        }
    } else if (s->kind==GXC_HANDLE) {
        if (s->length || s->bytes || s->items || s->names || s->stream) return 1;
        d->handle=s->handle; if(!d->handle) d->kind=GXC_NIL;
    } else if (s->kind==GXC_STREAM) {
        if (s->length || s->bytes || s->items || s->names || s->handle) return 1;
        d->stream=s->stream; if(!d->stream) d->kind=GXC_NIL;
    } else if (s->length || s->bytes || s->items || s->names || s->handle || s->stream) return 1;
    return 0;
}
int gxc_value_copy(gxc_value *d,const gxc_value *s) {
    if(!d) return 1;
    memset(d,0,sizeof(*d)); size_t budget=1000000,bytes=256u*1024u*1024u;
    int rc=copy_value(d,s,0,&budget,&bytes); if(rc) gxc_value_free(d); return rc;
}
const gxc_value *gxc_field(const gxc_value *v,const char *name) {
    static const gxc_value nil={0};
    if(!v || v->kind!=GXC_RECORD) return &nil;
    for(size_t i=0;i<v->length;i++) if(!strcmp(v->names[i],name)) return &v->items[i];
    return &nil;
}

const gxc_options *gxc_active_options;

_Thread_local bool gxc_callback_thread;
_Thread_local bool gxc_stream_thread;
