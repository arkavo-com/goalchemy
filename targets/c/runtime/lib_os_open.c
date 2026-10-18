#include "gx.h"
#include <errno.h>
#include <fcntl.h>
#include <stdlib.h>
#include <unistd.h>
/* Open files by handle; handles are never reused. */
typedef struct gx_os_file { int64_t id; int fd; struct gx_os_file *next; } gx_os_file;
static gx_os_file *gx_os_files;
static int64_t gx_os_last;
int64_t gx_os_register(int fd) {
    gx_os_file *f = malloc(sizeof(*f));
    if (!f) gx_fault("out of memory");
    f->id = ++gx_os_last; f->fd = fd; f->next = gx_os_files; gx_os_files = f;
    return f->id;
}
int gx_os_fd(gx_V handle) {
    if (handle.t != GX_INT) return -1;
    for (gx_os_file *f = gx_os_files; f; f = f->next) if (f->id == gx_i(handle)) return f->fd;
    return -1;
}
int gx_os_forget(gx_V handle) {
    if (handle.t != GX_INT) return -1;
    for (gx_os_file **at = &gx_os_files; *at; at = &(*at)->next) {
        gx_os_file *f = *at;
        if (f->id != gx_i(handle)) continue;
        int fd = f->fd; *at = f->next; free(f);
        return fd;
    }
    return -1;
}
gx_V gx_lib_os_open(gx_V name, gx_V flag, gx_V perm) {
    int64_t status = 0, fl = gx_i(flag);
    gx_V out[2] = {gx_int(0), gx_int(0)};
    if ((fl & ~63) || (fl & 3) == 3) { out[1] = gx_int(8); return gx_tuple(2, out); }
    char *path = gx_os_path(name, &status);
    if (!path) { out[1] = gx_int(status); return gx_tuple(2, out); }
    int oflag = (fl & 3) == 2 ? O_RDWR : (fl & 3) == 1 ? O_WRONLY : O_RDONLY;
    if (fl & 4) oflag |= O_APPEND;
    if (fl & 8) oflag |= O_CREAT;
    if (fl & 16) oflag |= O_EXCL;
    if (fl & 32) oflag |= O_TRUNC;
    int fd = open(path, oflag | O_CLOEXEC, (mode_t)(gx_i(perm) & 0777));
    free(path);
    if (fd < 0) { out[1] = gx_int(gx_os_status(errno)); return gx_tuple(2, out); }
    out[0] = gx_int(gx_os_register(fd));
    return gx_tuple(2, out);
}
