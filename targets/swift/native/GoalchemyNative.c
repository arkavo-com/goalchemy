/* SPDX-License-Identifier: Apache-2.0 */
#include "GoalchemyNative.h"
#include <openssl/evp.h>
#include <openssl/rsa.h>
#include <openssl/ec.h>
#include <openssl/pem.h>
#include <openssl/rand.h>
#include <openssl/hmac.h>
#include <openssl/core_names.h>
#include <openssl/err.h>
#include <openssl/x509.h>
#include <openssl/ssl.h>
#include <curl/curl.h>
#include <stdatomic.h>
#include <ctype.h>
#include <zlib.h>
#include <limits.h>
#include <stdlib.h>
#include <string.h>

void gcn_free(void *p) { free(p); }
int gcn_random(uint8_t *out,size_t n) { return n<=INT_MAX && RAND_bytes(out,(int)n)==1; }
int gcn_sha256(const uint8_t *data,size_t n,uint8_t *out) { unsigned int len=0;return EVP_Digest(data,n,out,&len,EVP_sha256(),NULL)==1 && len==32; }
int gcn_hmac(const uint8_t *key,size_t nk,const uint8_t *data,size_t n,uint8_t *out) { unsigned int len=0;return nk<=INT_MAX && HMAC(EVP_sha256(),key,(int)nk,data,n,out,&len)!=NULL && len==32; }
int gcn_aes(int decrypt,const uint8_t *key,const uint8_t *iv,const uint8_t *data,size_t n,const uint8_t *aad,size_t na,uint8_t *out,size_t *size) {
    int ok=0,l=0,last=0;EVP_CIPHER_CTX *ctx=EVP_CIPHER_CTX_new();if(!ctx)return 0;
    if(n>INT_MAX || na>INT_MAX || (decrypt && n<16))goto done;
    if(decrypt) {
        if(EVP_DecryptInit_ex(ctx,EVP_aes_256_gcm(),NULL,NULL,NULL)!=1 || EVP_CIPHER_CTX_ctrl(ctx,EVP_CTRL_GCM_SET_IVLEN,12,NULL)!=1 || EVP_DecryptInit_ex(ctx,NULL,NULL,key,iv)!=1)goto done;
        if(na && EVP_DecryptUpdate(ctx,NULL,&l,aad,(int)na)!=1)goto done;
        if(EVP_DecryptUpdate(ctx,out,&l,data,(int)n-16)!=1 || EVP_CIPHER_CTX_ctrl(ctx,EVP_CTRL_GCM_SET_TAG,16,(void *)(data+n-16))!=1 || EVP_DecryptFinal_ex(ctx,out+l,&last)!=1)goto done;
        *size=(size_t)(l+last);ok=1;
    } else {
        if(EVP_EncryptInit_ex(ctx,EVP_aes_256_gcm(),NULL,NULL,NULL)!=1 || EVP_CIPHER_CTX_ctrl(ctx,EVP_CTRL_GCM_SET_IVLEN,12,NULL)!=1 || EVP_EncryptInit_ex(ctx,NULL,NULL,key,iv)!=1)goto done;
        if(na && EVP_EncryptUpdate(ctx,NULL,&l,aad,(int)na)!=1)goto done;
        if(EVP_EncryptUpdate(ctx,out,&l,data,(int)n)!=1 || EVP_EncryptFinal_ex(ctx,out+l,&last)!=1 || EVP_CIPHER_CTX_ctrl(ctx,EVP_CTRL_GCM_GET_TAG,16,out+l+last)!=1)goto done;
        *size=(size_t)(l+last+16);ok=1;
    }
done:EVP_CIPHER_CTX_free(ctx);return ok;
}
/* kind is an RSA modulus size in bits (2048 or 4096) or an EC curve size (256, 384 or 521). */
void *gcn_key_generate(int kind) {
    int rsa=kind>=2048;EVP_PKEY *key=NULL;EVP_PKEY_CTX *ctx=EVP_PKEY_CTX_new_id(rsa ? EVP_PKEY_RSA:EVP_PKEY_EC,NULL);if(!ctx)return NULL;
    int ok=EVP_PKEY_keygen_init(ctx)>0;
    if(ok)ok=rsa ? EVP_PKEY_CTX_set_rsa_keygen_bits(ctx,kind)>0:EVP_PKEY_CTX_set_ec_paramgen_curve_nid(ctx,kind==384 ? NID_secp384r1:kind==521 ? NID_secp521r1:NID_X9_62_prime256v1)>0;
    if(ok)ok=EVP_PKEY_keygen(ctx,&key)>0;EVP_PKEY_CTX_free(ctx);if(!ok){EVP_PKEY_free(key);key=NULL;}return key;
}
/* OpenSSL names explicit parameters that resemble a named curve, so the encoding is checked too. */
static int gcn_ec_width(EVP_PKEY *key) {
    char group[80],encoding[32];size_t n=0;if(!EVP_PKEY_is_a(key,"EC") || EVP_PKEY_get_utf8_string_param(key,OSSL_PKEY_PARAM_GROUP_NAME,group,sizeof(group),&n)<=0)return 0;
    if(EVP_PKEY_get_utf8_string_param(key,OSSL_PKEY_PARAM_EC_ENCODING,encoding,sizeof(encoding),&n)<=0 || strcmp(encoding,OSSL_PKEY_EC_ENCODING_GROUP))return 0;
    if(!strcmp(group,"prime256v1") || !strcmp(group,"P-256"))return 32;
    if(!strcmp(group,"secp384r1") || !strcmp(group,"P-384"))return 48;
    if(!strcmp(group,"secp521r1") || !strcmp(group,"P-521"))return 66;
    return 0;
}
int gcn_key_kind(void *raw) {
    EVP_PKEY *key=raw;if(!key)return 0;
    if(EVP_PKEY_is_a(key,"RSA")){int bits=EVP_PKEY_get_bits(key);return bits==2048 || bits==3072 || bits==4096;}
    return gcn_ec_width(key) ? 2:0;
}
/* RSA modulus or EC coordinate width in bytes. */
int gcn_key_width(void *raw) { EVP_PKEY *key=raw;return gcn_key_kind(key)==1 ? EVP_PKEY_get_size(key):gcn_ec_width(key); }
int gcn_key_private(void *raw) { EVP_PKEY *key=raw;BIGNUM *bn=NULL;int ok=EVP_PKEY_get_bn_param(key,gcn_key_kind(key)==1 ? OSSL_PKEY_PARAM_RSA_D:OSSL_PKEY_PARAM_PRIV_KEY,&bn)>0;BN_clear_free(bn);return ok; }
static int gcn_no_password(char *buffer,int size,int write,void *user) { (void)buffer;(void)size;(void)write;(void)user;return 0; }
void *gcn_key_import(const uint8_t *data,size_t n) {
    if(n>65536 || n>INT_MAX)return NULL;
    BIO *bio=BIO_new_mem_buf(data,(int)n);if(!bio)return NULL;
    EVP_PKEY *key=PEM_read_bio_PrivateKey(bio,NULL,gcn_no_password,NULL);
    if(!key){ERR_clear_error();BIO_free(bio);bio=BIO_new_mem_buf(data,(int)n);if(bio)key=PEM_read_bio_PUBKEY(bio,NULL,NULL,NULL);}
    if(!key){ERR_clear_error();BIO_free(bio);bio=BIO_new_mem_buf(data,(int)n);X509 *certificate=bio ? PEM_read_bio_X509(bio,NULL,NULL,NULL):NULL;if(certificate){key=X509_get_pubkey(certificate);X509_free(certificate);}}
    if(!key){ERR_clear_error();BIO_free(bio);bio=BIO_new_mem_buf(data,(int)n);RSA *rsa=bio ? PEM_read_bio_RSAPublicKey(bio,NULL,NULL,NULL):NULL;if(rsa){key=EVP_PKEY_new();if(!key || EVP_PKEY_assign_RSA(key,rsa)!=1){EVP_PKEY_free(key);RSA_free(rsa);key=NULL;}}}
    if(key && !gcn_key_kind(key)){EVP_PKEY_free(key);key=NULL;}
    if(key){EVP_PKEY_CTX *ctx=EVP_PKEY_CTX_new(key,NULL);int valid=ctx && (gcn_key_private(key) ? EVP_PKEY_check(ctx):EVP_PKEY_public_check(ctx))>0;EVP_PKEY_CTX_free(ctx);if(!valid){EVP_PKEY_free(key);key=NULL;}}
    BIO_free(bio);return key;
}
void *gcn_key_retain(void *key) {return key && EVP_PKEY_up_ref(key)==1 ? key:NULL;}
void gcn_key_release(void *key) {EVP_PKEY_free(key);}
int gcn_key_export(void *key,int private,uint8_t **out,size_t *n) {
    BIO *bio=BIO_new(BIO_s_mem());if(!bio)return 0;
    int ok=private ? PEM_write_bio_PKCS8PrivateKey(bio,key,NULL,NULL,0,NULL,NULL):PEM_write_bio_PUBKEY(bio,key);
    if(ok){char *p=NULL;long len=BIO_get_mem_data(bio,&p);*out=malloc((size_t)len);if(!*out)ok=0;else{memcpy(*out,p,(size_t)len);*n=(size_t)len;}}
    BIO_free(bio);return ok;
}
int gcn_key_component(void *key,int component,uint8_t *out,size_t *n) {
    const char *name=component==0 ? OSSL_PKEY_PARAM_RSA_N:component==1 ? OSSL_PKEY_PARAM_RSA_E:component==2 ? OSSL_PKEY_PARAM_EC_PUB_X:OSSL_PKEY_PARAM_EC_PUB_Y;
    BIGNUM *bn=NULL;if(EVP_PKEY_get_bn_param(key,name,&bn)!=1)return 0;
    int length=component>=2 ? gcn_ec_width(key):BN_num_bytes(bn);int ok=*n>=(size_t)length && BN_bn2binpad(bn,out,length)==length;*n=(size_t)length;BN_free(bn);return ok;
}
static const EVP_MD *gcn_hash(int bits) { return bits==384 ? EVP_sha384():bits==512 ? EVP_sha512():EVP_sha256(); }
int gcn_sign(int alg,int bits,void *key,const uint8_t *data,size_t n,uint8_t *out,size_t *size) {
    EVP_MD_CTX *ctx=EVP_MD_CTX_new();size_t length=512;uint8_t signature[512];int ok=0,width=gcn_ec_width(key);if(!ctx)return 0;
    if(EVP_DigestSignInit(ctx,NULL,gcn_hash(bits),NULL,key)!=1 || EVP_DigestSign(ctx,signature,&length,data,n)!=1)goto done;
    if(alg==1){if(*size<length)goto done;memcpy(out,signature,length);*size=length;ok=1;}
    else {const unsigned char *p=signature;ECDSA_SIG *sig=d2i_ECDSA_SIG(NULL,&p,(long)length);if(!sig)goto done;const BIGNUM *r,*s;ECDSA_SIG_get0(sig,&r,&s);ok=width && *size>=(size_t)(2*width) && BN_bn2binpad(r,out,width)==width && BN_bn2binpad(s,out+width,width)==width;*size=(size_t)(2*width);ECDSA_SIG_free(sig);}
done:EVP_MD_CTX_free(ctx);return ok;
}
int gcn_verify(int alg,int bits,void *key,const uint8_t *data,size_t n,const uint8_t *signature,size_t size) {
    uint8_t der[160];const uint8_t *sig=signature;size_t length=size;
    if(alg==2){int width=gcn_ec_width(key);if(!width || size!=(size_t)(2*width))return -1;ECDSA_SIG *es=ECDSA_SIG_new();BIGNUM *r=BN_bin2bn(signature,width,NULL),*s=BN_bin2bn(signature+width,width,NULL);if(!es || !r || !s){ECDSA_SIG_free(es);BN_free(r);BN_free(s);return -1;}ECDSA_SIG_set0(es,r,s);if(i2d_ECDSA_SIG(es,NULL)>(int)sizeof(der)){ECDSA_SIG_free(es);return -1;}unsigned char *p=der;int len=i2d_ECDSA_SIG(es,&p);ECDSA_SIG_free(es);if(len<=0)return -1;sig=der;length=(size_t)len;}
    EVP_MD_CTX *ctx=EVP_MD_CTX_new();if(!ctx)return -1;int ok=EVP_DigestVerifyInit(ctx,NULL,gcn_hash(bits),NULL,key)==1 ? EVP_DigestVerify(ctx,sig,length,data,n):-1;EVP_MD_CTX_free(ctx);return ok==1 ? 1:ok==0 ? 0:-1;
}
int gcn_oaep(int decrypt,void *key,const uint8_t *data,size_t n,uint8_t *out,size_t *size) {
    EVP_PKEY_CTX *ctx=EVP_PKEY_CTX_new(key,NULL);if(!ctx)return 0;int ok=decrypt ? EVP_PKEY_decrypt_init(ctx)>0:EVP_PKEY_encrypt_init(ctx)>0;
    ok=ok && EVP_PKEY_CTX_set_rsa_padding(ctx,RSA_PKCS1_OAEP_PADDING)>0 && EVP_PKEY_CTX_set_rsa_oaep_md(ctx,EVP_sha1())>0 && EVP_PKEY_CTX_set_rsa_mgf1_md(ctx,EVP_sha1())>0;
    if(ok)ok=decrypt ? EVP_PKEY_decrypt(ctx,out,size,data,n)>0:EVP_PKEY_encrypt(ctx,out,size,data,n)>0;
    EVP_PKEY_CTX_free(ctx);return ok;
}
int gcn_ecdh(void *private,void *public,uint8_t *out,size_t *size) {
    EVP_PKEY_CTX *ctx=EVP_PKEY_CTX_new(private,NULL);if(!ctx)return 0;int ok=EVP_PKEY_derive_init(ctx)>0 && EVP_PKEY_derive_set_peer(ctx,public)>0 && EVP_PKEY_derive(ctx,out,size)>0;EVP_PKEY_CTX_free(ctx);return ok;
}
uint32_t gcn_crc32(const uint8_t *bytes,size_t n) {uLong crc=crc32(0L,Z_NULL,0);while(n){uInt chunk=n>UINT_MAX ? UINT_MAX:(uInt)n;crc=crc32(crc,bytes,chunk);bytes+=chunk;n-=chunk;}return (uint32_t)crc;}

