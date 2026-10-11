from .lib_http_do import http_do
from ..types.slice import Slice

def lib_http_do_with(t,context,method,raw,headers,input,max_bytes,timeout,options):
    values=([options.a[options.o+i] for i in range(options.l)] if options.a is not None else []) if type(options) is Slice else [None]
    http_do(t,context,method,raw,headers,input,max_bytes,timeout,values)
