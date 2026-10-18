import os
from .lib_os_open import forget
from .lib_os_read_file import os_status

def lib_os_close(file):
    fd=forget(file)
    if fd is None:return 8
    try:os.close(fd)
    except OSError as e:return os_status(e)
    return 0
