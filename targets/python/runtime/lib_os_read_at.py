import os
from .lib_os_open import MAX_CHUNK, lookup
from .lib_os_read import chunk
from .lib_os_read_file import os_status
from ..types.slice import BYTE_NIL

def lib_os_read_at(file,offset,max_bytes):
    fd=lookup(file)
    if fd is None or not 1<=max_bytes<=MAX_CHUNK or offset<0:return BYTE_NIL,False,8
    try:
        if hasattr(os,'pread'):data=os.pread(fd,max_bytes,offset)
        else:
            pos=os.lseek(fd,0,os.SEEK_CUR)
            try:
                os.lseek(fd,offset,os.SEEK_SET)
                data=os.read(fd,max_bytes)
            finally:os.lseek(fd,pos,os.SEEK_SET)
    except OSError as e:return BYTE_NIL,False,os_status(e)
    return chunk(data),len(data)==0,0
