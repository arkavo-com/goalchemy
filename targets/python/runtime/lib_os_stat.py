import os
import stat
from .lib_os_read_file import os_path, os_status

def lib_os_stat(name):
    bad=os_path(name)
    if bad is not None:return 0,0,False,bad
    try:st=os.stat(name)
    except OSError as e:return 0,0,False,os_status(e)
    return st.st_size,st.st_mode&0o777,stat.S_ISDIR(st.st_mode),0
