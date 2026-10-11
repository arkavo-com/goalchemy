import http.client
from .lib_http_exchange_close import exchange_op, EXCHANGE_CLOSED, EXCHANGE_STATE, INVALID, Reject_
from .lib_http_do import Response, HeaderLimit, flat_headers
from .lib_crypto_close import slice_strings
from .std_errors_new import std_errors_new
from ..types.slice import NIL

def lib_http_exchange_response(t,ex):
    zero=lambda err:[0,NIL,err]
    if ex is None:t.rv=zero(std_errors_new(INVALID));return
    if ex.closed:t.rv=zero(std_errors_new(EXCHANGE_CLOSED));return
    first=not ex.responded;ex.responded=True
    if ex.length>=0 and ex.written!=ex.length:ex.stop();t.rv=zero(std_errors_new(EXCHANGE_STATE));return
    def work(ex):
        if first:
            ex.start()
            if ex.length<0:ex.conn.send(b'0\r\n\r\n')
            try:ex.response=ex.conn.getresponse()
            except HeaderLimit:raise Reject_(b'http: response headers exceed limit')
        if ex.response is None:raise http.client.HTTPException('no response')
        close=[v for k,v in ex.response.getheaders() if k.lower()=='connection' and 'close' in [p.strip().lower() for p in v.split(',')]]
        skip=('Transfer-Encoding','Connection') if close else ('Transfer-Encoding',)
        return [ex.response.status,flat_headers(ex.response,skip)]
    exchange_op(t,ex,work,lambda w:[w[0],slice_strings(w[1]),None],zero)
