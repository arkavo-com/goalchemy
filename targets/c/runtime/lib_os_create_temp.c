#include "gx.h"
#include <errno.h>
#include <fcntl.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>
static void gx_os_random_name(char *out, size_t n) {
    static const char alphabet[] = "0123456789abcdefghijklmnopqrstuvwxyz";
    static uint64_t fallback;
    uint8_t r[16];
    bool ok = false;
    int fd = open("/dev/urandom", O_RDONLY | O_CLOEXEC);
    if (fd >= 0) { ok = read(fd, r, sizeof r) == (ssize_t)sizeof r; close(fd); }
    if (!ok) {
        struct timespec ts; clock_gettime(CLOCK_REALTIME, &ts);
        uint64_t x = (uint64_t)ts.tv_nsec ^ ((uint64_t)ts.tv_sec << 20) ^ ((uint64_t)getpid() << 40) ^ ++fallback;
        for (size_t i = 0; i < sizeof r; i++) { x ^= x << 13; x ^= x >> 7; x ^= x << 17; r[i] = (uint8_t)x; }
    }
    for (size_t i = 0; i < n; i++) out[i] = alphabet[r[i % sizeof r] % 36];
}
gx_V gx_lib_os_create_temp(gx_V dir, gx_V prefix, gx_V suffix) {
    gx_V out[3] = {gx_int(0), gx_str("", 0), gx_int(0)};
    const uint8_t *d = gx_sbytes(dir), *p = gx_sbytes(prefix), *s = gx_sbytes(suffix);
    size_t dn = gx_slen(dir), pn = gx_slen(prefix), sn = gx_slen(suffix);
    if ((dn && memchr(d, 0, dn)) || (pn && (memchr(p, 0, pn) || memchr(p, '/', pn))) || (sn && (memchr(s, 0, sn) || memchr(s, '/', sn)))) { out[2] = gx_int(8); return gx_tuple(3, out); }
    const char *base = NULL;
    if (dn == 0) { base = getenv("TMPDIR"); if (!base || !*base) base = "/tmp"; d = (const uint8_t *)base; dn = strlen(base); }
    size_t cap = dn + 1 + pn + 10 + sn;
    char *name = malloc(cap + 1);
    if (!name) gx_fault("out of memory");
    for (int attempt = 0; ; attempt++) {
        memcpy(name, d, dn); name[dn] = '/'; memcpy(name + dn + 1, p, pn);
        gx_os_random_name(name + dn + 1 + pn, 10);
        memcpy(name + dn + 1 + pn + 10, s, sn); name[cap] = 0;
        int fd = open(name, O_RDWR | O_CREAT | O_EXCL | O_CLOEXEC, 0600);
        if (fd >= 0) { out[0] = gx_int(gx_os_register(fd)); out[1] = gx_str(name, cap); free(name); return gx_tuple(3, out); }
        if (errno == EEXIST && attempt < 10000) continue;
        out[1] = gx_str(name, cap); out[2] = gx_int(gx_os_status(errno)); free(name);
        return gx_tuple(3, out);
    }
}