struct gcn_header { char *name;char *value;size_t sequence; };
struct gcn_http { atomic_int canceled;uint8_t *body;size_t length,limit;struct gcn_header *headers;size_t count,header_bytes;int status;const char *error;int auto_gzip,decoded,line_status,z_active,z_done;z_stream z;uint8_t *cert,*key,*roots;size_t cert_len,key_len,roots_len;int insecure;char *socket; };
gcn_http *gcn_http_create(void){gcn_http *http=calloc(1,sizeof(*http));if(http)atomic_init(&http->canceled,0);return http;}
void gcn_http_cancel(gcn_http *http){if(http)atomic_store(&http->canceled,1);}
static int http_progress(void *data,curl_off_t a,curl_off_t b,curl_off_t c,curl_off_t d){(void)a;(void)b;(void)c;(void)d;return atomic_load(&((gcn_http *)data)->canceled);}
static int http_append(gcn_http *http,const uint8_t *data,size_t n){
    if(n>http->limit-http->length){http->error="http: response body exceeds limit";return 0;}
    uint8_t *out=realloc(http->body,http->length+n+1);if(!out){http->error="http: allocation failed";return 0;}
    http->body=out;memcpy(out+http->length,data,n);http->length+=n;return 1;
}
static void http_gzip_end(gcn_http *http){if(http->z_active){inflateEnd(&http->z);http->z_active=0;}}
static int http_gunzip(gcn_http *http,const uint8_t *data,size_t n){
    if(!http->z_active){memset(&http->z,0,sizeof http->z);if(inflateInit2(&http->z,16+MAX_WBITS)!=Z_OK){http->error="http: allocation failed";return 0;}http->z_active=1;http->z_done=0;}
    uint8_t out[16384];http->z.next_in=(Bytef *)data;http->z.avail_in=(uInt)n;
    do{
        if(http->z_done){if(!http->z.avail_in)break;if(inflateReset(&http->z)!=Z_OK){http->error="http: transport failure";return 0;}http->z_done=0;}
        http->z.next_out=out;http->z.avail_out=sizeof out;int r=inflate(&http->z,Z_NO_FLUSH);size_t got=sizeof out-http->z.avail_out;
        if(got && !http_append(http,out,got))return 0;
        if(r==Z_STREAM_END)http->z_done=1;else if(r==Z_BUF_ERROR && !got)break;else if(r!=Z_OK && r!=Z_BUF_ERROR){http->error="http: transport failure";return 0;}
    }while(http->z.avail_in || !http->z.avail_out);
    return 1;
}
static size_t http_body(char *data,size_t size,size_t count,void *opaque){
    gcn_http *http=opaque;if(count && size>SIZE_MAX/count)return 0;size_t n=size*count;
    if(n>UINT_MAX){http->error="http: transport failure";return 0;}
    if(http->decoded)return http_gunzip(http,(const uint8_t *)data,n) ? n:0;
    if(n>http->limit-http->length){http->error="http: response body exceeds limit";return 0;}
    uint8_t *out=realloc(http->body,http->length+n+1);if(!out){http->error="http: allocation failed";return 0;}
    http->body=out;memcpy(out+http->length,data,n);http->length+=n;return n;
}
static void http_clear_headers(gcn_http *http){for(size_t i=0;i<http->count;i++){free(http->headers[i].name);free(http->headers[i].value);}free(http->headers);http->headers=NULL;http->count=0;}
static char *http_string(const char *p,size_t n){char *s=malloc(n+1);if(!s)return NULL;memcpy(s,p,n);s[n]=0;return s;}
static size_t http_header(char *data,size_t size,size_t count,void *opaque){
    gcn_http *http=opaque;if(count && size>SIZE_MAX/count)return 0;size_t n=size*count;
    http->header_bytes+=n;if(http->header_bytes>65536){http->error="http: response headers exceed limit";return 0;}
    if(n>=5 && !memcmp(data,"HTTP/",5)){http_clear_headers(http);http_gzip_end(http);http->decoded=0;http->line_status=0;if(n>=12)for(size_t i=9;i<12;i++)http->line_status=http->line_status*10+(data[i]-'0');return n;}
    if(n<=2){
        const char *encoding=NULL,*length=NULL;for(size_t i=0;i<http->count;i++){if(!encoding && !strcmp(http->headers[i].name,"Content-Encoding"))encoding=http->headers[i].value;if(!length && !strcmp(http->headers[i].name,"Content-Length"))length=http->headers[i].value;}
        int st=http->line_status,bodyless=st==204||st==304||(st>=100&&st<200)||(length && !strcmp(length,"0"));
        http->decoded=http->auto_gzip && encoding && !strcasecmp(encoding,"gzip") && !bodyless;
        return n;
    }
    char *colon=memchr(data,':',n);if(!colon)return n;
    size_t name_size=(size_t)(colon-data);const char *start=colon+1,*end=data+n;
    while(start<end && (*start==' ' || *start=='\t'))start++;
    while(end>start && (end[-1]=='\r' || end[-1]=='\n' || end[-1]==' ' || end[-1]=='\t'))end--;
    char *name=http_string(data,name_size),*value=http_string(start,(size_t)(end-start));
    if(!name || !value){free(name);free(value);http->error="http: allocation failed";return 0;}
    int upper=1;for(size_t i=0;i<name_size;i++){unsigned char c=(unsigned char)name[i];name[i]=(char)(upper ? toupper(c):tolower(c));upper=c=='-';}
    struct gcn_header *headers=realloc(http->headers,(http->count+1)*sizeof(*headers));
    if(!headers){free(name);free(value);http->error="http: allocation failed";return 0;}
    http->headers=headers;headers[http->count]=(struct gcn_header){name,value,http->count};http->count++;return n;
}
static int header_compare(const void *a,const void *b){const struct gcn_header *x=a,*y=b;int cmp=strcmp(x->name,y->name);return cmp ? cmp:x->sequence<y->sequence ? -1:x->sequence>y->sequence;}
static int http_certs(const uint8_t *data,size_t n,X509 **first){
    BIO *bio=BIO_new_mem_buf(data,(int)n);if(!bio)return -1;int count=0;X509 *cert;
    while((cert=PEM_read_bio_X509(bio,NULL,NULL,NULL))){if(first && !count)*first=cert;else X509_free(cert);count++;}
    ERR_clear_error();BIO_free(bio);return count;
}
static uint8_t *http_copy(const uint8_t *data,size_t n){uint8_t *p=malloc(n?n:1);if(p && n)memcpy(p,data,n);return p;}
/* Stores transport options: 1 valid, 0 a certificate, key or root CA that does not parse, -1 allocation failure. */
int gcn_http_tls(gcn_http *http,const uint8_t *cert,size_t cert_len,const uint8_t *key,size_t key_len,const uint8_t *roots,size_t roots_len,int insecure){
    http->insecure=insecure;
    if(roots){int n=http_certs(roots,roots_len,NULL);if(n<0)return -1;if(!n)return 0;if(!(http->roots=http_copy(roots,roots_len)))return -1;http->roots_len=roots_len;}
    if(cert){
        X509 *leaf=NULL;int n=http_certs(cert,cert_len,&leaf);if(n<0)return -1;if(!n)return 0;
        BIO *bio=BIO_new_mem_buf(key,(int)key_len);if(!bio){X509_free(leaf);return -1;}
        EVP_PKEY *pkey=PEM_read_bio_PrivateKey(bio,NULL,NULL,NULL);int ok=pkey && X509_check_private_key(leaf,pkey)==1;EVP_PKEY_free(pkey);X509_free(leaf);BIO_free(bio);ERR_clear_error();
        if(!ok)return 0;
        if(!(http->cert=http_copy(cert,cert_len)) || !(http->key=http_copy(key,key_len)))return -1;http->cert_len=cert_len;http->key_len=key_len;
    }
    return 1;
}
int gcn_http_socket(gcn_http *http,const char *path){free(http->socket);http->socket=strdup(path);return http->socket!=NULL;}
static CURLcode http_ssl_ctx(CURL *curl,void *ssl_ctx,void *opaque){
    (void)curl;gcn_http *http=opaque;BIO *bio=BIO_new_mem_buf(http->roots,(int)http->roots_len);if(!bio)return CURLE_OUT_OF_MEMORY;
    X509_STORE *store=SSL_CTX_get_cert_store((SSL_CTX *)ssl_ctx);X509 *cert;CURLcode code=CURLE_OK;
    while((cert=PEM_read_bio_X509(bio,NULL,NULL,NULL))){if(!X509_STORE_add_cert(store,cert)){unsigned long e=ERR_peek_last_error();if(ERR_GET_REASON(e)!=X509_R_CERT_ALREADY_IN_HASH_TABLE)code=CURLE_SSL_CACERT_BADFILE;}X509_free(cert);}
    ERR_clear_error();BIO_free(bio);return code;
}
int gcn_http_run(gcn_http *http,const char *method,const char *url,const char *const *headers,size_t header_count,const uint8_t *body,size_t body_size,size_t limit,int64_t timeout){
    CURL *curl=curl_easy_init();struct curl_slist *list=NULL;int ok=0,auto_gzip=1,seen_encoding=0,seen_range=0;if(!curl){http->error="http: transport initialization failed";return 0;}http->limit=limit;
    for(size_t i=0;i<header_count;i+=2){size_t n=strlen(headers[i])+strlen(headers[i+1])+3;char *header=malloc(n);if(!header){http->error="http: allocation failed";goto done;}snprintf(header,n,"%s: %s",headers[i],headers[i+1]);struct curl_slist *next=curl_slist_append(list,header);free(header);if(!next){http->error="http: allocation failed";goto done;}list=next;if(!strcasecmp(headers[i],"accept-encoding") && !seen_encoding++ && *headers[i+1])auto_gzip=0;if(!strcasecmp(headers[i],"range") && !seen_range++ && *headers[i+1])auto_gzip=0;}
    curl_easy_setopt(curl,CURLOPT_URL,url);curl_easy_setopt(curl,CURLOPT_HTTPHEADER,list);curl_easy_setopt(curl,CURLOPT_NOSIGNAL,1L);
    curl_easy_setopt(curl,CURLOPT_FOLLOWLOCATION,0L);curl_easy_setopt(curl,CURLOPT_MAXREDIRS,0L);curl_easy_setopt(curl,CURLOPT_TIMEOUT_MS,(long)timeout);
    curl_easy_setopt(curl,CURLOPT_SSL_VERIFYPEER,http->insecure ? 0L:1L);curl_easy_setopt(curl,CURLOPT_SSL_VERIFYHOST,http->insecure ? 0L:2L);
    if(http->cert){struct curl_blob cert={http->cert,http->cert_len,CURL_BLOB_COPY},key={http->key,http->key_len,CURL_BLOB_COPY};curl_easy_setopt(curl,CURLOPT_SSLCERT_BLOB,&cert);curl_easy_setopt(curl,CURLOPT_SSLCERTTYPE,"PEM");curl_easy_setopt(curl,CURLOPT_SSLKEY_BLOB,&key);curl_easy_setopt(curl,CURLOPT_SSLKEYTYPE,"PEM");}
    if(http->roots){curl_easy_setopt(curl,CURLOPT_SSL_CTX_FUNCTION,http_ssl_ctx);curl_easy_setopt(curl,CURLOPT_SSL_CTX_DATA,http);}
    if(http->socket){curl_easy_setopt(curl,CURLOPT_UNIX_SOCKET_PATH,http->socket);curl_easy_setopt(curl,CURLOPT_NOPROXY,"*");}curl_easy_setopt(curl,CURLOPT_PROTOCOLS,CURLPROTO_HTTP|CURLPROTO_HTTPS);
    curl_easy_setopt(curl,CURLOPT_WRITEFUNCTION,http_body);curl_easy_setopt(curl,CURLOPT_WRITEDATA,http);curl_easy_setopt(curl,CURLOPT_HEADERFUNCTION,http_header);curl_easy_setopt(curl,CURLOPT_HEADERDATA,http);
    curl_easy_setopt(curl,CURLOPT_XFERINFOFUNCTION,http_progress);curl_easy_setopt(curl,CURLOPT_XFERINFODATA,http);curl_easy_setopt(curl,CURLOPT_NOPROGRESS,0L);
    http->auto_gzip=auto_gzip;curl_easy_setopt(curl,CURLOPT_HTTP_CONTENT_DECODING,0L);
    if(auto_gzip){struct curl_slist *next=curl_slist_append(list,"Accept-Encoding: gzip");if(!next){http->error="http: allocation failed";goto done;}list=next;curl_easy_setopt(curl,CURLOPT_HTTPHEADER,list);}
    if(!strcmp(method,"POST")){curl_easy_setopt(curl,CURLOPT_POST,1L);curl_easy_setopt(curl,CURLOPT_POSTFIELDS,body ? (const char *)body:"");curl_easy_setopt(curl,CURLOPT_POSTFIELDSIZE_LARGE,(curl_off_t)body_size);}
    CURLcode result=curl_easy_perform(curl);
    if(result==CURLE_OK && !http->error && http->decoded && http->z_active && !http->z_done){http->error="http: transport failure";http_gzip_end(http);goto done;}
    http_gzip_end(http);
    if(result!=CURLE_OK){if(!http->error)http->error=result==CURLE_OPERATION_TIMEDOUT ? "context deadline exceeded":result==CURLE_ABORTED_BY_CALLBACK ? "context canceled":curl_easy_strerror(result);goto done;}
    long status=0;curl_easy_getinfo(curl,CURLINFO_RESPONSE_CODE,&status);http->status=(int)status;
    if(http->decoded){{size_t out=0;for(size_t i=0;i<http->count;i++){if(!strcmp(http->headers[i].name,"Content-Encoding") || !strcmp(http->headers[i].name,"Content-Length")){free(http->headers[i].name);free(http->headers[i].value);}else{http->headers[out++]=http->headers[i];}}http->count=out;}}
    if(http->count)qsort(http->headers,http->count,sizeof(*http->headers),header_compare);ok=1;
done:curl_easy_cleanup(curl);curl_slist_free_all(list);return ok;
}
int gcn_http_status(gcn_http *http){return http->status;}
const uint8_t *gcn_http_body(gcn_http *http,size_t *length){*length=http->length;return http->body;}
size_t gcn_http_headers(gcn_http *http){return http->count;}
const char *gcn_http_header_name(gcn_http *http,size_t index){return http->headers[index].name;}
const char *gcn_http_header_value(gcn_http *http,size_t index){return http->headers[index].value;}
const char *gcn_http_error(gcn_http *http){return http->error ? http->error:"http: transport failure";}
void gcn_http_release(gcn_http *http){if(http){http_gzip_end(http);http_clear_headers(http);free(http->body);free(http->cert);if(http->key){OPENSSL_cleanse(http->key,http->key_len);free(http->key);}free(http->roots);free(http->socket);free(http);}}

