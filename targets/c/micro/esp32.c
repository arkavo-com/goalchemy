/* ESP-IDF main-task adapter. Keep the arena out of the FreeRTOS stack. */
#include "micro.h"
#include <stdio.h>
#include <inttypes.h>
#include "esp_timer.h"
#include "esp_heap_caps.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
static _Alignas(max_align_t) unsigned char arena[GX_MICRO_ARENA_BYTES];
static void output(void *state, const void *bytes, size_t n) {
    (void)state; fwrite(bytes, 1, n, stdout);
}
void app_main(void) {
    gx_micro_stats stats = {0};
    int64_t started = esp_timer_get_time();
    int status = goalchemy_micro_run(arena, sizeof arena, output, NULL, &stats);
    int64_t elapsed = esp_timer_get_time() - started;
    printf("\ngoalchemy: status=%d execution_us=%" PRId64
           " arena_peak_bytes=%zu arena_capacity_bytes=%zu allocations=%zu"
           " source_depth=%zu task_stack_free_min_bytes=%u heap_free_bytes=%zu heap_free_min_bytes=%zu\n",
        status, elapsed, stats.arena_peak_bytes, stats.arena_capacity_bytes,
        stats.allocations, stats.peak_source_depth, (unsigned)uxTaskGetStackHighWaterMark(NULL),
        heap_caps_get_free_size(MALLOC_CAP_8BIT), heap_caps_get_minimum_free_size(MALLOC_CAP_8BIT));
    if (stats.fault) printf("goalchemy fault: %s\n", stats.fault);
}
