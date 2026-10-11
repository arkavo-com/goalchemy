package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
)

func TestGeneratedCStreams(t *testing.T) {
	res := buildStreams(t)
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
	for name, data := range map[string]string{"consumer.c": cStreamsConsumer, "build.sh": cPackageConsumerBuild} {
		if err := os.WriteFile(filepath.Join(consumer, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runTool(t, consumer, "sh", "build.sh", filepath.Join(out, "libgoalchemy.a"))
	runTool(t, consumer, filepath.Join(consumer, "consumer"))
}

const cStreamsConsumer = `#define _POSIX_C_SOURCE 200809L
#include "goalchemy.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#define CHECK(ok,msg) do{if(!(ok)){fprintf(stderr,"FAIL %s (line %d)\n",msg,__LINE__);exit(1);}}while(0)
typedef struct {uint8_t *data;size_t len,cap,pos;int closes;} buffer;
static int64_t buf_read(void *s,uint8_t *b,size_t n,char *m){(void)m;buffer *x=s;size_t k=x->len-x->pos<n?x->len-x->pos:n;memcpy(b,x->data+x->pos,k);x->pos+=k;return (int64_t)k;}
static int64_t buf_write(void *s,const uint8_t *b,size_t n,char *m){(void)m;buffer *x=s;if(x->len+n>x->cap){x->cap=(x->len+n)*2;x->data=realloc(x->data,x->cap);}memcpy(x->data+x->len,b,n);x->len+=n;return (int64_t)n;}
static int64_t buf_seek(void *s,int64_t off,int whence,char *m){buffer *x=s;int64_t base=whence==0?0:whence==1?(int64_t)x->pos:(int64_t)x->len;if(base+off<0){strcpy(m,"negative position");return -1;}x->pos=(size_t)(base+off);return (int64_t)x->pos;}
static int buf_close(void *s,char *m){(void)m;((buffer *)s)->closes++;return 0;}
static int64_t fail_read(void *s,uint8_t *b,size_t n,char *m){(void)s;(void)b;(void)n;strcpy(m,"disk on fire");return -1;}
static int64_t reenter_read(void *s,uint8_t *b,size_t n,char *m){(void)s;(void)b;(void)n;gxc_value r;gxc_error e;int k=goalchemy_invoke("Inits",NULL,0,NULL,&r,&e);gxc_error_free(&e);snprintf(m,256,"reentrant kind %d",k);return -1;}
static buffer from(const char *s){buffer b={0};b.len=b.cap=strlen(s);b.data=malloc(b.len+1);memcpy(b.data,s,b.len);return b;}
static gxc_stream reader(buffer *b){return (gxc_stream){.state=b,.read=buf_read,.seek=buf_seek,.close=buf_close};}
static gxc_stream writer(buffer *b){return (gxc_stream){.state=b,.write=buf_write};}
static gxc_value sv(const gxc_stream *s){return (gxc_value){.kind=GXC_STREAM,.stream=s};}
static gxc_value num(int64_t n){return (gxc_value){.kind=GXC_INT,.integer=n};}
static int call(const char *name,gxc_value *args,size_t n,gxc_value *out){gxc_error e;int k=goalchemy_invoke(name,args,n,NULL,out,&e);gxc_error_free(&e);return k;}
int main(void){
 size_t big_len=3u<<20;buffer big={0};big.data=malloc(big_len);big.len=big.cap=big_len;for(size_t i=0;i<big_len;i++)big.data[i]=(uint8_t)(i%251);
 buffer out={0};gxc_stream r=reader(&big),w=writer(&out);gxc_value r0;
 {gxc_value a[2]={sv(&w),sv(&r)};CHECK(call("Copy",a,2,&r0)==0&&r0.integer==(int64_t)big_len&&out.len==big_len&&!memcmp(out.data,big.data,big_len),"chunked copy");}
 buffer three={0};uint8_t bytes3[3]={1,2,3};three.data=bytes3;three.len=3;gxc_stream t=reader(&three);
 {gxc_value a=sv(&t);CHECK(call("Sum",&a,1,&r0)==0&&r0.integer==6,"sum");}
 buffer hello=from("hello, world");gxc_stream h=reader(&hello);
 {gxc_value a[3]={sv(&h),num(7),num(5)};CHECK(call("Section",a,3,&r0)==0&&r0.length==5&&!memcmp(r0.bytes,"world",5),"seek");gxc_value_free(&r0);}
 buffer shortb=from("short");gxc_stream sh=reader(&shortb);
 {gxc_value a[3]={sv(&sh),num(3),num(5)};CHECK(call("Section",a,3,&r0)==1,"io.ErrUnexpectedEOF");}
 buffer abc=from("abc");gxc_stream ab=reader(&abc);
 {gxc_value a=sv(&ab);CHECK(call("Drain",&a,1,&r0)==0&&r0.integer==3&&abc.closes==1,"close");}
 buffer empty={0};gxc_stream em=reader(&empty);
 {gxc_value a=sv(&em);CHECK(call("AtEOF",&a,1,&r0)==0&&r0.integer==1,"host EOF is io.EOF");}
 gxc_stream f={.read=fail_read};
 {gxc_value a=sv(&f);CHECK(call("FirstError",&a,1,&r0)==0&&r0.length==12&&!memcmp(r0.bytes,"disk on fire",12),"host error");gxc_value_free(&r0);}
 {gxc_value a={0};CHECK(call("Nil",&a,1,&r0)==0&&r0.integer==1,"nil stream");}
 gxc_stream re={.read=reenter_read};
 {gxc_value a=sv(&re);CHECK(call("FirstError",&a,1,&r0)==0&&r0.length==17&&!memcmp(r0.bytes,"reentrant kind 10",17),"reentrant host stream");gxc_value_free(&r0);}
 {gxc_value a=sv(&w);CHECK(call("Sum",&a,1,&r0)==5,"stream without read");}
 buffer docb=from("persistent document");gxc_stream d=reader(&docb);
 {gxc_value a=sv(&d);CHECK(call("Open",&a,1,&r0)==0&&r0.kind==GXC_HANDLE,"open");}
 gxc_handle *doc=r0.handle;
 {gxc_value a={.kind=GXC_HANDLE,.handle=doc};CHECK(call("Doc.Size",&a,1,&r0)==0&&r0.integer==19,"size");}
 {gxc_value a[3]={{.kind=GXC_HANDLE,.handle=doc},num(11),num(8)};CHECK(call("Doc.ReadAt",a,3,&r0)==0&&r0.length==8&&!memcmp(r0.bytes,"document",8),"stream kept by the instance");gxc_value_free(&r0);}
 buffer whole={0};gxc_stream ww=writer(&whole);
 {gxc_value a[2]={{.kind=GXC_HANDLE,.handle=doc},sv(&ww)};CHECK(call("Doc.WriteTo",a,2,&r0)==0&&r0.integer==19&&whole.len==19&&!memcmp(whole.data,"persistent document",19),"WriteTo");}
 gxc_handle_free(doc);
 puts("PASS streams");
 return 0;
}
`
