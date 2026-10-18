#include "gx.h"
#include <errno.h>
#include <stdlib.h>
#include <sys/stat.h>
gx_V gx_lib_os_stat(gx_V name) {
    int64_t status = 0;
    gx_V out[4] = {gx_int(0), gx_int(0), gx_bool(false), gx_int(0)};
    char *path = gx_os_path(name, &status);
    if (!path) { out[3] = gx_int(status); return gx_tuple(4, out); }
    struct stat st;
    int r = stat(path, &st);
    free(path);
    if (r != 0) { out[3] = gx_int(gx_os_status(errno)); return gx_tuple(4, out); }
    out[0] = gx_int((int64_t)st.st_size); out[1] = gx_int((int64_t)(st.st_mode & 0777)); out[2] = gx_bool(S_ISDIR(st.st_mode));
    return gx_tuple(4, out);
}
