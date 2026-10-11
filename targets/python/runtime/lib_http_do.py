"""Bounded dedicated HTTP/1 transport; socket/body close precedes completion ACK."""
import http.client
import ssl
import socket
import threading
import urllib.parse
import re
import zlib
import os
import shutil
import tempfile
from .task_spawn import sched, register_host, HostFault, fault_text
from .std_context_err import check_context, host_boundary, CONTEXT_DEADLINE_EXCEEDED
from .std_errors_new import std_errors_new
from .lib_crypto_close import byte_input, slice_bytes, slice_strings, Reject, MAX
from ..types.slice import Slice, NIL, BYTE_NIL

ZERO=[0,NIL,BYTE_NIL,None]
FORBIDDEN={'host','content-length','transfer-encoding','connection','proxy-authorization','proxy-connection','upgrade','trailer','te'}
class HeaderLimit(Exception): pass
class HeaderReader:
    def __init__(self,fp):self.fp=fp;self.count=0;self.headers=True
    def readline(self,limit=-1):
        line=self.fp.readline(min(65537-self.count,limit) if self.headers and limit>=0 else (65537-self.count if self.headers else limit))
        if self.headers:
            self.count+=len(line)
            if self.count>65536:raise HeaderLimit()
        return line
    def __getattr__(self,name):return getattr(self.fp,name)
class Response(http.client.HTTPResponse):
    def __init__(self,*args,**kwargs):
        super().__init__(*args,**kwargs);self.fp=HeaderReader(self.fp)
    def begin(self):
        super().begin()
        if self.fp is not None:self.fp.headers=False

class Gunzip:
    def __init__(self,limit):self.limit=limit;self.out=[];self.count=0;self.d=None;self.done=False
    def feed(self,data):
        while data:
            if self.d is None or self.done:self.d=zlib.decompressobj(16+zlib.MAX_WBITS);self.done=False
            data=self.inflate(data)
            if self.d.eof:self.done=True;data=self.d.unused_data
    def inflate(self,data):
        while True:
            chunk=self.d.decompress(data,self.limit+1-self.count)
            self.out.append(chunk);self.count+=len(chunk)
            if self.count>self.limit:raise Reject('http: response body exceeds limit')
            data=self.d.unconsumed_tail
            if not data or self.d.eof:return data
    def finish(self):
        if self.d is not None and not self.done:raise zlib.error('truncated gzip stream')
        return b''.join(self.out)

class Lease:
    def __init__(self):self.stopped=threading.Event();self.lock=threading.Lock();self.connection=None
    def stop(self):
        self.stopped.set()
        with self.lock:
            conn=self.connection
            if conn is not None and conn.sock is not None:
                try:conn.sock.shutdown(socket.SHUT_RDWR)
                except OSError:pass
    def release(self):
        with self.lock:
            conn=self.connection;self.connection=None
        if conn is not None:conn.close()

OPTION_NAMES={b'client-cert',b'client-key',b'root-cas',b'insecure-skip-verify'}
class BadTLS(Exception): pass
def tls_context(options):
    """Returns the SSL context for transport options, or None for defaults."""
    if not options:return None
    if len(options)%2:raise Reject('invalid')
    values={};total=0
    for i in range(0,len(options),2):
        name,value=options[i],options[i+1]
        if type(name) is not bytes or type(value) is not bytes:raise Reject('invalid')
        total+=len(name)+len(value)
        if total>1<<20 or name in values or name not in OPTION_NAMES:raise Reject('invalid')
        values[name]=value
    skip=values.get(b'insecure-skip-verify')
    if (b'client-cert' in values)!=(b'client-key' in values) or skip not in (None,b'true',b'false'):raise Reject('invalid')
    ctx=ssl.create_default_context()
    try:
        if b'root-cas' in values:ctx.load_verify_locations(cadata=values[b'root-cas'].decode('ascii'))
        if b'client-cert' in values:
            directory=tempfile.mkdtemp(prefix='goalchemy-tls-')
            try:
                paths=[]
                for name in (b'client-cert',b'client-key'):
                    path=os.path.join(directory,name.decode());paths.append(path)
                    with open(os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600),'wb') as f:f.write(values[name])
                ctx.load_cert_chain(paths[0],paths[1])
            finally:shutil.rmtree(directory,ignore_errors=True)
    except (ssl.SSLError,ValueError,UnicodeError) as e:raise BadTLS() from e
    if skip==b'true':ctx.check_hostname=False;ctx.verify_mode=ssl.CERT_NONE
    return ctx

def lib_http_do(t,context,method,raw,headers,input,max_bytes,timeout):
    http_do(t,context,method,raw,headers,input,max_bytes,timeout,[])

