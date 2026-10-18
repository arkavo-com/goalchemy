import errno
import os
from .lib_os_read_file import os_path, os_status

def lib_os_remove(name):
    bad=os_path(name)
    if bad is not None:return bad
    try:os.unlink(name)
    except OSError as e:
        try:os.rmdir(name)
        except OSError as e2:return os_status(e if e2.errno==errno.ENOTDIR else e2)
    return 0
