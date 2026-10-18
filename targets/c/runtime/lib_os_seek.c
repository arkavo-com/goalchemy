#include "gx.h"
#include <errno.h>
#include <unistd.h>
gx_V gx_lib_os_seek(gx_V file, gx_V offset, gx_V whence) {
    int fd = gx_os_fd(file);
    int64_t w = gx_i(whence);
    gx_V out[2] = {gx_int(0), gx_int(0)};
    if (fd < 0 || w < 0 || w > 2) { out[1] = gx_int(8); return gx_tuple(2, out); }
    off_t pos = lseek(fd, (off_t)gx_i(offset), w == 0 ? SEEK_SET : w == 1 ? SEEK_CUR : SEEK_END);
    if (pos < 0) { out[1] = gx_int(gx_os_status(errno)); return gx_tuple(2, out); }
    out[0] = gx_int((int64_t)pos);
    return gx_tuple(2, out);
}
