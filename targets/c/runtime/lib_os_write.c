#include "gx.h"
#include <errno.h>
#include <stdlib.h>
#include <unistd.h>
gx_V gx_lib_os_write(gx_V file, gx_V data) {
    int fd = gx_os_fd(file);
    gx_V out[2] = {gx_int(0), gx_int(0)};
    if (fd < 0 || data.l > (1u << 20)) { out[1] = gx_int(8); return gx_tuple(2, out); }
    uint8_t *body = NULL;
    const uint8_t *p;
    if (gx_byte_backing(data) || data.l == 0) p = data.l ? gx_bytes(data) : (const uint8_t *)"";
    else {
        body = malloc(data.l);
        if (!body) gx_fault("out of memory");
        for (uint32_t i = 0; i < data.l; i++) body[i] = (uint8_t)gx_i(gx_vals(data)[i]);
        p = body;
    }
    size_t done = 0;
    int64_t status = 0;
    while (done < data.l) {
        ssize_t w = write(fd, p + done, data.l - done);
        if (w < 0) { if (errno == EINTR) continue; status = gx_os_status(errno); break; }
        if (w == 0) { status = 9; break; }
        done += (size_t)w;
    }
    free(body);
    out[0] = gx_int((int64_t)done); out[1] = gx_int(status);
    return gx_tuple(2, out);
}
