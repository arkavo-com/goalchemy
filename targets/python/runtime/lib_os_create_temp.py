import os
import tempfile
from .lib_os_open import register
from .lib_os_read_file import os_status

def relative(directory,name):
    return os.path.join(directory,os.path.basename(name)) if directory and isinstance(name,bytes) else name

def lib_os_create_temp(directory,prefix,suffix):
    if b'\0' in directory+prefix+suffix or b'/' in prefix+suffix:return 0,b'',8
    try:fd,name=tempfile.mkstemp(suffix=suffix,prefix=prefix,dir=directory or None)
    except OSError as e:
        attempted=relative(directory,e.filename) if isinstance(e.filename,bytes) else b''
        return 0,attempted,os_status(e)
    return register(fd),relative(directory,name),0
