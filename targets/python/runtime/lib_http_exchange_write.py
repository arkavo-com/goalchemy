from .lib_http_exchange_close import exchange_op, EXCHANGE_CLOSED, EXCHANGE_STATE, INVALID
from .lib_crypto_close import byte_input, Reject
from .std_errors_new import std_errors_new

def lib_http_exchange_write(t,ex,data):
    try:chunk=bytes(byte_input(data,1<<20))
    except Reject:chunk=None
    if ex is None or chunk is None:t.rv=[std_errors_new(INVALID)];return
    if ex.closed:t.rv=[std_errors_new(EXCHANGE_CLOSED)];return
    if ex.responded or ex.length==0 and chunk or ex.length>=0 and len(chunk)>ex.length-ex.written:t.rv=[std_errors_new(EXCHANGE_STATE)];return
    ex.written+=len(chunk)
    if not chunk:t.rv=[None];return
    def work(ex):
        ex.start()
        ex.conn.send(b'%X\r\n'%len(chunk)+chunk+b'\r\n' if ex.length<0 else chunk)
    exchange_op(t,ex,work,lambda _:[None],lambda err:[err])
