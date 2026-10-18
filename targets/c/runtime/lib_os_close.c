#include "gx.h"
#include <errno.h>
#include <unistd.h>
gx_V gx_lib_os_close(gx_V file) {
    int fd = gx_os_forget(file);
    if (fd < 0) return gx_int(8);
    if (close(fd) != 0) return gx_int(gx_os_status(errno));
    return gx_int(0);
}
