import os
from .lib_os_open import MAX_CHUNK, lookup
from .lib_os_read_file import os_status
from ..types.slice import Slice, BYTE_NIL

def chunk(data):
    return Slice(bytearray(data),0,len(data),len(data),True)

def lib_os_read(file,max_bytes):
    fd=lookup(file)
    if fd is None or not 1<=max_bytes<=MAX_CHUNK:return BYTE_NIL,False,8
    try:data=os.read(fd,max_bytes)
    except OSError as e:return BYTE_NIL,False,os_status(e)
    return chunk(data),len(data)==0,0
