#include "gx.h"
#include <errno.h>
#include <stdlib.h>
#include <unistd.h>
gx_V gx_lib_os_remove(gx_V name) {
    int64_t status = 0;
    char *path = gx_os_path(name, &status);
    if (!path) return gx_int(status);
    if (unlink(path) == 0) { free(path); return gx_int(0); }
    int e = errno;
    if (rmdir(path) == 0) { free(path); return gx_int(0); }
    if (errno != ENOTDIR) e = errno;
    free(path);
    return gx_int(gx_os_status(e));
}
