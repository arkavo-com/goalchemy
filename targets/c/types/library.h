/* Owned native value ABI. No collector pointers cross this boundary. */
#ifndef GOALCHEMY_LIBRARY_H
#define GOALCHEMY_LIBRARY_H
#include <stddef.h>
#include <stdint.h>
#include <stdbool.h>
#include <stdatomic.h>
typedef enum { GXC_NIL, GXC_BOOL, GXC_INT, GXC_BYTES, GXC_LIST, GXC_RECORD, GXC_FLOAT, GXC_HANDLE } gxc_kind;
/* An opaque handle to a source object owned by a library instance. */
typedef struct gxc_handle gxc_handle;
typedef struct gxc_value {
    gxc_kind kind;
    int64_t integer;
    double floating;
    uint8_t *bytes;
    size_t length;
    struct gxc_value *items;
    char **names;
    /* GXC_HANDLE only: borrowed by values; released by gxc_handle_free. */
    gxc_handle *handle;
} gxc_value;
typedef struct gxc_error { int kind; gxc_value message, fields; } gxc_error;
/* Callback executes on a native worker outside source/runtime locks. Return 0
 * for owned response, 1 for declared rejection, 3 for host fault. It must release
 * its actual resources before returning; cancellation is cooperative. */
typedef int (*gxc_provider)(void *state, const uint8_t *name, size_t name_length,
    const uint8_t *payload, size_t payload_length, const atomic_bool *canceled,
    gxc_value *response);
extern _Thread_local bool gxc_callback_thread;
typedef struct gxc_options {
    const atomic_bool *canceled;
    int64_t timeout_nanoseconds; /* 0: no call deadline, positive: owner-relative */
    gxc_provider provider;
    void *provider_state; /* retained by caller until invocation returns */
} gxc_options;
/* Inputs are copied before waiting for the serialized source owner. Outputs and
 * errors are malloc-owned and detached before the source owner retires. */
int goalchemy_invoke(const char *name, const gxc_value *arguments, size_t count,
    const gxc_options *options, gxc_value *result, gxc_error *error);
/* Error kinds: 1 source, 2 panic, 3 host fault, 4 fatal, 5 invalid argument,
 * 6 canceled, 7 closed handle, 8 poisoned instance, 9 handles of different
 * instances. Handle methods are invoked as "Type.Method" with the receiver
 * as the first argument. Returning the same source pointer again returns the
 * same live gxc_handle. */
/* Fails calls queued on h, cancels its active call, runs the source Close if
 * any, then releases h. Later calls fail with kind 7. h stays valid until
 * gxc_handle_free. */
int gxc_handle_close(gxc_handle *h, gxc_error *error);
/* Releases h without running Close if it is still open, then frees it. */
void gxc_handle_free(gxc_handle *h);
const char *gxc_handle_type(const gxc_handle *h);
/* Receives boundary warnings such as abandoned goroutines; NULL restores the
 * default, which writes them to standard error. */
void gxc_set_warning_handler(void (*handler)(void *state, const char *message), void *state);
/* Wake the serialized active driver; never directly runs source frames. */
void goalchemy_wake(void);
void gxc_value_free(gxc_value *value);
void gxc_error_free(gxc_error *error);
int gxc_value_copy(gxc_value *destination, const gxc_value *source);
const gxc_value *gxc_field(const gxc_value *value, const char *name);
/* One std/log/slog record. Levels follow log/slog: -4 debug, 0 info, 4 warn,
 * 8 error. Strings are UTF-8 bytes with lengths and a terminating NUL; attrs
 * holds attr_count key/value pairs as 2*attr_count strings, with group names
 * joined to keys by ".". text is the record rendered like slog's TextHandler
 * without the time. Everything is freed when the handler returns. */
typedef struct gxc_log_record {
    int64_t level;
    int64_t unix_nano;
    const char *message;
    size_t message_length;
    const char *const *attrs;
    const size_t *attr_lengths;
    size_t attr_count;
    const char *text;
    size_t text_length;
} gxc_log_record;
typedef void (*gxc_log_handler)(void *state, const gxc_log_record *record);
/* Routes records at level and above to handler, synchronously on the source
 * owner's thread; the handler must not call into the library. A NULL handler
 * restores the default: text written to standard error at level 4 and above. */
void gxc_set_log_handler(gxc_log_handler handler, void *state, int64_t level);
#endif
