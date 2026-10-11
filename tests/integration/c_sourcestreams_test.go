package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
)

func TestGeneratedCSourceStreams(t *testing.T) {
	res := buildSourceStreams(t)
	out, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("c", res, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	runTool(t, out, "sh", "build.sh")
	for _, name := range []string{"goalchemy.h", "library.h"} {
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(consumer, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{"consumer.c": cSourceStreamsConsumer, "build.sh": cPackageConsumerBuild} {
		if err := os.WriteFile(filepath.Join(consumer, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runTool(t, consumer, "sh", "build.sh", filepath.Join(out, "libgoalchemy.a"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(consumer, "consumer"))
	cmd.Dir, cmd.Env = consumer, append(driver.ToolEnv(), fmt.Sprintf("THROUGHPUT_MIB=%d", throughputMiB(256)))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("consumer: %v\n%s", err, output)
	}
	t.Logf("%s", output)
}

const cSourceStreamsConsumer = `#define _POSIX_C_SOURCE 200809L
#include "goalchemy.h"
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#define CHECK(ok,msg) do{if(!(ok)){fprintf(stderr,"FAIL %s (line %d)\n",msg,__LINE__);exit(1);}}while(0)
static gxc_value num(int64_t n){return (gxc_value){.kind=GXC_INT,.integer=n};}
static gxc_value hv(gxc_handle *h){return (gxc_value){.kind=GXC_HANDLE,.handle=h};}
static int call(const char *name,gxc_value *args,size_t n,gxc_value *out){gxc_error e;int k=goalchemy_invoke(name,args,n,NULL,out,&e);gxc_error_free(&e);return k;}
static gxc_handle *open_handle(const char *name,gxc_value *args,size_t n,const char *type){gxc_value r;CHECK(call(name,args,n,&r)==0&&r.kind==GXC_HANDLE&&!strcmp(gxc_handle_type(r.handle),type),name);return r.handle;}
static uint8_t *pattern(size_t n,size_t from){uint8_t *b=malloc(n?n:1);for(size_t i=0;i<n;i++)b[i]=(uint8_t)((from+i)%251);return b;}
static int rd(gxc_handle *h,uint8_t *b,size_t n,size_t *k){gxc_error e;int rc=gxc_stream_read(h,b,n,k,&e);gxc_error_free(&e);return rc;}
static uint8_t *read_all(gxc_handle *h,size_t *len){size_t cap=1<<16,n=0,k;uint8_t *b=malloc(cap);for(;;){if(cap-n<(1u<<20)){cap=cap*2+(1u<<20);b=realloc(b,cap);}CHECK(rd(h,b+n,1u<<20,&k)==0,"read");if(!k)break;n+=k;}*len=n;return b;}
static int closeh(gxc_handle *h){gxc_error e;int rc=gxc_handle_close(h,&e);gxc_error_free(&e);return rc;}
typedef struct {size_t left,at;uint8_t *block;} source;
static int64_t source_read(void *s,uint8_t *b,size_t n,char *m){(void)m;source *x=s;size_t k=n<x->left?n:x->left;if(k>(128u<<10))k=128u<<10;memcpy(b,x->block+x->at%251,k);x->left-=k;x->at+=k;return (int64_t)k;}
typedef struct {uint8_t *data;size_t len,pos;} memory;
static int64_t memory_read(void *s,uint8_t *b,size_t n,char *m){(void)m;memory *x=s;size_t k=x->len-x->pos<n?x->len-x->pos:n;memcpy(b,x->data+x->pos,k);x->pos+=k;return (int64_t)k;}
static gxc_handle *shared;static size_t total;static pthread_mutex_t total_lock=PTHREAD_MUTEX_INITIALIZER;
static void *reader(void *arg){(void)arg;uint8_t buf[4096];for(;;){size_t k;CHECK(rd(shared,buf,sizeof buf,&k)==0,"concurrent read");if(!k)return NULL;pthread_mutex_lock(&total_lock);total+=k;pthread_mutex_unlock(&total_lock);}}
static long rss_mib(void){FILE *f=fopen("/proc/self/status","r");char line[256];long v=0;while(f&&fgets(line,sizeof line,f))if(!strncmp(line,"VmRSS:",6))v=atol(line+6)>>10;if(f)fclose(f);return v;}
int main(void){
 size_t len;uint8_t *data,*want;
 {gxc_value a=num(3<<20);gxc_handle *r=open_handle("Pattern",&a,1,"io.Reader");
  size_t k;gxc_error e;CHECK(gxc_stream_write(r,(const uint8_t *)"x",1,&k,&e)==11,"write on a reader");gxc_error_free(&e);
  CHECK(gxc_stream_seek(r,0,0,NULL,&e)==11,"seek on a reader");gxc_error_free(&e);
  data=read_all(r,&len);want=pattern(3<<20,0);CHECK(len==3u<<20&&!memcmp(data,want,len),"read all");free(data);free(want);
  CHECK(closeh(r)==0,"close");CHECK(rd(r,(uint8_t[4]){0},4,&k)==7,"read after close");CHECK(closeh(r)==0,"close twice");gxc_handle_free(r);}
 {gxc_value a={.kind=GXC_BYTES,.bytes=(uint8_t *)"hello, world",.length=12};gxc_handle *rs=open_handle("Text",&a,1,"io.ReadSeeker");
  int64_t pos;gxc_error e;CHECK(gxc_stream_seek(rs,7,0,&pos,&e)==0&&pos==7,"seek");
  data=read_all(rs,&len);CHECK(len==5&&!memcmp(data,"world",5),"read after seek");free(data);
  CHECK(gxc_stream_seek(rs,-5,2,&pos,&e)==0&&pos==7,"seek from end");closeh(rs);gxc_handle_free(rs);}
 {gxc_value a=num(10);gxc_handle *f=open_handle("Failing",&a,1,"io.Reader");uint8_t buf[64];size_t k;
  CHECK(rd(f,buf,64,&k)==0&&k==10,"data before the error");CHECK(rd(f,buf,64,&k)==1,"source error");closeh(f);gxc_handle_free(f);}
 {gxc_value r;CHECK(call("Nil",NULL,0,&r)==0&&r.kind==GXC_NIL,"nil reader");}
 {uint8_t *big=pattern(3<<20,0);memory m={big,3u<<20,0};gxc_stream s={.state=&m,.read=memory_read};gxc_value a={.kind=GXC_STREAM,.stream=&s};
  gxc_handle *rc=open_handle("Through",&a,1,"io.ReadCloser");data=read_all(rc,&len);CHECK(len==3u<<20&&!memcmp(data,big,len),"host to source to host");free(data);free(big);
  CHECK(closeh(rc)==0,"close through");gxc_handle_free(rc);}
 {gxc_value a={.kind=GXC_BOOL,.integer=1};gxc_handle *sink=open_handle("NewSink",&a,1,"Sink");
  gxc_value s=hv(sink);gxc_handle *w=open_handle("Sink.Writer",&s,1,"io.WriteCloser");
  uint8_t *d=pattern(5<<20,0);size_t k;gxc_error e;CHECK(gxc_stream_write(w,d,5u<<20,&k,&e)==0&&k==5u<<20,"write");
  CHECK(closeh(w)==0,"close writer");gxc_value r;CHECK(call("Sink.Closes",&s,1,&r)==0&&r.integer==1,"writer close runs the source Close");
  CHECK(gxc_stream_write(w,d,1,&k,&e)==7,"write after close");gxc_error_free(&e);gxc_handle_free(w);
  CHECK(gxc_stream_write(sink,(const uint8_t *)"tail",4,&k,&e)==0&&k==4,"handle stream write");
  CHECK(call("Sink.Count",&s,1,&r)==0&&r.integer==(5<<20)+4,"count");
  gxc_handle *c=open_handle("Sink.Contents",&s,1,"io.Reader");data=read_all(c,&len);
  CHECK(len==(5u<<20)+4&&!memcmp(data,d,5u<<20)&&!memcmp(data+(5u<<20),"tail",4),"contents");free(data);free(d);
  closeh(c);gxc_handle_free(c);closeh(sink);gxc_handle_free(sink);}
 {gxc_value a={.kind=GXC_BYTES,.bytes=(uint8_t *)"persistent document",.length=19};gxc_handle *doc=open_handle("Open",&a,1,"Doc");
  int64_t pos;gxc_error e;CHECK(gxc_stream_seek(doc,11,0,&pos,&e)==0&&pos==11,"handle seek");
  data=read_all(doc,&len);CHECK(len==8&&!memcmp(data,"document",8),"handle read");free(data);
  gxc_value d=hv(doc),r;CHECK(call("Doc.Reads",&d,1,&r)==0&&r.integer>=2,"reads");
  CHECK(closeh(doc)==0,"close doc");CHECK(call("Doc.Reads",&d,1,&r)==7,"closed");gxc_handle_free(doc);}
 {gxc_value a=num(1<<20);shared=open_handle("Pattern",&a,1,"io.Reader");pthread_t ts[8];
  for(int i=0;i<8;i++){pthread_create(&ts[i],NULL,reader,NULL);}
  for(int i=0;i<8;i++){pthread_join(ts[i],NULL);}
  CHECK(total==1u<<20,"concurrent reads");closeh(shared);gxc_handle_free(shared);}
 const char *env=getenv("THROUGHPUT_MIB");size_t mib=env?(size_t)atol(env):256,size=mib<<20;
 uint8_t *block=pattern(251*1024,0);source src={size,0,block};gxc_stream s={.state=&src,.read=source_read};gxc_value a={.kind=GXC_STREAM,.stream=&s};
 struct timespec t0,t1;clock_gettime(CLOCK_MONOTONIC,&t0);
 gxc_handle *r=open_handle("Through",&a,1,"io.ReadCloser");
 long base=rss_mib(),peak=0;uint32_t h=2166136261u;size_t count=0,k;uint8_t *buf=malloc(1u<<20);
 for(;;){CHECK(rd(r,buf,1u<<20,&k)==0,"throughput read");if(!k)break;for(size_t i=0;i<k;i++)h=(h^buf[i])*16777619u;if((count>>24)!=((count+k)>>24)){long v=rss_mib();if(v>peak)peak=v;}count+=k;}
 closeh(r);gxc_handle_free(r);clock_gettime(CLOCK_MONOTONIC,&t1);
 double seconds=(double)(t1.tv_sec-t0.tv_sec)+(double)(t1.tv_nsec-t0.tv_nsec)/1e9;
 uint32_t w2=2166136261u;for(size_t i=0;i<size;i++)w2=(w2^(uint8_t)(i%251))*16777619u;
 CHECK(count==size&&h==w2,"throughput copy");CHECK(peak<base+256,"rss growth");
 printf("throughput: %zu MiB in %.1fs (%.1f MiB/s), rss %ld MiB at start, peak %ld MiB\n",mib,seconds,(double)mib/seconds,base,peak);
 puts("PASS source streams");
 free(buf);free(block);return 0;
}
`
