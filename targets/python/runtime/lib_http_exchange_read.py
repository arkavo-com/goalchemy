import http.client
from .lib_http_exchange_close import exchange_op, EXCHANGE_CLOSED, EXCHANGE_STATE, INVALID
from .lib_crypto_close import slice_bytes
from .std_errors_new import std_errors_new
from ..types.slice import BYTE_NIL

def lib_http_exchange_read(t,ex,max_bytes):
    zero=lambda err:[BYTE_NIL,False,err]
    if ex is None or type(max_bytes) is not int or not 1<=max_bytes<=1<<20:t.rv=zero(std_errors_new(INVALID));return
    if ex.closed:t.rv=zero(std_errors_new(EXCHANGE_CLOSED));return
    if not ex.responded:t.rv=zero(std_errors_new(EXCHANGE_STATE));return
    def work(ex):
        r=ex.response
        if r is None:raise http.client.HTTPException('no response')
        chunk=r.read1(max_bytes)
        if not chunk and not r.chunked and r.length:raise http.client.IncompleteRead(b'')
        return chunk
    exchange_op(t,ex,work,lambda chunk:[slice_bytes(chunk),False,None] if chunk else [BYTE_NIL,True,None],zero)
