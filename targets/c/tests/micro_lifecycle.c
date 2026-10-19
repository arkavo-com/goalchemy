#include "gx.h"
#include <assert.h>
#include <stdio.h>
#include <string.h>
static _Alignas(max_align_t) unsigned char arena[65536];
static unsigned char guard[64];
static char output[64];
static size_t written;
static void sink(void *state, const void *bytes, size_t n) {
    (void)state;
    assert(goalchemy_micro_run(arena,sizeof arena,NULL,NULL,NULL)==GX_MICRO_BUSY);
    assert(written+n<sizeof output);
    memcpy(output+written,bytes,n); written+=n;
}
static void empty(void) {}
static void overflow(void) { gx_micro_alloc(SIZE_MAX); }
static void depth(void) { for (int n=0;n<=GX_MICRO_MAX_DEPTH;n++) gx_source_enter(); }
static void panicking(void) { gx_throw(gx_boxv(&GX_STRING_TYPE, gx_cstr("boom"))); }
int main(void) {
    gx_micro_stats stats = {0}; size_t peak=0;
    memset(guard,0xa5,sizeof guard);
    for (int n=0;n<100;n++) {
        written=0; memset(output,0,sizeof output);
        assert(goalchemy_micro_run(arena,sizeof arena,sink,NULL,&stats)==GX_MICRO_OK);
        assert(!strcmp(output,"1 7\n")); /* globals + arena start fresh */
        assert(!stats.fault && stats.arena_peak_bytes <= sizeof arena);
        if (n) assert(peak==stats.arena_peak_bytes); peak=stats.arena_peak_bytes;
        assert(goalchemy_micro_run(arena,1,NULL,NULL,&stats)==GX_MICRO_FAULT);
        assert(!strcmp(stats.fault,"arena exhausted"));
    }
    assert(goalchemy_micro_run(arena+1,sizeof arena-1,NULL,NULL,&stats)==GX_MICRO_INVALID);
    assert(goalchemy_micro_run(NULL,1,NULL,NULL,&stats)==GX_MICRO_INVALID);
    assert(gx_micro_run(empty,overflow,arena,sizeof arena,NULL,NULL,&stats)==GX_MICRO_FAULT);
    assert(gx_micro_run(empty,depth,arena,sizeof arena,NULL,NULL,&stats)==GX_MICRO_FAULT);
    assert(!strcmp(stats.fault,"source call depth exceeded"));
    assert(gx_micro_run(empty,panicking,arena,sizeof arena,NULL,NULL,&stats)==GX_MICRO_PANIC);
    assert(goalchemy_micro_run(arena,sizeof arena,NULL,NULL,&stats)==GX_MICRO_OK);
    for (size_t i=0;i<sizeof guard;i++) assert(guard[i]==0xa5);
    puts("micro lifecycle passed");
}
