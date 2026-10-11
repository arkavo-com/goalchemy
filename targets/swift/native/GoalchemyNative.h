/* SPDX-License-Identifier: Apache-2.0 */
#ifndef GOALCHEMY_NATIVE_H
#define GOALCHEMY_NATIVE_H
#include <stddef.h>
#include <stdint.h>
int gcn_random(uint8_t *, size_t);
int gcn_sha256(const uint8_t *, size_t, uint8_t *);
int gcn_hmac(const uint8_t *, size_t, const uint8_t *, size_t, uint8_t *);
int gcn_aes(int, const uint8_t *, const uint8_t *, const uint8_t *, size_t, const uint8_t *, size_t, uint8_t *, size_t *);
void *gcn_key_generate(int);
void *gcn_key_import(const uint8_t *, size_t);
int gcn_key_kind(void *);
int gcn_key_width(void *);
int gcn_key_private(void *);
void *gcn_key_retain(void *);
void gcn_key_release(void *);
int gcn_key_export(void *, int, uint8_t **, size_t *);
int gcn_key_component(void *, int, uint8_t *, size_t *);
int gcn_sign(int, int, void *, const uint8_t *, size_t, uint8_t *, size_t *);
int gcn_verify(int, int, void *, const uint8_t *, size_t, const uint8_t *, size_t);
int gcn_oaep(int, void *, const uint8_t *, size_t, uint8_t *, size_t *);
int gcn_ecdh(void *, void *, uint8_t *, size_t *);
uint32_t gcn_crc32(const uint8_t *, size_t);
void gcn_free(void *);

typedef struct gcn_http gcn_http;
gcn_http *gcn_http_create(void);
void gcn_http_cancel(gcn_http *);
int gcn_http_run(gcn_http *, const char *, const char *, const char *const *, size_t, const uint8_t *, size_t, size_t, int64_t);
int gcn_http_status(gcn_http *);
const uint8_t *gcn_http_body(gcn_http *, size_t *);
size_t gcn_http_headers(gcn_http *);
const char *gcn_http_header_name(gcn_http *, size_t);
const char *gcn_http_header_value(gcn_http *, size_t);
const char *gcn_http_error(gcn_http *);
void gcn_http_release(gcn_http *);
int gcn_http_socket(gcn_http *, const char *);
int gcn_http_tls(gcn_http *, const uint8_t *, size_t, const uint8_t *, size_t, const uint8_t *, size_t, int);
#endif
