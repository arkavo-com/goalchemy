import os
from .lib_os_open import lookup
from .lib_os_read_file import os_status

def lib_os_seek(file,offset,whence):
    fd=lookup(file)
    if fd is None or not 0<=whence<=2:return 0,8
    try:return os.lseek(fd,offset,whence),0
    except OSError as e:return 0,os_status(e)
