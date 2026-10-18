import os
from .lib_os_read_file import os_path, os_status

MAX_CHUNK=1<<20
FLAGS=((1,os.O_WRONLY),(2,os.O_RDWR),(4,os.O_APPEND),(8,os.O_CREAT),(16,os.O_EXCL),(32,os.O_TRUNC))
_files={}
_last=0

def register(fd):
    global _last
    _last+=1
    _files[_last]=fd
    return _last

def lookup(h):
    return _files.get(h)

def forget(h):
    return _files.pop(h,None)

def lib_os_open(name,flag,perm):
    bad=os_path(name)
    if bad is not None:return 0,bad
    if flag&~63 or flag&3==3:return 0,8
    flags=getattr(os,'O_CLOEXEC',0)
    for bit,value in FLAGS:
        if flag&bit:flags|=value
    try:fd=os.open(name,flags,perm&0o777)
    except OSError as e:return 0,os_status(e)
    return register(fd),0
