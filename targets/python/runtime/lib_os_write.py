import os
from .lib_os_open import MAX_CHUNK, lookup
from .lib_os_read_file import os_status

def lib_os_write(file,data):
    fd=lookup(file)
    if fd is None or data.l>MAX_CHUNK:return 0,8
    body=b'' if data.a is None else bytes(data.a[data.o:data.o+data.l])
    view=memoryview(body)
    done=0
    try:
        while done<len(body):
            n=os.write(fd,view[done:])
            if n<=0:return done,9
            done+=n
    except OSError as e:return done,os_status(e)
    return done,0
