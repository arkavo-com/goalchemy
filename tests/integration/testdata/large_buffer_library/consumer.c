#include "goalchemy.h"
#include <assert.h>
#include <stdlib.h>
#include <stdio.h>

int main(void) {
    size_t n = 257u << 20;
    gxc_value input = {.kind=GXC_BYTES, .length=n, .bytes=calloc(n,1)}, output = {0};
    gxc_error error = {0};
    assert(input.bytes);
    input.bytes[0]=7; input.bytes[n-1]=19;
    assert(goalchemy_invoke("Echo",&input,1,NULL,&output,&error)==0);
    assert(output.kind==GXC_BYTES && output.length==n && output.bytes[0]==8 && output.bytes[n-1]==20);
    assert(input.bytes[0]==7 && input.bytes[n-1]==19);
    gxc_value_free(&output);
    input.length=90u<<20;
    gxc_value items[3]={input,input,input}, list={.kind=GXC_LIST,.length=3,.items=items};
    assert(gxc_value_copy(&output,&list)==0);
    for(size_t i=0;i<3;i++)assert(output.items[i].length==input.length && output.items[i].bytes[0]==7);
    gxc_value_free(&output);
    free(input.bytes);
    puts("PASS large buffer library");
}
