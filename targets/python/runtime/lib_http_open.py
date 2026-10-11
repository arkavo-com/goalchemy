import http.client
import socket
import ssl
from .task_spawn import sched, HostFault
from .std_context_err import check_context, host_boundary
from .std_errors_new import std_errors_new
from .lib_crypto_close import Reject
from .lib_http_do import validate_request, tls_context, BadTLS, Response, unix_connector
from .lib_http_exchange_close import Exchange, INVALID
from ..types.slice import Slice

MAX_EXCHANGE_MILLIS=24*60*60*1000

def lib_http_open(t,context,method,raw,headers,length,timeout,options):
    s=sched();s.check()
    if context is None:t.rv=[None,std_errors_new(INVALID)];return
    try:check_context(context)
    except HostFault:t.rv=[None,std_errors_new(INVALID)];return
    if context.err is not None:t.rv=[None,context.err];return
    try:
        if method not in (b'GET',b'POST') or type(length) is not int or length< -1 or method==b'GET' and length!=0 or type(timeout) is not int or not 1<=timeout<=MAX_EXCHANGE_MILLIS:raise Reject('invalid')
        u,hostname,port,path,pairs,_=validate_request(raw,headers)
        if type(options) is not Slice:raise Reject('invalid')
        tls,unix=tls_context([options.a[options.o+i] for i in range(options.l)] if options.a is not None else [])
        if unix is not None and not hasattr(socket,'AF_UNIX'):raise Reject('invalid')
    except BadTLS:t.rv=[None,std_errors_new(b'http: invalid TLS option')];return
    except (Reject,ValueError,UnicodeError):t.rv=[None,std_errors_new(INVALID)];return
    seconds=timeout/1000
    def connect():
        conn=http.client.HTTPSConnection(hostname,port,timeout=seconds,context=tls or ssl.create_default_context()) if u.scheme=='https' else http.client.HTTPConnection(hostname,port,timeout=seconds)
        conn.response_class=Response
        if unix is not None:conn.connect=unix_connector(conn,unix)
        return conn
    boundary=host_boundary(context,timeout*1000000)
    t.rv=[Exchange(s,boundary,connect,method.decode(),path,pairs,length),None]
