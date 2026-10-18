#include "gx.h"
#include <errno.h>
#include <unistd.h>
gx_V gx_lib_os_read_at(gx_V file, gx_V offset, gx_V max) {
    int fd = gx_os_fd(file);
    int64_t n = gx_i(max), off = gx_i(offset);
    if (fd < 0 || n < 1 || n > (1 << 20) || off < 0) return gx_os_chunk(0, NULL, 8);
    uint8_t *data = gx_alloc_bytes((size_t)n);
    ssize_t r;
    do r = pread(fd, data, (size_t)n, (off_t)off); while (r < 0 && errno == EINTR);
    if (r < 0) return gx_os_chunk(0, NULL, gx_os_status(errno));
    return gx_os_chunk(r, data, 0);
}
