/* Streamed lib/http exchanges: a libcurl transfer thread per exchange and a
 * thread per operation; source values stay on the owner. */
#include "native.h"
#include <curl/curl.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <ctype.h>
#include <pthread.h>
extern pthread_once_t gx_curl_once;
extern CURLcode gx_curl_init_status;
void gx_curl_initialize(void);
bool gx_http_transport_apply(CURL *easy,gx_http_transport *t);
#define BUFFER_LIMIT (1u<<20)
typedef struct gx_exchange {
 pthread_mutex_t mu;pthread_cond_t cv;int refs;
 char *method,*url;struct curl_slist *headers;int64_t length;gx_http_transport tr;
 bool stop,ended,done,head;const char *error;
 const uint8_t *chunk;size_t chunk_len,chunk_off;
 uint8_t *in;size_t in_len,in_off;
 char *hdr;size_t hdr_len,resp_start;long status;
 bool read_paused,write_paused;
} gx_exchange;
static void exchange_release(gx_exchange *x) {
 pthread_mutex_lock(&x->mu);int refs=--x->refs;pthread_mutex_unlock(&x->mu);if(refs)return;
 pthread_mutex_destroy(&x->mu);pthread_cond_destroy(&x->cv);free(x->method);free(x->url);curl_slist_free_all(x->headers);gx_http_options_free(&x->tr);free(x->in);free(x->hdr);free(x);
}
static void exchange_abort(gx_exchange *x) {pthread_mutex_lock(&x->mu);x->stop=true;pthread_cond_broadcast(&x->cv);pthread_mutex_unlock(&x->mu);}
static size_t on_read(char *dest,size_t a,size_t b,void *arg) {
 gx_exchange *x=arg;size_t cap=a*b;pthread_mutex_lock(&x->mu);size_t n=0;
 if(x->stop)n=CURL_READFUNC_ABORT;
 else if(x->chunk&&x->chunk_off<x->chunk_len){n=x->chunk_len-x->chunk_off;if(n>cap)n=cap;memcpy(dest,x->chunk+x->chunk_off,n);x->chunk_off+=n;if(x->chunk_off==x->chunk_len){x->chunk=NULL;pthread_cond_broadcast(&x->cv);}}
 else if(!x->ended){x->read_paused=true;n=CURL_READFUNC_PAUSE;}
 pthread_mutex_unlock(&x->mu);return n;
}
static size_t on_write(char *data,size_t a,size_t b,void *arg) {
 gx_exchange *x=arg;size_t n=a*b;pthread_mutex_lock(&x->mu);
 if(x->stop){pthread_mutex_unlock(&x->mu);return 0;}
 if(x->in_len-x->in_off>=BUFFER_LIMIT){x->write_paused=true;pthread_mutex_unlock(&x->mu);return CURL_WRITEFUNC_PAUSE;}
 if(x->in_off){memmove(x->in,x->in+x->in_off,x->in_len-x->in_off);x->in_len-=x->in_off;x->in_off=0;}
 uint8_t *p=realloc(x->in,x->in_len+n+1);if(!p){x->error="http: transport failure";pthread_mutex_unlock(&x->mu);return 0;}
 x->in=p;memcpy(p+x->in_len,data,n);x->in_len+=n;pthread_cond_broadcast(&x->cv);pthread_mutex_unlock(&x->mu);return n;
}
static size_t on_header(char *data,size_t a,size_t b,void *arg) {
 gx_exchange *x=arg;size_t n=a*b;pthread_mutex_lock(&x->mu);
 if(n>=5&&!memcmp(data,"HTTP/",5)){x->hdr_len=0;x->status=0;if(n>=12)for(size_t i=9;i<12;i++)x->status=x->status*10+(data[i]-'0');}
 if(n>65536-x->hdr_len){x->error="http: response headers exceed limit";pthread_mutex_unlock(&x->mu);return 0;}
 char *p=realloc(x->hdr,x->hdr_len+n+1);if(!p){x->error="http: transport failure";pthread_mutex_unlock(&x->mu);return 0;}
 x->hdr=p;memcpy(p+x->hdr_len,data,n);x->hdr_len+=n;
 if(((n==2&&data[0]=='\r')||(n==1&&data[0]=='\n'))&&!(x->status>=100&&x->status<200)){x->head=true;pthread_cond_broadcast(&x->cv);}
 pthread_mutex_unlock(&x->mu);return n;
}
static void *transfer(void *arg) {
 gx_exchange *x=arg;CURL *easy=NULL;CURLM *multi=NULL;bool added=false;const char *error=NULL;CURLcode code=CURLE_OK;
 pthread_once(&gx_curl_once,gx_curl_initialize);
 if(gx_curl_init_status!=CURLE_OK||!(easy=curl_easy_init())||!(multi=curl_multi_init()))error="http: transport failure";
 #define SET(o,v) do{if(!error&&curl_easy_setopt(easy,o,v)!=CURLE_OK)error="http: transport failure";}while(0)
 if(!error){
  SET(CURLOPT_URL,x->url);SET(CURLOPT_CUSTOMREQUEST,x->method);SET(CURLOPT_HTTPHEADER,x->headers);SET(CURLOPT_HTTP_CONTENT_DECODING,0L);
  SET(CURLOPT_FOLLOWLOCATION,0L);SET(CURLOPT_NOSIGNAL,1L);SET(CURLOPT_FORBID_REUSE,1L);SET(CURLOPT_PROTOCOLS,CURLPROTO_HTTP|CURLPROTO_HTTPS);
  SET(CURLOPT_WRITEFUNCTION,on_write);SET(CURLOPT_WRITEDATA,x);SET(CURLOPT_HEADERFUNCTION,on_header);SET(CURLOPT_HEADERDATA,x);
  if(!strcmp(x->method,"POST")){SET(CURLOPT_POST,1L);SET(CURLOPT_READFUNCTION,on_read);SET(CURLOPT_READDATA,x);if(x->length>=0)SET(CURLOPT_POSTFIELDSIZE_LARGE,(curl_off_t)x->length);}
  if(!error&&!gx_http_transport_apply(easy,&x->tr))error="http: invalid TLS option";
  if(!error&&curl_multi_add_handle(multi,easy)!=CURLM_OK)error="http: transport failure";else added=!error;
 }
 #undef SET
 int running=1;
 while(!error&&running){
  pthread_mutex_lock(&x->mu);
  bool stop=x->stop,resume=false;
  if(x->read_paused&&(x->chunk||x->ended)){x->read_paused=false;resume=true;}
  if(x->write_paused&&x->in_len-x->in_off<BUFFER_LIMIT){x->write_paused=false;resume=true;}
  pthread_mutex_unlock(&x->mu);
  if(stop){code=CURLE_ABORTED_BY_CALLBACK;break;}
  if(resume)curl_easy_pause(easy,CURLPAUSE_CONT);
  if(curl_multi_perform(multi,&running)!=CURLM_OK){error="http: transport failure";break;}
  if(running&&curl_multi_poll(multi,NULL,0,5,NULL)!=CURLM_OK){error="http: transport failure";break;}
 }
 if(!error&&code==CURLE_OK){int pending;CURLMsg *msg;while((msg=curl_multi_info_read(multi,&pending)))if(msg->msg==CURLMSG_DONE)code=msg->data.result;}
 if(added)curl_multi_remove_handle(multi,easy);if(easy)curl_easy_cleanup(easy);if(multi)curl_multi_cleanup(multi);
 pthread_mutex_lock(&x->mu);
 x->done=true;if(!x->error)x->error=error?error:code!=CURLE_OK?"http: transport failure":NULL;
 pthread_cond_broadcast(&x->cv);pthread_mutex_unlock(&x->mu);
 exchange_release(x);return NULL;
}

