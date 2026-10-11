/* libcurl worker owns all requests/transport/body data. ACK follows resource release. */
#include "native.h"
#include <curl/curl.h>
#include <stdlib.h>
#include <stdio.h>
#include <ctype.h>
#include <strings.h>
#include <limits.h>
#include <zlib.h>
#include <openssl/err.h>
#include <openssl/pem.h>
#include <openssl/ssl.h>
#include <openssl/x509.h>
typedef struct http_work {
 gx_HostToken token;pthread_t thread;bool started;atomic_bool canceled;
 char *method,*url;struct curl_slist *headers;uint8_t *input;size_t input_len,max;
 int64_t timeout_ms;uint8_t *body,*header_data;size_t body_len,header_len;
 const char *error;uint64_t status;size_t response_start;bool automatic_gzip,decoded;
 z_stream z;bool z_active,z_done;
 gx_http_transport tr;
} http_work;
pthread_once_t gx_curl_once=PTHREAD_ONCE_INIT;
CURLcode gx_curl_init_status;
void gx_curl_initialize(void) {gx_curl_init_status=curl_global_init(CURL_GLOBAL_DEFAULT);}
static void http_zero(gx_Task *t,gx_V err) {gx_V out[4]={gx_int(0),gx_nil_slice(),gx_nil_byte_slice(),err};gx_set_rv(t,4,out);}
static void *native_alloc(size_t n) {return malloc(n?n:1);}
static char *text_snapshot(gx_V v) {char *p=native_alloc((size_t)v.l+1);if(!p)return NULL;if(v.l)memcpy(p,gx_sbytes(v),v.l);p[v.l]=0;return p;}
static bool body_append(http_work *w,const uint8_t *data,size_t n) {
 if(n>w->max-w->body_len){w->error="http: response body exceeds limit";return false;}
 uint8_t *p=realloc(w->body,w->body_len+n+1);if(!p){w->error="host_fault: HTTP body allocation";return false;}w->body=p;memcpy(p+w->body_len,data,n);w->body_len+=n;return true;
}
static void gzip_end(http_work *w) {if(w->z_active){inflateEnd(&w->z);w->z_active=false;}}
/* Decoding happens here rather than in libcurl: whether CURLOPT_HTTP_CONTENT_DECODING
 * set from the header callback takes effect differs between libcurl 7.x and 8.x. */