/* Streamed lib/http exchanges: a libcurl transfer thread per exchange; the
 * blocking operations below run on Swift worker threads. */
#include <pthread.h>
#define GCN_EXCHANGE_BUFFER (1u<<20)
struct gcn_exchange {
    pthread_mutex_t mu;pthread_cond_t cv;int refs;
    char *method,*url;struct curl_slist *headers;int64_t length;gcn_http *transport;
    int stop,ended,done,head;const char *error;
    const uint8_t *chunk;size_t chunk_len,chunk_off;
    uint8_t *in;size_t in_len,in_off;
    char *hdr;size_t hdr_len;long status;
    int read_paused,write_paused;
};
static void exchange_release(gcn_exchange *x){
    pthread_mutex_lock(&x->mu);int refs=--x->refs;pthread_mutex_unlock(&x->mu);if(refs)return;
    pthread_mutex_destroy(&x->mu);pthread_cond_destroy(&x->cv);free(x->method);free(x->url);curl_slist_free_all(x->headers);gcn_http_release(x->transport);free(x->in);free(x->hdr);free(x);
}
void gcn_exchange_release(gcn_exchange *x){if(x)exchange_release(x);}
void gcn_exchange_abort(gcn_exchange *x){if(!x)return;pthread_mutex_lock(&x->mu);x->stop=1;pthread_cond_broadcast(&x->cv);pthread_mutex_unlock(&x->mu);}
static size_t exchange_on_read(char *dest,size_t a,size_t b,void *arg){
    gcn_exchange *x=arg;size_t cap=a*b,n=0;pthread_mutex_lock(&x->mu);
    if(x->stop)n=CURL_READFUNC_ABORT;
    else if(x->chunk && x->chunk_off<x->chunk_len){n=x->chunk_len-x->chunk_off;if(n>cap)n=cap;memcpy(dest,x->chunk+x->chunk_off,n);x->chunk_off+=n;if(x->chunk_off==x->chunk_len){x->chunk=NULL;pthread_cond_broadcast(&x->cv);}}
    else if(!x->ended){x->read_paused=1;n=CURL_READFUNC_PAUSE;}
    pthread_mutex_unlock(&x->mu);return n;
}
static size_t exchange_on_write(char *data,size_t a,size_t b,void *arg){
    gcn_exchange *x=arg;size_t n=a*b;pthread_mutex_lock(&x->mu);
    if(x->stop){pthread_mutex_unlock(&x->mu);return 0;}
    if(x->in_len-x->in_off>=GCN_EXCHANGE_BUFFER){x->write_paused=1;pthread_mutex_unlock(&x->mu);return CURL_WRITEFUNC_PAUSE;}
    if(x->in_off){memmove(x->in,x->in+x->in_off,x->in_len-x->in_off);x->in_len-=x->in_off;x->in_off=0;}
    uint8_t *p=realloc(x->in,x->in_len+n+1);if(!p){x->error="http: transport failure";pthread_mutex_unlock(&x->mu);return 0;}
    x->in=p;memcpy(p+x->in_len,data,n);x->in_len+=n;pthread_cond_broadcast(&x->cv);pthread_mutex_unlock(&x->mu);return n;
}
static size_t exchange_on_header(char *data,size_t a,size_t b,void *arg){
    gcn_exchange *x=arg;size_t n=a*b;pthread_mutex_lock(&x->mu);
    if(n>=5 && !memcmp(data,"HTTP/",5)){x->hdr_len=0;x->status=0;if(n>=12)for(size_t i=9;i<12;i++)x->status=x->status*10+(data[i]-'0');}
    if(n>65536-x->hdr_len){x->error="http: response headers exceed limit";pthread_mutex_unlock(&x->mu);return 0;}
    char *p=realloc(x->hdr,x->hdr_len+n+1);if(!p){x->error="http: transport failure";pthread_mutex_unlock(&x->mu);return 0;}
    x->hdr=p;memcpy(p+x->hdr_len,data,n);x->hdr_len+=n;
    if(((n==2 && data[0]=='\r')||(n==1 && data[0]=='\n')) && !(x->status>=100 && x->status<200)){x->head=1;pthread_cond_broadcast(&x->cv);}
    pthread_mutex_unlock(&x->mu);return n;
}
static void *exchange_transfer(void *arg){
    gcn_exchange *x=arg;gcn_http *t=x->transport;CURL *easy=curl_easy_init();CURLM *multi=curl_multi_init();const char *error=NULL;CURLcode code=CURLE_OK;int added=0;
    if(!easy || !multi)error="http: transport failure";
    if(!error){
        curl_easy_setopt(easy,CURLOPT_URL,x->url);curl_easy_setopt(easy,CURLOPT_CUSTOMREQUEST,x->method);curl_easy_setopt(easy,CURLOPT_HTTPHEADER,x->headers);
        curl_easy_setopt(easy,CURLOPT_HTTP_CONTENT_DECODING,0L);curl_easy_setopt(easy,CURLOPT_FOLLOWLOCATION,0L);curl_easy_setopt(easy,CURLOPT_NOSIGNAL,1L);curl_easy_setopt(easy,CURLOPT_FORBID_REUSE,1L);
        curl_easy_setopt(easy,CURLOPT_PROTOCOLS,CURLPROTO_HTTP|CURLPROTO_HTTPS);
        curl_easy_setopt(easy,CURLOPT_WRITEFUNCTION,exchange_on_write);curl_easy_setopt(easy,CURLOPT_WRITEDATA,x);curl_easy_setopt(easy,CURLOPT_HEADERFUNCTION,exchange_on_header);curl_easy_setopt(easy,CURLOPT_HEADERDATA,x);
        if(!strcmp(x->method,"POST")){curl_easy_setopt(easy,CURLOPT_POST,1L);curl_easy_setopt(easy,CURLOPT_READFUNCTION,exchange_on_read);curl_easy_setopt(easy,CURLOPT_READDATA,x);if(x->length>=0)curl_easy_setopt(easy,CURLOPT_POSTFIELDSIZE_LARGE,(curl_off_t)x->length);}
        curl_easy_setopt(easy,CURLOPT_SSL_VERIFYPEER,t->insecure ? 0L:1L);curl_easy_setopt(easy,CURLOPT_SSL_VERIFYHOST,t->insecure ? 0L:2L);
        if(t->cert){struct curl_blob cert={t->cert,t->cert_len,CURL_BLOB_COPY},key={t->key,t->key_len,CURL_BLOB_COPY};curl_easy_setopt(easy,CURLOPT_SSLCERT_BLOB,&cert);curl_easy_setopt(easy,CURLOPT_SSLCERTTYPE,"PEM");curl_easy_setopt(easy,CURLOPT_SSLKEY_BLOB,&key);curl_easy_setopt(easy,CURLOPT_SSLKEYTYPE,"PEM");}
        if(t->roots){curl_easy_setopt(easy,CURLOPT_SSL_CTX_FUNCTION,http_ssl_ctx);curl_easy_setopt(easy,CURLOPT_SSL_CTX_DATA,t);}
        if(t->socket){curl_easy_setopt(easy,CURLOPT_UNIX_SOCKET_PATH,t->socket);curl_easy_setopt(easy,CURLOPT_NOPROXY,"*");}
        if(curl_multi_add_handle(multi,easy)!=CURLM_OK)error="http: transport failure";else added=1;
    }
    int running=1;
    while(!error && running){
        pthread_mutex_lock(&x->mu);int stop=x->stop,resume=0;
        if(x->read_paused && (x->chunk || x->ended)){x->read_paused=0;resume=1;}
        if(x->write_paused && x->in_len-x->in_off<GCN_EXCHANGE_BUFFER){x->write_paused=0;resume=1;}
        pthread_mutex_unlock(&x->mu);
        if(stop){code=CURLE_ABORTED_BY_CALLBACK;break;}
        if(resume)curl_easy_pause(easy,CURLPAUSE_CONT);
        if(curl_multi_perform(multi,&running)!=CURLM_OK){error="http: transport failure";break;}
        if(running && curl_multi_poll(multi,NULL,0,5,NULL)!=CURLM_OK){error="http: transport failure";break;}
    }
    if(!error && code==CURLE_OK){int pending;CURLMsg *msg;while((msg=curl_multi_info_read(multi,&pending)))if(msg->msg==CURLMSG_DONE)code=msg->data.result;}
    if(added)curl_multi_remove_handle(multi,easy);if(easy)curl_easy_cleanup(easy);if(multi)curl_multi_cleanup(multi);
    pthread_mutex_lock(&x->mu);x->done=1;if(!x->error)x->error=error ? error:code!=CURLE_OK ? "http: transport failure":NULL;pthread_cond_broadcast(&x->cv);pthread_mutex_unlock(&x->mu);
    exchange_release(x);return NULL;
}
/* Takes ownership of transport (its TLS and socket options). NULL on failure. */
gcn_exchange *gcn_exchange_open(gcn_http *transport,const char *method,const char *url,const char *const *headers,size_t count,int64_t length){
    gcn_exchange *x=calloc(1,sizeof(*x));if(!x)return NULL;
    pthread_mutex_init(&x->mu,NULL);pthread_cond_init(&x->cv,NULL);x->refs=2;x->length=length;x->transport=transport;
    x->method=strdup(method);x->url=strdup(url);if(!x->method || !x->url){x->refs=1;exchange_release(x);return NULL;}
    for(size_t i=0;i<count;i+=2){size_t n=strlen(headers[i])+strlen(headers[i+1])+3;char *line=malloc(n);if(!line){x->refs=1;exchange_release(x);return NULL;}
        if(*headers[i+1])snprintf(line,n,"%s: %s",headers[i],headers[i+1]);else snprintf(line,n,"%s;",headers[i]);
        struct curl_slist *next=curl_slist_append(x->headers,line);free(line);if(!next){x->refs=1;exchange_release(x);return NULL;}x->headers=next;}
    const char *extra[]={"Expect:",length<0 ? "Transfer-Encoding: chunked":NULL};
    for(int i=0;i<2;i++)if(extra[i]){struct curl_slist *next=curl_slist_append(x->headers,extra[i]);if(!next){x->refs=1;exchange_release(x);return NULL;}x->headers=next;}
    pthread_t thread;if(pthread_create(&thread,NULL,exchange_transfer,x)){x->refs=1;exchange_release(x);return NULL;}
    pthread_detach(thread);return x;
}
void gcn_exchange_retain(gcn_exchange *x){pthread_mutex_lock(&x->mu);x->refs++;pthread_mutex_unlock(&x->mu);}
const char *gcn_exchange_error(gcn_exchange *x){pthread_mutex_lock(&x->mu);const char *e=x->error ? x->error:"http: transport failure";pthread_mutex_unlock(&x->mu);return e;}
/* Blocks until the transfer takes all of data: 1, or 0 on failure. */
int gcn_exchange_write(gcn_exchange *x,const uint8_t *data,size_t n){
    pthread_mutex_lock(&x->mu);x->chunk=data;x->chunk_len=n;x->chunk_off=0;pthread_cond_broadcast(&x->cv);
    while(x->chunk && !x->stop && !x->done)pthread_cond_wait(&x->cv,&x->mu);
    int ok=x->chunk==NULL;x->chunk=NULL;pthread_mutex_unlock(&x->mu);return ok;
}
/* Ends the request body and waits for the response head: 1, or 0 on failure. */
int gcn_exchange_response(gcn_exchange *x){
    pthread_mutex_lock(&x->mu);x->ended=1;pthread_cond_broadcast(&x->cv);
    while(!x->head && !x->stop && !x->done)pthread_cond_wait(&x->cv,&x->mu);
    int ok=x->head && !x->stop;pthread_mutex_unlock(&x->mu);return ok;
}
long gcn_exchange_status(gcn_exchange *x){return x->status;}
const char *gcn_exchange_head(gcn_exchange *x,size_t *length){*length=x->hdr_len;return x->hdr;}
/* Reads up to max response bytes: the count, 0 at the end, or -1 on failure. */
long gcn_exchange_read(gcn_exchange *x,uint8_t *buffer,size_t max){
    pthread_mutex_lock(&x->mu);
    while(x->in_len==x->in_off && !x->stop && !x->done)pthread_cond_wait(&x->cv,&x->mu);
    long n;
    if(x->stop || (x->in_len==x->in_off && x->error))n=-1;
    else{size_t k=x->in_len-x->in_off;if(k>max)k=max;memcpy(buffer,x->in+x->in_off,k);x->in_off+=k;n=(long)k;pthread_cond_broadcast(&x->cv);}
    pthread_mutex_unlock(&x->mu);return n;
}
