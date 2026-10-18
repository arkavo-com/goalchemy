#include "gx.h"
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
gx_V gx_lib_os_rename(gx_V oldpath, gx_V newpath) {
    int64_t status = 0;
    char *from = gx_os_path(oldpath, &status);
    if (!from) return gx_int(status);
    char *to = gx_os_path(newpath, &status);
    if (!to) { free(from); return gx_int(status); }
    int r = rename(from, to);
    int e = errno;
    free(from); free(to);
    return gx_int(r == 0 ? 0 : gx_os_status(e));
}