/* Owner registry: GC nodes reachable from a static root keep each context alive. */
typedef struct exchange_entry {uint64_t id,generation;gx_exchange *x;gx_V context;int64_t deadline,length,written;bool responded;struct exchange_entry *next;} exchange_entry;
static exchange_entry *entries;
static uint64_t next_exchange;
void gx_http_exchanges_release(uint64_t domain) {
 exchange_entry **at=&entries;
 while(*at){exchange_entry *e=*at;if(e->generation!=domain){at=&e->next;continue;}*at=e->next;exchange_abort(e->x);exchange_release(e->x);}
}
static exchange_entry *find_exchange(gx_V v) {
 if(v.t!=GX_INT)return NULL;
 for(exchange_entry *e=entries;e;e=e->next)if(e->id==(uint64_t)gx_i(v)&&e->generation==gx_domain())return e;
 return NULL;
}
static gx_V error_value(const char *message){return gx_std_errors_new(gx_cstr(message));}

void gx_lib_http_open(gx_Task *t,gx_V ctx,gx_V method,gx_V url,gx_V headers,gx_V length,gx_V timeout,gx_V options) {
 gx_V out[2]={gx_nil(),gx_nil()};const char *invalid="http: invalid request or limit";
 if(gx_is_nil(ctx)){out[1]=error_value(invalid);gx_set_rv(t,2,out);return;}
 gx_V err=gx_std_context_context_err(ctx);if(!gx_is_nil(err)){out[1]=err;gx_set_rv(t,2,out);return;}
 bool get=method.t==GX_STR&&method.l==3&&!memcmp(gx_sbytes(method),"GET",3),post=method.t==GX_STR&&method.l==4&&!memcmp(gx_sbytes(method),"POST",4);
 int64_t n=gx_i(length),ms=gx_i(timeout);
 bool bad=(!get&&!post)||n< -1||(get&&n!=0)||ms<1||ms>86400000||!gx_http_valid_url(url)||headers.t!=GX_SLICE||headers.l%2||headers.l>32768;
 size_t total=0;
 for(uint32_t i=0;!bad&&i<headers.l;i+=2){gx_V k=gx_vals(headers)[i],v=gx_vals(headers)[i+1];total+=(size_t)k.l+v.l+4;if(total>65536||!gx_http_header_name(k)){bad=true;break;}for(size_t j=0;j<v.l;j++){uint8_t c=gx_sbytes(v)[j];if(c==127||(c<32&&c!=9))bad=true;}
  static const char *forbidden[]={"host","content-length","transfer-encoding","connection","proxy-authorization","proxy-connection","upgrade","trailer","te"};
  for(size_t j=0;j<sizeof forbidden/sizeof *forbidden;j++)if(k.l==strlen(forbidden[j])&&!strncasecmp((const char *)gx_sbytes(k),forbidden[j],k.l))bad=true;}
 if(bad){out[1]=error_value(invalid);gx_set_rv(t,2,out);return;}
 gx_exchange *x=calloc(1,sizeof(*x));if(!x)gx_host_fault("HTTP exchange allocation");
 int status=gx_http_options(&x->tr,options);
 if(status){gx_http_options_free(&x->tr);free(x);out[1]=error_value(status==2?"http: invalid TLS option":invalid);gx_set_rv(t,2,out);return;}
 pthread_mutex_init(&x->mu,NULL);pthread_cond_init(&x->cv,NULL);x->refs=2;x->length=n;
 x->method=malloc(method.l+1);x->url=malloc(url.l+1);if(!x->method||!x->url)gx_host_fault("HTTP exchange allocation");
 memcpy(x->method,gx_sbytes(method),method.l);x->method[method.l]=0;memcpy(x->url,gx_sbytes(url),url.l);x->url[url.l]=0;
 for(uint32_t i=0;i<headers.l;i+=2){gx_V k=gx_vals(headers)[i],v=gx_vals(headers)[i+1];char *line=malloc((size_t)k.l+v.l+3);if(!line)gx_host_fault("HTTP header allocation");memcpy(line,gx_sbytes(k),k.l);if(v.l){line[k.l]=':';line[k.l+1]=' ';memcpy(line+k.l+2,gx_sbytes(v),v.l);line[k.l+v.l+2]=0;}else{line[k.l]=';';line[k.l+1]=0;}struct curl_slist *next=curl_slist_append(x->headers,line);free(line);if(!next)gx_host_fault("HTTP header allocation");x->headers=next;}
 const char *extra[]={"Expect:",n<0?"Transfer-Encoding: chunked":NULL};
 for(size_t i=0;i<2;i++)if(extra[i]){struct curl_slist *next=curl_slist_append(x->headers,extra[i]);if(!next)gx_host_fault("HTTP header allocation");x->headers=next;}
 pthread_t thread;if(pthread_create(&thread,NULL,transfer,x))gx_host_fault("HTTP exchange submission");pthread_detach(thread);
 gx_HostBoundary boundary=gx_host_boundary(ctx,ms*1000000);
 exchange_entry *e=GC_MALLOC(sizeof(*e));if(!e)gx_host_fault("HTTP exchange registry allocation");
 if(next_exchange==INT64_MAX)gx_host_fault("exchange identity exhausted");
 *e=(exchange_entry){++next_exchange,gx_domain(),x,ctx,boundary.has_deadline?boundary.deadline:INT64_MAX,n,0,false,entries};entries=e;
 gx_native_exchanges_release=gx_http_exchanges_release;gx_native_resources_track();
 out[0]=gx_int((int64_t)e->id);gx_set_rv(t,2,out);
}