static bool gzip_append(http_work *w,const uint8_t *data,size_t n) {
 if(!w->z_active){memset(&w->z,0,sizeof w->z);if(inflateInit2(&w->z,16+MAX_WBITS)!=Z_OK){w->error="host_fault: HTTP gzip initialization";return false;}w->z_active=true;w->z_done=false;}
 uint8_t out[16384];w->z.next_in=(Bytef *)data;w->z.avail_in=(uInt)n;
 do{
  if(w->z_done){if(!w->z.avail_in)break;if(inflateReset(&w->z)!=Z_OK){w->error="http: transport failure";return false;}w->z_done=false;}
  w->z.next_out=out;w->z.avail_out=sizeof out;int r=inflate(&w->z,Z_NO_FLUSH);size_t got=sizeof out-w->z.avail_out;
  if(got&&!body_append(w,out,got))return false;
  if(r==Z_STREAM_END)w->z_done=true;else if(r==Z_BUF_ERROR&&!got)break;else if(r!=Z_OK&&r!=Z_BUF_ERROR){w->error="http: transport failure";return false;}
 }while(w->z.avail_in||!w->z.avail_out);
 return true;
}
static size_t body_write(char *data,size_t a,size_t b,void *arg) {
 http_work *w=arg;if(a&&b>SIZE_MAX/a){w->error="http: response body exceeds limit";return 0;}size_t n=a*b;
 if(n>UINT_MAX){w->error="http: transport failure";return 0;}
 return (w->decoded?gzip_append(w,(const uint8_t *)data,n):body_append(w,(const uint8_t *)data,n))?n:0;
}
static size_t header_write(char *data,size_t a,size_t b,void *arg) {
 http_work *w=arg;if(a&&b>SIZE_MAX/a)return 0;size_t n=a*b;
 if(n>65536-w->header_len){w->error="http: response headers exceed limit";return 0;}
 uint8_t *p=realloc(w->header_data,w->header_len+n+1);if(!p){w->error="host_fault: HTTP header allocation";return 0;}w->header_data=p;
 if(n>=5&&!memcmp(data,"HTTP/",5)){w->response_start=w->header_len;w->decoded=false;gzip_end(w);}
 memcpy(p+w->header_len,data,n);w->header_len+=n;
 if((n==2&&data[0]=='\r'&&data[1]=='\n')||(n==1&&data[0]=='\n')){
  /* Go's automatic mode decodes gzip only; explicit Accept-Encoding and Range
   * leave the wire body intact. */
  bool gzip=false,encoding_seen=false,empty=false,length_seen=false;size_t start=w->response_start;
  int status=0;if(w->header_len-start>=12)for(size_t i=start+9;i<start+12;i++)status=status*10+(p[i]-'0');
  for(size_t i=start;i<w->header_len;i++)if(p[i]=='\n'){
   size_t end=i;if(end>start&&p[end-1]=='\r')end--;
   bool ce=!encoding_seen&&end-start>=17&&!strncasecmp((const char *)p+start,"Content-Encoding:",17);
   bool cl=!length_seen&&end-start>=15&&!strncasecmp((const char *)p+start,"Content-Length:",15);
   if(ce||cl){
    size_t v=start+(ce?17:15);while(v<end&&(p[v]==' '||p[v]=='\t'))v++;while(end>v&&(p[end-1]==' '||p[end-1]=='\t'))end--;
    if(ce){encoding_seen=true;gzip=end-v==4&&!strncasecmp((const char *)p+v,"gzip",4);}
    else{length_seen=true;empty=end-v==1&&p[v]=='0';}
   }start=i+1;
  }
  w->decoded=w->automatic_gzip&&gzip&&!empty&&status!=204&&status!=304&&(status<100||status>=200);
 }
 return n;
}
static int progress(void *arg,curl_off_t a,curl_off_t b,curl_off_t c,curl_off_t d) {(void)a;(void)b;(void)c;(void)d;return atomic_load(&((http_work *)arg)->canceled)?1:0;}
static void write64(uint8_t *p,uint64_t n) {for(int i=0;i<8;i++){p[i]=(uint8_t)n;n>>=8;}}
static uint64_t read64(const uint8_t *p) {uint64_t n=0;for(int i=7;i>=0;i--)n=n*256+p[i];return n;}
/* Adds root-cas to the store curl loaded from the system bundle. */
static CURLcode http_ssl_ctx(CURL *curl,void *ssl_ctx,void *arg) {
 (void)curl;gx_http_transport *t=arg;BIO *bio=BIO_new_mem_buf(t->root_cas,(int)t->root_cas_len);if(!bio)return CURLE_OUT_OF_MEMORY;
 X509_STORE *store=SSL_CTX_get_cert_store((SSL_CTX *)ssl_ctx);X509 *cert;CURLcode code=CURLE_OK;
 while((cert=PEM_read_bio_X509(bio,NULL,NULL,NULL))){if(!X509_STORE_add_cert(store,cert)){unsigned long e=ERR_peek_last_error();if(ERR_GET_REASON(e)!=X509_R_CERT_ALREADY_IN_HASH_TABLE)code=CURLE_SSL_CACERT_BADFILE;}X509_free(cert);}
 ERR_clear_error();BIO_free(bio);return code;
}
static int http_pem_certs(const char *data,size_t n,X509 **first) {
 BIO *bio=BIO_new_mem_buf(data,(int)n);if(!bio)gx_host_fault("HTTP TLS option allocation");int count=0;X509 *cert;
 while((cert=PEM_read_bio_X509(bio,NULL,NULL,NULL))){if(first&&!count)*first=cert;else X509_free(cert);count++;}
 ERR_clear_error();BIO_free(bio);return count;
}
static bool http_pair_valid(const char *cert,size_t cert_len,const char *key,size_t key_len) {
 X509 *leaf=NULL;if(!http_pem_certs(cert,cert_len,&leaf))return false;BIO *bio=BIO_new_mem_buf(key,(int)key_len);if(!bio){X509_free(leaf);gx_host_fault("HTTP TLS option allocation");}
 EVP_PKEY *pkey=PEM_read_bio_PrivateKey(bio,NULL,NULL,NULL);bool ok=pkey&&X509_check_private_key(leaf,pkey)==1;EVP_PKEY_free(pkey);X509_free(leaf);BIO_free(bio);ERR_clear_error();return ok;
}
static char *http_option_copy(gx_V v) {char *p=native_alloc((size_t)v.l+1);if(!p)gx_host_fault("HTTP TLS option allocation");if(v.l)memcpy(p,gx_sbytes(v),v.l);p[v.l]=0;return p;}
/* 0 valid, 1 invalid request, 2 invalid TLS option. Copies values into w. */
int gx_http_options(gx_http_transport *t,gx_V options) {
 if(options.t!=GX_SLICE||options.l%2)return 1;if(!options.l)return 0;
 static const char *names[]={"client-cert","client-key","root-cas","insecure-skip-verify","unix-socket"};gx_V values[5];bool seen[5]={false,false,false,false,false};size_t total=0;
 for(uint32_t i=0;i<options.l;i+=2){gx_V n=gx_vals(options)[i],v=gx_vals(options)[i+1];if(n.t!=GX_STR||v.t!=GX_STR)return 1;total+=(size_t)n.l+v.l;if(total>(1u<<20))return 1;int k=-1;for(int j=0;j<5;j++)if(n.l==strlen(names[j])&&!memcmp(gx_sbytes(n),names[j],n.l))k=j;if(k<0||seen[k])return 1;seen[k]=true;values[k]=v;}
 if(seen[0]!=seen[1])return 1;
 if(seen[4]){gx_V v=values[4];if(!v.l||v.l>4096||memchr(gx_sbytes(v),0,v.l))return 1;t->unix_socket=http_option_copy(v);}
 if(seen[3]){gx_V v=values[3];bool yes=v.l==4&&!memcmp(gx_sbytes(v),"true",4),no=v.l==5&&!memcmp(gx_sbytes(v),"false",5);if(!yes&&!no)return 1;t->insecure=yes;}
 if(seen[2]){if(!values[2].l||!http_pem_certs((const char *)gx_sbytes(values[2]),values[2].l,NULL))return 2;t->root_cas=http_option_copy(values[2]);t->root_cas_len=values[2].l;}
 if(seen[0]){if(!values[0].l||!values[1].l||!http_pair_valid((const char *)gx_sbytes(values[0]),values[0].l,(const char *)gx_sbytes(values[1]),values[1].l))return 2;t->client_cert=http_option_copy(values[0]);t->client_cert_len=values[0].l;t->client_key=http_option_copy(values[1]);t->client_key_len=values[1].l;}
 return 0;
}
void gx_http_options_free(gx_http_transport *t) {
 free(t->client_cert);t->client_cert=NULL;if(t->client_key){OPENSSL_cleanse(t->client_key,t->client_key_len);free(t->client_key);t->client_key=NULL;}free(t->root_cas);t->root_cas=NULL;free(t->unix_socket);t->unix_socket=NULL;
}
/* Applies verification and DoWith options; false when libcurl rejects one. */
bool gx_http_transport_apply(CURL *easy,gx_http_transport *t) {
 bool ok=curl_easy_setopt(easy,CURLOPT_SSL_VERIFYPEER,t->insecure?0L:1L)==CURLE_OK&&curl_easy_setopt(easy,CURLOPT_SSL_VERIFYHOST,t->insecure?0L:2L)==CURLE_OK;
 if(ok&&t->client_cert){struct curl_blob cert={t->client_cert,t->client_cert_len,CURL_BLOB_COPY},key={t->client_key,t->client_key_len,CURL_BLOB_COPY};ok=curl_easy_setopt(easy,CURLOPT_SSLCERT_BLOB,&cert)==CURLE_OK&&curl_easy_setopt(easy,CURLOPT_SSLCERTTYPE,"PEM")==CURLE_OK&&curl_easy_setopt(easy,CURLOPT_SSLKEY_BLOB,&key)==CURLE_OK&&curl_easy_setopt(easy,CURLOPT_SSLKEYTYPE,"PEM")==CURLE_OK;}
 if(ok&&t->root_cas)ok=curl_easy_setopt(easy,CURLOPT_SSL_CTX_FUNCTION,http_ssl_ctx)==CURLE_OK&&curl_easy_setopt(easy,CURLOPT_SSL_CTX_DATA,t)==CURLE_OK;
 if(ok&&t->unix_socket)ok=curl_easy_setopt(easy,CURLOPT_UNIX_SOCKET_PATH,t->unix_socket)==CURLE_OK&&curl_easy_setopt(easy,CURLOPT_NOPROXY,"*")==CURLE_OK;
 return ok;
}
static void *http_thread(void *arg) {
 http_work *w=arg;CURL *easy=NULL;CURLM *multi=NULL;CURLcode code=CURLE_OK;bool acquisition=false;
 pthread_once(&gx_curl_once,gx_curl_initialize);
 if(gx_curl_init_status!=CURLE_OK){w->error="host_fault: libcurl initialization";goto release;}
 easy=curl_easy_init();multi=curl_multi_init();if(!easy||!multi){w->error="host_fault: libcurl acquisition";goto release;}
 #define SET(option,value) do{if(curl_easy_setopt(easy,option,value)!=CURLE_OK){w->error="host_fault: libcurl option";goto release;}}while(0)
 SET(CURLOPT_URL,w->url);SET(CURLOPT_CUSTOMREQUEST,w->method);SET(CURLOPT_HTTPHEADER,w->headers);
 SET(CURLOPT_HTTP_CONTENT_DECODING,0L);if(w->automatic_gzip)SET(CURLOPT_ACCEPT_ENCODING,"gzip");
 SET(CURLOPT_FOLLOWLOCATION,0L);SET(CURLOPT_NOSIGNAL,1L);
 if(!gx_http_transport_apply(easy,&w->tr)){w->error="host_fault: libcurl option";goto release;}
 SET(CURLOPT_PROTOCOLS,CURLPROTO_HTTP|CURLPROTO_HTTPS);SET(CURLOPT_REDIR_PROTOCOLS,CURLPROTO_HTTP|CURLPROTO_HTTPS);
 SET(CURLOPT_TIMEOUT_MS,(long)w->timeout_ms);SET(CURLOPT_CONNECTTIMEOUT_MS,(long)w->timeout_ms);SET(CURLOPT_FORBID_REUSE,1L);
 SET(CURLOPT_WRITEFUNCTION,body_write);SET(CURLOPT_WRITEDATA,w);SET(CURLOPT_HEADERFUNCTION,header_write);SET(CURLOPT_HEADERDATA,w);
 SET(CURLOPT_NOPROGRESS,0L);SET(CURLOPT_XFERINFOFUNCTION,progress);SET(CURLOPT_XFERINFODATA,w);
 if(!strcmp(w->method,"POST")){SET(CURLOPT_POSTFIELDS,w->input);SET(CURLOPT_POSTFIELDSIZE_LARGE,(curl_off_t)w->input_len);}
 if(curl_multi_add_handle(multi,easy)!=CURLM_OK){w->error="host_fault: libcurl submission";goto release;}acquisition=true;
 int running=0;
 do{
  if(atomic_load(&w->canceled)){code=CURLE_ABORTED_BY_CALLBACK;break;}
  if(curl_multi_perform(multi,&running)!=CURLM_OK){w->error="host_fault: libcurl dispatch";break;}
  if(running&&curl_multi_poll(multi,NULL,0,10,NULL)!=CURLM_OK){w->error="host_fault: libcurl poll";break;}
 }while(running);
 if(code==CURLE_OK&&!w->error){int pending;CURLMsg *msg;bool found=false;while((msg=curl_multi_info_read(multi,&pending)))if(msg->msg==CURLMSG_DONE){code=msg->data.result;found=true;}if(!found)w->error="host_fault: libcurl missing terminal result";}
 if(code!=CURLE_OK&&!w->error)w->error=code==CURLE_OUT_OF_MEMORY?"host_fault: HTTP transport allocation":code==CURLE_ABORTED_BY_CALLBACK?"http: canceled":"http: transport failure";
 if(!w->error&&w->decoded&&w->z_active&&!w->z_done)w->error="http: transport failure";
 long status=0;if(!w->error&&curl_easy_getinfo(easy,CURLINFO_RESPONSE_CODE,&status)!=CURLE_OK)w->error="host_fault: HTTP response status";w->status=(uint64_t)status;
release:
 gzip_end(w);
 if(acquisition)curl_multi_remove_handle(multi,easy);if(easy)curl_easy_cleanup(easy);if(multi)curl_multi_cleanup(multi);
 curl_slist_free_all(w->headers);w->headers=NULL;free(w->method);w->method=NULL;free(w->url);w->url=NULL;free(w->input);w->input=NULL;gx_http_options_free(&w->tr);
 size_t errlen=w->error?strlen(w->error):0;
 size_t n=40+w->header_len+w->body_len+errlen;uint8_t *wire=malloc(n);
 if(wire){write64(wire,w->status);write64(wire+8,w->header_len);write64(wire+16,w->body_len);write64(wire+24,errlen);write64(wire+32,w->decoded?1:0);if(w->header_len)memcpy(wire+40,w->header_data,w->header_len);if(w->body_len)memcpy(wire+40+w->header_len,w->body,w->body_len);if(errlen)memcpy(wire+40+w->header_len+w->body_len,w->error,errlen);}
 free(w->header_data);w->header_data=NULL;free(w->body);w->body=NULL;
 const char *fault=!wire?"HTTP publication allocation":w->error&&!strncmp(w->error,"host_fault:",11)?w->error:NULL;
 /* publish snapshots wire; all actual native transport/input/body acquisitions
  * are released before ACK, registration holder remains until joined cleanup. */
 if(!gx_host_publish(w->token,wire,wire?n:0,fault))gx_host_publish_fault(w->token,"HTTP native publication failure");free(wire);gx_host_ack(w->token);gx_host_token_release(w->token);return NULL;
 #undef SET
}
static const char *http_cancel(void *arg){atomic_store(&((http_work *)arg)->canceled,true);return NULL;}
static const char *http_cleanup(void *arg){http_work *w=arg;if(w->started&&pthread_join(w->thread,NULL))return "cannot join HTTP worker";free(w->method);free(w->url);free(w->input);curl_slist_free_all(w->headers);free(w->body);free(w->header_data);gx_http_options_free(&w->tr);free(w);return NULL;}
typedef struct http_pair {gx_V name,value;size_t order;} http_pair;
static int header_compare(const void *a,const void *b){const http_pair *x=a,*y=b;size_t n=x->name.l<y->name.l?x->name.l:y->name.l;int c=memcmp(gx_sbytes(x->name),gx_sbytes(y->name),n);if(c)return c;if(x->name.l!=y->name.l)return x->name.l<y->name.l?-1:1;return x->order<y->order?-1:x->order>y->order;}
static const char *http_decode(gx_Task *t,const uint8_t *wire,size_t n,gx_V canceled,gx_V roots) {
 (void)roots;if(!gx_is_nil(canceled)){http_zero(t,canceled);return NULL;}
 if(n<40)return "HTTP wire length";uint64_t status=read64(wire),h=read64(wire+8),b=read64(wire+16),e=read64(wire+24),decoded=read64(wire+32);
 if(h>65536||b>GX_NATIVE_MAX||e>65536||h+b+e!=n-40||status>999||decoded>1)return "HTTP wire bounds";
 if(e){http_zero(t,gx_std_errors_new(gx_str((const char *)wire+40+h+b,(size_t)e)));return NULL;}
 const uint8_t *headers=wire+40;gx_V *values=gx_alloc_vals(32768);uint32_t count=0;size_t start=0;
 for(size_t i=0;i<h;i++)if(headers[i]=='\n'){
  size_t end=i;if(end>start&&headers[end-1]=='\r')end--;const uint8_t *colon=memchr(headers+start,':',end-start);
  if(colon){if(count>=32766)return "HTTP header count";size_t k=(size_t)(colon-headers);size_t at=k+1;while(at<end&&(headers[at]==' '||headers[at]=='\t'))at++;uint8_t *name=gx_alloc_bytes(k-start);bool first=true;for(size_t j=start;j<k;j++){name[j-start]=(uint8_t)(first?toupper(headers[j]):tolower(headers[j]));first=headers[j]=='-';}values[count++]=gx_str((const char *)name,k-start);values[count++]=gx_str((const char *)headers+at,end-at);
  }else if(end-start>=5&&!memcmp(headers+start,"HTTP/",5))count=0;
  start=i+1;
 }
 http_pair *pairs=GC_MALLOC((count/2?count/2:1)*sizeof(*pairs));if(!pairs)gx_host_fault("HTTP header sorting allocation");size_t pair_count=0;
 for(size_t i=0;i<count;i+=2){gx_V name=values[i];if(decoded&&((name.l==16&&!memcmp(gx_sbytes(name),"Content-Encoding",16))||(name.l==14&&!memcmp(gx_sbytes(name),"Content-Length",14))))continue;pairs[pair_count++]=(http_pair){name,values[i+1],i};}
 qsort(pairs,pair_count,sizeof(*pairs),header_compare);count=0;for(size_t i=0;i<pair_count;i++){values[count++]=pairs[i].name;values[count++]=pairs[i].value;}
 uint8_t *body=gx_alloc_bytes((size_t)b);if(b)memcpy(body,wire+40+h,(size_t)b);
 gx_V out[4]={gx_int((int64_t)status),gx_slice(values,count,count),gx_byte_slice(body,(uint32_t)b,(uint32_t)b),gx_nil()};gx_set_rv(t,4,out);return NULL;
}
bool gx_http_header_name(gx_V v){if(!v.l)return false;for(size_t i=0;i<v.l;i++){unsigned char c=gx_sbytes(v)[i];if(!c||!(((c>='0'&&c<='9')||(c>='A'&&c<='Z')||(c>='a'&&c<='z'))||strchr("!#$%&'*+-.^_`|~",c)))return false;}return true;}
bool gx_http_valid_url(gx_V v) {
 if(v.t!=GX_STR||!v.l||v.l>8192)return false;for(size_t i=0;i<v.l;i++){uint8_t c=gx_sbytes(v)[i];if(c<=32||c==127||c=='\\'||c==0)return false;if(i+2<v.l&&c==0xef&&gx_sbytes(v)[i+1]==0xbb&&gx_sbytes(v)[i+2]==0xbf)return false;}
 char *url=text_snapshot(v);if(!url)gx_host_fault("HTTP URL allocation");CURLU *u=curl_url();if(!u){free(url);gx_host_fault("HTTP URL parser allocation");}bool ok=u&&curl_url_set(u,CURLUPART_URL,url,0)==CURLUE_OK;char *scheme=NULL,*host=NULL,*user=NULL,*password=NULL,*fragment=NULL,*port=NULL;
 if(ok){ok=curl_url_get(u,CURLUPART_SCHEME,&scheme,0)==CURLUE_OK&&(!strcmp(scheme,"http")||!strcmp(scheme,"https"))&&curl_url_get(u,CURLUPART_HOST,&host,0)==CURLUE_OK&&*host;
 if(curl_url_get(u,CURLUPART_USER,&user,0)==CURLUE_OK||curl_url_get(u,CURLUPART_PASSWORD,&password,0)==CURLUE_OK||curl_url_get(u,CURLUPART_FRAGMENT,&fragment,0)==CURLUE_OK)ok=false;
 if(curl_url_get(u,CURLUPART_PORT,&port,0)==CURLUE_OK&&!strcmp(port,"0"))ok=false;}
 curl_free(scheme);curl_free(host);curl_free(user);curl_free(password);curl_free(fragment);curl_free(port);if(u)curl_url_cleanup(u);free(url);return ok;
}
void gx_lib_http_do(gx_Task *t,gx_V ctx,gx_V method,gx_V url,gx_V headers,gx_V input,gx_V max,gx_V timeout) {
 gx_http_do(t,ctx,method,url,headers,input,max,timeout,gx_nil_slice());
}
void gx_http_do(gx_Task *t,gx_V ctx,gx_V method,gx_V url,gx_V headers,gx_V input,gx_V max,gx_V timeout,gx_V options) {
 gx_V invalid=gx_std_errors_new(gx_cstr("http: invalid request or limit"));
 if(gx_is_nil(ctx)){http_zero(t,invalid);return;}gx_HostBoundary boundary=gx_host_boundary(ctx,gx_i(timeout)>0&&gx_i(timeout)<=300000?gx_i(timeout)*1000000:0);
 gx_V error=gx_std_context_context_err(ctx);if(!gx_is_nil(error)){http_zero(t,error);return;}
 bool get=method.t==GX_STR&&method.l==3&&!memcmp(gx_sbytes(method),"GET",3),post=method.t==GX_STR&&method.l==4&&!memcmp(gx_sbytes(method),"POST",4);
 if((!get&&!post)||!gx_http_valid_url(url)||input.t!=GX_SLICE||!gx_byte_backing(input)||input.l>GX_NATIVE_MAX||get&&input.l||gx_i(max)<0||gx_i(max)>GX_NATIVE_MAX||gx_i(timeout)<1||gx_i(timeout)>300000||headers.t!=GX_SLICE||headers.l%2||headers.l>32768){http_zero(t,invalid);return;}
 size_t total=0;for(uint32_t i=0;i<headers.l;i+=2){gx_V n=gx_vals(headers)[i],v=gx_vals(headers)[i+1];total+=(size_t)n.l+v.l+4;if(total>65536||!gx_http_header_name(n)){http_zero(t,invalid);return;}for(size_t j=0;j<v.l;j++){uint8_t c=gx_sbytes(v)[j];if(c==127||c<32&&c!=9){http_zero(t,invalid);return;}}char *name=text_snapshot(n);if(!name)gx_host_fault("HTTP validation allocation");const char *forbidden[]={"host","content-length","transfer-encoding","connection","proxy-authorization","proxy-connection","upgrade","trailer","te"};bool bad=false;for(size_t j=0;j<sizeof forbidden/sizeof *forbidden;j++)if(!strcasecmp(name,forbidden[j]))bad=true;free(name);if(bad){http_zero(t,invalid);return;}}
 http_work *w=calloc(1,sizeof(*w));if(!w)gx_host_fault("HTTP work allocation");atomic_init(&w->canceled,false);
 int option_status=gx_http_options(&w->tr,options);if(option_status){gx_http_options_free(&w->tr);free(w);http_zero(t,option_status==2?gx_std_errors_new(gx_cstr("http: invalid TLS option")):invalid);return;}
 bool encoding_seen=false,range_seen=false;w->automatic_gzip=true;for(uint32_t i=0;i<headers.l;i+=2){gx_V name=gx_vals(headers)[i],value=gx_vals(headers)[i+1];if(name.l==15&&!strncasecmp((const char *)gx_sbytes(name),"Accept-Encoding",15)&&!encoding_seen){encoding_seen=true;if(value.l)w->automatic_gzip=false;}if(name.l==5&&!strncasecmp((const char *)gx_sbytes(name),"Range",5)&&!range_seen){range_seen=true;if(value.l)w->automatic_gzip=false;}}
 w->method=text_snapshot(method);w->url=text_snapshot(url);w->input=native_alloc(input.l);if(!w->method||!w->url||!w->input){http_cleanup(w);gx_host_fault("HTTP snapshot allocation");}if(input.l)memcpy(w->input,gx_bytes(input),input.l);w->input_len=input.l;w->max=(size_t)gx_i(max);
 int64_t remain=boundary.deadline-gx_now();w->timeout_ms=remain>0?(remain+999999)/1000000:1;
 for(uint32_t i=0;i<headers.l;i+=2){gx_V n=gx_vals(headers)[i],v=gx_vals(headers)[i+1];if(w->automatic_gzip&&n.l==15&&!strncasecmp((const char *)gx_sbytes(n),"Accept-Encoding",15))continue;char *line=native_alloc((size_t)n.l+v.l+3);if(!line){http_cleanup(w);gx_host_fault("HTTP header snapshot allocation");}memcpy(line,gx_sbytes(n),n.l);if(v.l){line[n.l]=':';line[n.l+1]=' ';memcpy(line+n.l+2,gx_sbytes(v),v.l);line[n.l+v.l+2]=0;}else{line[n.l]=';';line[n.l+1]=0;}struct curl_slist *next=curl_slist_append(w->headers,line);free(line);if(!next){http_cleanup(w);gx_host_fault("HTTP header allocation");}w->headers=next;}
 w->token=gx_host_register(t,boundary,ctx,http_decode,http_cancel,http_cleanup,w);
 gx_host_token_retain(w->token);
 if(!gx_host_should_submit(w->token)){free(w->method);w->method=NULL;free(w->url);w->url=NULL;free(w->input);w->input=NULL;curl_slist_free_all(w->headers);w->headers=NULL;gx_http_options_free(&w->tr);gx_host_publish(w->token,NULL,0,NULL);gx_host_ack(w->token);gx_host_token_release(w->token);return;}
 if(pthread_create(&w->thread,NULL,http_thread,w)){free(w->method);w->method=NULL;free(w->url);w->url=NULL;free(w->input);w->input=NULL;curl_slist_free_all(w->headers);w->headers=NULL;gx_http_options_free(&w->tr);gx_host_publish(w->token,NULL,0,"HTTP worker submission");gx_host_ack(w->token);gx_host_token_release(w->token);return;}
 w->started=true;
}