def http_do(t,context,method,raw,headers,input,max_bytes,timeout,options):
    invalid=b'http: invalid request or limit'
    s=sched();s.check()
    if context is None:
        t.rv=[0,NIL,BYTE_NIL,std_errors_new(invalid)];return
    try:check_context(context)
    except HostFault:
        t.rv=[0,NIL,BYTE_NIL,std_errors_new(invalid)];return
    if context.err is not None:t.rv=[0,NIL,BYTE_NIL,context.err];return
    lease=Lease();boundary=None
    try:
        if method not in (b'GET',b'POST') or type(max_bytes) is not int or not 0<=max_bytes<=MAX or type(timeout) is not int or not 1<=timeout<=300000:raise Reject('invalid')
        boundary=host_boundary(context,timeout*1000000)
        body=byte_input(input)
        if method==b'GET' and body:raise Reject('invalid')
        if type(raw) is not bytes or len(raw)>8192:raise Reject('invalid')
        url=raw.decode('utf-8','strict')
        if any(ord(c)<=32 or ord(c)==127 or c=='\ufeff' for c in url) or '\\' in url:raise Reject('invalid')
        u=urllib.parse.urlsplit(url)
        if u.scheme not in ('http','https') or not u.hostname or u.username is not None or u.password is not None or u.fragment or u.port is not None and not 1<=u.port<=65535:raise Reject('invalid')
        if type(headers) is not Slice or headers.l%2:raise Reject('invalid')
        pairs=[];total=0;automatic=True;seen=set()
        for i in range(0,headers.l,2):
            name=headers.a[headers.o+i];value=headers.a[headers.o+i+1]
            if type(name) is not bytes or type(value) is not bytes:raise Reject('invalid')
            total+=len(name)+len(value)+4
            if total>65536 or not re.fullmatch(rb"[!#$%&'*+.^_`|~0-9A-Za-z-]+",name) or name.decode('ascii').lower() in FORBIDDEN or any(c==127 or c<32 and c!=9 for c in value):raise Reject('invalid')
            lower=name.decode('ascii').lower()
            if lower in ('accept-encoding','range') and lower not in seen:
                seen.add(lower)
                if value:automatic=False
            pairs.append((name.decode('ascii'),value.decode('latin-1')))
        path=urllib.parse.quote(u.path or '/',safe="/%:@!$&'()*+,;=-._~")
        if u.query:path+='?'+urllib.parse.quote(u.query,safe="/%?:@!$&'()*+,;=-._~")
        hostname=u.hostname.encode('idna').decode('ascii');port=u.port
        tls=tls_context(options)
    except BadTLS:
        if boundary is not None:boundary.detach()
        t.rv=[0,NIL,BYTE_NIL,std_errors_new(b'http: invalid TLS option')];return
    except (Reject,ValueError,UnicodeError):
        if boundary is not None:boundary.detach()
        t.rv=[0,NIL,BYTE_NIL,std_errors_new(invalid)];return
    check_context(boundary)
    if boundary.err is not None:
        err=boundary.err;boundary.detach();t.rv=[0,NIL,BYTE_NIL,err];return
    def decode(w):
        if w[0] is not None:return [0,NIL,BYTE_NIL,std_errors_new(w[0])]
        return [w[1],slice_strings(w[2]),slice_bytes(w[3]),None]
    token=register_host(t,boundary,lease.stop,boundary.detach,decode,lambda err:[0,NIL,BYTE_NIL,err])
    def work():
        nonlocal body,pairs
        response=None;record=None;failure=None
        try:
            # Acquisition errors are implementation faults, separately from I/O.
            try:
                conn=http.client.HTTPSConnection(hostname,port,timeout=timeout/1000,context=tls or ssl.create_default_context()) if u.scheme=='https' else http.client.HTTPConnection(hostname,port,timeout=timeout/1000)
                conn.response_class=Response
                with lease.lock:lease.connection=conn
            except BaseException as e:raise HostFault('HTTP acquisition') from e
            if lease.stopped.is_set():raise OSError('canceled')
            conn.connect()
            if lease.stopped.is_set():raise OSError('canceled')
            conn.putrequest(method.decode(),path,skip_accept_encoding=True)
            for name,value in pairs:
                if automatic and name.lower()=='accept-encoding':continue
                conn.putheader(name,value)
            if automatic:conn.putheader('Accept-Encoding','gzip')
            if method==b'POST':conn.putheader('Content-Length',str(len(body)))
            conn.putheader('Connection','close');conn.endheaders(body if body else None)
            response=conn.getresponse()
            encoding=response.getheader('Content-Encoding')
            bodyless=response.status in (204,304) or 100<=response.status<200 or response.length==0
            decoded=automatic and encoding is not None and encoding.lower()=='gzip' and not bodyless
            gunzip=Gunzip(max_bytes) if decoded else None
            chunks=[];count=0
            while count <= max_bytes:
                chunk=response.read(8192 if gunzip else min(8192,max_bytes+1-count))
                if not chunk:
                    if response.length is not None and response.length>0:raise http.client.IncompleteRead(b'')
                    break
                if gunzip:gunzip.feed(chunk)
                else:chunks.append(chunk);count+=len(chunk)
            out=gunzip.finish() if gunzip else b''.join(chunks);chunks.clear()
            if len(out)>max_bytes:raise Reject('http: response body exceeds limit')
            mapped={}
            for name,value in response.getheaders():
                canon='-'.join(p[:1].upper()+p[1:].lower() for p in name.split('-'))
                if decoded and canon in ('Content-Encoding','Content-Length'):continue
                mapped.setdefault(canon,[]).append(value)
            flat=[]
            for name in sorted(mapped):
                for value in mapped[name]:flat.extend([name.encode('ascii'),value.encode('latin-1')])
            record=[None,response.status,flat,out]
        except Reject as e:record=[str(e).encode(),0,[],b'']
        except HeaderLimit:record=[b'http: response headers exceed limit',0,[],b'']
        except (OSError,http.client.HTTPException,UnicodeError,ValueError,zlib.error):record=[b'http: transport failure',0,[],b'']
        except BaseException as e:failure='HTTP adapter: '+fault_text(e)
        finally:
            try:
                if response is not None:response.close()
            except BaseException as e:failure='HTTP body cleanup: '+fault_text(e)
            try:lease.release()
            except BaseException as e:failure='HTTP connection cleanup: '+fault_text(e)
            body=None;pairs.clear()
        token.publish(record,fault=failure)
        token.acknowledge_cleanup()
    try:threading.Thread(target=work,name='goalchemy-http',daemon=True).start()
    except BaseException as e:
        try:lease.release()
        except BaseException as cleanup:e=cleanup
        token.publish(fault='HTTP submission: '+fault_text(e));token.acknowledge_cleanup()