/* One operation: the worker waits on the exchange, then publishes a wire of a
 * kind byte (0 result, 1 error message) and its payload. */
typedef struct exchange_op {gx_exchange *x;gx_HostToken token;pthread_t thread;bool started;int kind;size_t max;uint8_t *data;size_t len;} exchange_op;
enum {OP_WRITE,OP_RESPONSE,OP_READ};
static void *op_run(void *arg) {
 exchange_op *op=arg;gx_exchange *x=op->x;uint8_t *wire=NULL;size_t n=0;const char *error=NULL;
 pthread_mutex_lock(&x->mu);
 if(op->kind==OP_WRITE){
  x->chunk=op->data;x->chunk_len=op->len;x->chunk_off=0;pthread_cond_broadcast(&x->cv);
  while(x->chunk&&!x->stop&&!x->done)pthread_cond_wait(&x->cv,&x->mu);
  if(x->chunk){x->chunk=NULL;error=x->error?x->error:"http: transport failure";}
  else{wire=malloc(1);if(wire){wire[0]=0;n=1;}}
 }else if(op->kind==OP_RESPONSE){
  x->ended=true;pthread_cond_broadcast(&x->cv);
  while(!x->head&&!x->stop&&!x->done)pthread_cond_wait(&x->cv,&x->mu);
  if(!x->head||x->stop)error=x->error?x->error:"http: transport failure";
  else{n=9+x->hdr_len;wire=malloc(n);if(wire){wire[0]=0;uint64_t s=(uint64_t)x->status;for(int i=0;i<8;i++)wire[1+i]=(uint8_t)(s>>(8*i));memcpy(wire+9,x->hdr,x->hdr_len);}}
 }else{
  while(x->in_len==x->in_off&&!x->stop&&!x->done)pthread_cond_wait(&x->cv,&x->mu);
  if(x->stop||(x->in_len==x->in_off&&x->error))error=x->error?x->error:"http: transport failure";
  else{size_t k=x->in_len-x->in_off;if(k>op->max)k=op->max;n=1+k;wire=malloc(n);if(wire){wire[0]=k?0:2;memcpy(wire+1,x->in+x->in_off,k);x->in_off+=k;pthread_cond_broadcast(&x->cv);}}
 }
 pthread_mutex_unlock(&x->mu);
 if(error){n=1+strlen(error);wire=malloc(n);if(wire){wire[0]=1;memcpy(wire+1,error,n-1);}}
 if(!gx_host_publish(op->token,wire,wire?n:0,wire?NULL:"HTTP exchange publication allocation"))gx_host_publish_fault(op->token,"HTTP native publication failure");
 free(wire);gx_host_ack(op->token);gx_host_token_release(op->token);return NULL;
}
static const char *op_cancel(void *arg){exchange_abort(((exchange_op *)arg)->x);return NULL;}
static const char *op_cleanup(void *arg){exchange_op *op=arg;if(op->started&&pthread_join(op->thread,NULL))return "cannot join HTTP exchange worker";exchange_release(op->x);free(op->data);free(op);return NULL;}
static void op_zero(gx_Task *t,int kind,gx_V err){
 if(kind==OP_WRITE){gx_set_rv(t,1,&err);return;}
 if(kind==OP_RESPONSE){gx_V out[3]={gx_int(0),gx_nil_slice(),err};gx_set_rv(t,3,out);return;}
 gx_V out[3]={gx_nil_byte_slice(),gx_bool(false),err};gx_set_rv(t,3,out);
}
static bool header_skipped(const char *name,size_t n,const uint8_t *value,size_t vn){
 if(n==17&&!strncasecmp(name,"Transfer-Encoding",17))return true;
 if(n!=10||strncasecmp(name,"Connection",10))return false;
 for(size_t i=0;i+5<=vn;i++)if(!strncasecmp((const char *)value+i,"close",5))return true;
 return false;
}
typedef struct exchange_pair {gx_V name,value;size_t order;} exchange_pair;
static int pair_compare(const void *a,const void *b){const exchange_pair *x=a,*y=b;size_t n=x->name.l<y->name.l?x->name.l:y->name.l;int c=memcmp(gx_sbytes(x->name),gx_sbytes(y->name),n);if(c)return c;if(x->name.l!=y->name.l)return x->name.l<y->name.l?-1:1;return x->order<y->order?-1:x->order>y->order;}
static const char *op_decode(gx_Task *t,int kind,const uint8_t *wire,size_t n,gx_V canceled){
 if(!gx_is_nil(canceled)){op_zero(t,kind,canceled);return NULL;}
 if(n<1)return "HTTP exchange wire";
 if(wire[0]==1){op_zero(t,kind,gx_std_errors_new(gx_str((const char *)wire+1,n-1)));return NULL;}
 if(kind==OP_WRITE){gx_V nil=gx_nil();gx_set_rv(t,1,&nil);return NULL;}
 if(kind==OP_READ){
  if(wire[0]==2){gx_V out[3]={gx_nil_byte_slice(),gx_bool(true),gx_nil()};gx_set_rv(t,3,out);return NULL;}
  uint8_t *b=gx_alloc_bytes(n-1);memcpy(b,wire+1,n-1);gx_V out[3]={gx_byte_slice(b,(uint32_t)(n-1),(uint32_t)(n-1)),gx_bool(false),gx_nil()};gx_set_rv(t,3,out);return NULL;
 }
 if(n<9)return "HTTP exchange wire";
 uint64_t status=0;for(int i=7;i>=0;i--)status=status*256+wire[1+i];
 const uint8_t *h=wire+9;size_t hn=n-9,start=0,count=0;exchange_pair *pairs=GC_MALLOC((hn/4+1)*sizeof(*pairs));if(!pairs)gx_host_fault("HTTP header allocation");
 for(size_t i=0;i<hn;i++)if(h[i]=='\n'){
  size_t end=i;if(end>start&&h[end-1]=='\r')end--;const uint8_t *colon=memchr(h+start,':',end-start);
  if(colon){size_t k=(size_t)(colon-h),at=k+1;while(at<end&&(h[at]==' '||h[at]=='\t'))at++;size_t vend=end;while(vend>at&&(h[vend-1]==' '||h[vend-1]=='\t'))vend--;
   if(!header_skipped((const char *)h+start,k-start,h+at,vend-at)){uint8_t *name=gx_alloc_bytes(k-start);bool up=true;for(size_t j=start;j<k;j++){name[j-start]=(uint8_t)(up?toupper(h[j]):tolower(h[j]));up=h[j]=='-';}
    pairs[count]=(exchange_pair){gx_str((const char *)name,k-start),gx_str((const char *)h+at,vend-at),count};count++;}}
  start=i+1;
 }
 qsort(pairs,count,sizeof(*pairs),pair_compare);gx_V *values=gx_alloc_vals(count*2+1);for(size_t i=0;i<count;i++){values[2*i]=pairs[i].name;values[2*i+1]=pairs[i].value;}
 gx_V out[3]={gx_int((int64_t)status),gx_slice(values,(uint32_t)(count*2),(uint32_t)(count*2)),gx_nil()};gx_set_rv(t,3,out);return NULL;
}
static const char *decode_write(gx_Task *t,const uint8_t *w,size_t n,gx_V c,gx_V r){(void)r;return op_decode(t,OP_WRITE,w,n,c);}
static const char *decode_response(gx_Task *t,const uint8_t *w,size_t n,gx_V c,gx_V r){(void)r;return op_decode(t,OP_RESPONSE,w,n,c);}
static const char *decode_read(gx_Task *t,const uint8_t *w,size_t n,gx_V c,gx_V r){(void)r;return op_decode(t,OP_READ,w,n,c);}
/* Starts an operation; data is malloc-owned by the operation. */
static void gx_http_exchange_op(gx_Task *t,gx_V handle,int kind,size_t max,uint8_t *data,size_t len) {
 exchange_entry *e=find_exchange(handle);
 int64_t remaining=e->deadline==INT64_MAX?0:e->deadline-gx_now();
 if(e->deadline!=INT64_MAX&&remaining<=0){exchange_abort(e->x);free(data);op_zero(t,kind,gx_context_deadline_exceeded());return;}
 exchange_op *op=calloc(1,sizeof(*op));if(!op){free(data);gx_host_fault("HTTP exchange operation allocation");}
 op->x=e->x;op->kind=kind;op->max=max;op->data=data;op->len=len;
 pthread_mutex_lock(&e->x->mu);e->x->refs++;pthread_mutex_unlock(&e->x->mu);
 gx_HostBoundary boundary=gx_host_boundary(e->context,remaining);
 op->token=gx_host_register(t,boundary,e->context,kind==OP_WRITE?decode_write:kind==OP_RESPONSE?decode_response:decode_read,op_cancel,op_cleanup,op);
 gx_host_token_retain(op->token);
 if(!gx_host_should_submit(op->token)){gx_host_publish(op->token,NULL,0,NULL);gx_host_ack(op->token);gx_host_token_release(op->token);return;}
 if(pthread_create(&op->thread,NULL,op_run,op)){gx_host_publish(op->token,NULL,0,"HTTP exchange worker submission");gx_host_ack(op->token);gx_host_token_release(op->token);return;}
 op->started=true;
}
static const char *CLOSED="http: exchange closed",*STATE="http: invalid exchange operation",*INVALID="http: invalid request or limit";
void gx_http_exchange_write(gx_Task *t,gx_V handle,gx_V data) {
 exchange_entry *e=find_exchange(handle);
 if(gx_is_nil(handle)||data.t!=GX_SLICE||!gx_byte_backing(data)||data.l>(1u<<20)){op_zero(t,OP_WRITE,error_value(INVALID));return;}
 if(!e){op_zero(t,OP_WRITE,error_value(CLOSED));return;}
 if(e->responded||(e->length==0&&data.l)||(e->length>=0&&(int64_t)data.l>e->length-e->written)){op_zero(t,OP_WRITE,error_value(STATE));return;}
 e->written+=data.l;if(!data.l){gx_V nil=gx_nil();gx_set_rv(t,1,&nil);return;}
 uint8_t *copy=malloc(data.l);if(!copy)gx_host_fault("HTTP exchange chunk allocation");memcpy(copy,gx_bytes(data),data.l);
 gx_http_exchange_op(t,handle,OP_WRITE,0,copy,data.l);
}
void gx_http_exchange_response(gx_Task *t,gx_V handle) {
 exchange_entry *e=find_exchange(handle);
 if(gx_is_nil(handle)){op_zero(t,OP_RESPONSE,error_value(INVALID));return;}
 if(!e){op_zero(t,OP_RESPONSE,error_value(CLOSED));return;}
 e->responded=true;
 if(e->length>=0&&e->written!=e->length){exchange_abort(e->x);op_zero(t,OP_RESPONSE,error_value(STATE));return;}
 gx_http_exchange_op(t,handle,OP_RESPONSE,0,NULL,0);
}
void gx_http_exchange_read(gx_Task *t,gx_V handle,gx_V max) {
 exchange_entry *e=find_exchange(handle);int64_t m=gx_i(max);
 if(gx_is_nil(handle)||m<1||m>(1<<20)){op_zero(t,OP_READ,error_value(INVALID));return;}
 if(!e){op_zero(t,OP_READ,error_value(CLOSED));return;}
 if(!e->responded){op_zero(t,OP_READ,error_value(STATE));return;}
 gx_http_exchange_op(t,handle,OP_READ,(size_t)m,NULL,0);
}

void gx_lib_http_exchange_close(gx_Task *t,gx_V handle) {
 gx_set_rv(t,0,NULL);if(gx_is_nil(handle)||handle.t!=GX_INT)return;
 for(exchange_entry **at=&entries;*at;at=&(*at)->next){exchange_entry *e=*at;if(e->id==(uint64_t)gx_i(handle)&&e->generation==gx_domain()){*at=e->next;exchange_abort(e->x);exchange_release(e->x);return;}}
}
