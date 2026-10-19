/* Host smoke runner: execution time and arena usage, excluding compilation. */
#include "micro.h"
#include <stdio.h>
#include <stdlib.h>
#include <time.h>
static _Alignas(max_align_t) unsigned char arena[GX_MICRO_ARENA_BYTES];
static void output(void *state, const void *bytes, size_t n) {
    (void)state; fwrite(bytes, 1, n, stderr);
}
int main(void) {
    gx_micro_stats stats = {0};
    struct timespec before, after;
    clock_gettime(CLOCK_MONOTONIC, &before);
    int status = goalchemy_micro_run(arena, sizeof arena, output, NULL, &stats);
    clock_gettime(CLOCK_MONOTONIC, &after);
    if (stats.fault) fprintf(stderr, "goalchemy runtime fault: %s\n", stats.fault);
    const char *path = getenv("GOALCHEMY_MICRO_METRICS");
    if (path) {
        FILE *f = fopen(path, "w");
        if (!f) return 1;
        fprintf(f, "{\"execution_seconds\":%.9f,\"status\":%d,\"arena_capacity_bytes\":%zu,\"arena_peak_bytes\":%zu,\"allocations\":%zu,\"peak_source_depth\":%zu}\n",
            (after.tv_sec - before.tv_sec) + (after.tv_nsec - before.tv_nsec) / 1e9,
            status, stats.arena_capacity_bytes, stats.arena_peak_bytes, stats.allocations, stats.peak_source_depth);
        fclose(f);
    }
    return status;
}
