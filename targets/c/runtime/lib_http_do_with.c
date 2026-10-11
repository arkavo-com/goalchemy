#include "native.h"
void gx_lib_http_do_with(gx_Task *t,gx_V ctx,gx_V method,gx_V url,gx_V headers,gx_V input,gx_V max,gx_V timeout,gx_V options) {
 gx_http_do(t,ctx,method,url,headers,input,max,timeout,options);
}
