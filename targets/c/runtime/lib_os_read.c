#include "gx.h"
#include <errno.h>
#include <unistd.h>
gx_V gx_os_chunk(ssize_t r, uint8_t *data, int64_t status) {
    gx_V out[3];
    if (status) { out[0] = gx_nil_byte_slice(); out[1] = gx_bool(false); out[2] = gx_int(status); }
    else { out[0] = gx_byte_slice(data, (uint32_t)r, (uint32_t)r); out[1] = gx_bool(r == 0); out[2] = gx_int(0); }
    return gx_tuple(3, out);
}
gx_V gx_lib_os_read(gx_V file, gx_V max) {
    int fd = gx_os_fd(file);
    int64_t n = gx_i(max);
    if (fd < 0 || n < 1 || n > (1 << 20)) return gx_os_chunk(0, NULL, 8);
    uint8_t *data = gx_alloc_bytes((size_t)n);
    ssize_t r;
    do r = read(fd, data, (size_t)n); while (r < 0 && errno == EINTR);
    if (r < 0) return gx_os_chunk(0, NULL, gx_os_status(errno));
    return gx_os_chunk(r, data, 0);
}
