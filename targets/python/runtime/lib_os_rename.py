import os
from .lib_os_read_file import os_path, os_status

def lib_os_rename(oldpath,newpath):
    for name in (oldpath,newpath):
        bad=os_path(name)
        if bad is not None:return bad
    try:os.replace(oldpath,newpath)
    except OSError as e:return os_status(e)
    return 0
