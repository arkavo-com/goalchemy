import os
from .lib_os_open import lookup
from .lib_os_read_file import os_status

def lib_os_sync(file):
    fd=lookup(file)
    if fd is None:return 8
    try:os.fsync(fd)
    except OSError as e:return os_status(e)
    return 0
