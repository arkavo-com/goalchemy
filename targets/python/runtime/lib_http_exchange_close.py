"""Streamed lib/http exchanges on http.client; one worker thread per operation."""
import http.client
import socket
import threading
from .task_spawn import sched, register_host, HostFault, fault_text
from .std_errors_new import std_errors_new

EXCHANGE_CLOSED=b'http: exchange closed'
EXCHANGE_STATE=b'http: invalid exchange operation'
INVALID=b'http: invalid request or limit'
FAILED=b'http: transport failure'

class Exchange:
    """Opened by lib.http.open; the boundary context carries the source
    cancellation and the whole-exchange deadline."""
    def __init__(self,owner,boundary,connect,method,path,pairs,length):
        self.owner,self.boundary,self.connect=owner,boundary,connect
        self.method,self.path,self.pairs,self.length=method,path,pairs,length
        self.lock=threading.Lock();self.conn=None;self.response=None;self.started=False
        self.closed=False;self.responded=False;self.written=0;self.stopped=False
        self.detach_hook=boundary._add_hook(self.stop)[1]
        owner.resources.add(self.release)
    def stop(self):
        """Nonblocking abort: unblocks any socket operation in progress."""
        with self.lock:
            self.stopped=True;conn=self.conn
        if conn is not None and conn.sock is not None:
            try:conn.sock.shutdown(socket.SHUT_RDWR)
            except OSError:pass
    def release(self):
        if self.closed:return
        self.closed=True;self.stop()
        self.owner.resources.discard(self.release)
        try:self.detach_hook()
        finally:self.boundary.detach()
        threading.Thread(target=self.disconnect,name='goalchemy-http-close',daemon=True).start()
    def disconnect(self):
        with self.lock:conn,self.conn=self.conn,None
        try:
            if self.response is not None:self.response.close()
        except BaseException:pass
        try:
            if conn is not None:conn.close()
        except BaseException:pass
    def start(self):
        """Connects and sends the request head; runs on a worker."""
        if self.started:return
        self.started=True
        conn=self.connect()
        with self.lock:
            self.conn=conn
            if self.stopped:raise OSError('stopped')
        conn.connect()
        if self.stopped:raise OSError('stopped')
        conn.putrequest(self.method,self.path,skip_accept_encoding=True)
        for name,value in self.pairs:conn.putheader(name,value)
        if self.length>0 or self.length==0 and self.method=='POST':conn.putheader('Content-Length',str(self.length))
        elif self.length<0:conn.putheader('Transfer-Encoding','chunked')
        conn.putheader('Connection','close');conn.endheaders()

def exchange_op(t,ex,work,decode,zero):
    """Runs work(ex) on a thread; it returns a wire value or raises. Errors
    become the boundary's context error once the exchange is aborted."""
    s=sched();s.check()
    if ex.owner is not s:raise HostFault('HTTP exchange belongs to another owner')
    def wire_decode(w):
        if w[0] is not None:return zero(std_errors_new(w[0]))
        return decode(w[1])
    token=register_host(t,ex.boundary,ex.stop,lambda:None,wire_decode,zero)
    def run():
        record=None;failure=None
        try:record=[None,work(ex)]
        except Reject_ as e:record=[e.message,None]
        except (OSError,http.client.HTTPException,ValueError) as e:record=[FAILED,None]
        except BaseException as e:failure='HTTP exchange adapter: '+fault_text(e)
        token.publish(record,fault=failure);token.acknowledge_cleanup()
    try:threading.Thread(target=run,name='goalchemy-http',daemon=True).start()
    except BaseException as e:
        token.publish(fault='HTTP submission: '+fault_text(e));token.acknowledge_cleanup()

class Reject_(Exception):
    def __init__(self,message):super().__init__(message);self.message=message

def lib_http_exchange_close(t,ex):
    if ex is not None:ex.release()
    t.rv=[]
