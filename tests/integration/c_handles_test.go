package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
)

func TestGeneratedCHandles(t *testing.T) {
	res := buildHandles(t)
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
	for name, data := range map[string]string{"consumer.c": cHandlesConsumer, "build.sh": cPackageConsumerBuild} {
		if err := os.WriteFile(filepath.Join(consumer, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runTool(t, consumer, "sh", "build.sh", filepath.Join(out, "libgoalchemy.a"))
	runTool(t, consumer, filepath.Join(consumer, "consumer"))
}

const cHandlesConsumer = `#define _POSIX_C_SOURCE 200809L
#include "goalchemy.h"
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#define CHECK(ok,msg) do{if(!(ok)){fprintf(stderr,"FAIL %s (line %d)\n",msg,__LINE__);exit(1);}}while(0)
static gxc_value str(const char *s){return (gxc_value){.kind=GXC_BYTES,.bytes=(uint8_t *)s,.length=strlen(s)};}
static gxc_value num(int64_t n){return (gxc_value){.kind=GXC_INT,.integer=n};}
static gxc_value hv(gxc_handle *h){return h?(gxc_value){.kind=GXC_HANDLE,.handle=h}:(gxc_value){0};}
static int call(const char *name,gxc_value *args,size_t n,gxc_value *out){gxc_error e;int k=goalchemy_invoke(name,args,n,NULL,out,&e);gxc_error_free(&e);return k;}
static gxc_handle *make(const char *name){gxc_value a=str(name),r;CHECK(call("New",&a,1,&r)==0&&r.kind==GXC_HANDLE,"New");return r.handle;}
static int64_t add(gxc_handle *h,int64_t d){gxc_value a[2]={hv(h),num(d)},r;CHECK(call("Counter.Add",a,2,&r)==0,"Add");return r.integer;}
static int64_t field(gxc_handle *h,const char *name){gxc_value a=hv(h),r;CHECK(call("Counter.Snap",&a,1,&r)==0,"Snap");int64_t v=gxc_field(&r,name)->integer;gxc_value_free(&r);return v;}
static gxc_handle *one(const char *name,gxc_handle *h){gxc_value a=hv(h),r;CHECK(call(name,&a,1,&r)==0,name);return r.kind==GXC_HANDLE?r.handle:NULL;}
static int64_t total(gxc_handle *p){gxc_value a=hv(p),r;CHECK(call("Coord.Total",&a,1,&r)==0,"Total");return r.integer;}
static int64_t with(const char *name,gxc_handle *p,int64_t d){gxc_value a[2]={hv(p),num(d)},r;CHECK(call(name,a,2,&r)==0,name);return r.integer;}
static void *worker(void *arg){for(int i=0;i<50;i++)add(arg,1);return NULL;}
typedef struct {gxc_handle *h;int64_t ms;int kind;} pending;
static void *wait_call(void *arg){pending *p=arg;gxc_value a[2]={hv(p->h),num(p->ms)},r;p->kind=call("Counter.Wait",a,2,&r);return NULL;}
static void *add_call(void *arg){pending *p=arg;gxc_value a[2]={hv(p->h),num(1)},r;p->kind=call("Counter.Add",a,2,&r);return NULL;}
static void sleep_ms(long ms){struct timespec t={ms/1000,(ms%1000)*1000000};nanosleep(&t,NULL);}
static char warnings[4096];
static void warn(void *state,const char *m){(void)state;strncat(warnings,m,sizeof warnings-strlen(warnings)-2);strcat(warnings,"|");}
int main(void){
 gxc_handle *c=make("a");gxc_value r;
 for(int i=1;i<=3;i++)CHECK(add(c,2)==2*i,"persistent field");
 gxc_value neg[2]={hv(c),num(-1)};CHECK(call("Counter.Add",neg,2,&r)==1,"source error");
 CHECK(field(c,"N")==6&&field(c,"Inits")==1&&field(c,"Created")==1&&gxc_field(&r,"LastNeg")!=NULL,"instance globals");
 {gxc_value a=hv(c),s;CHECK(call("Counter.Snap",&a,1,&s)==0&&gxc_field(&s,"LastNeg")->integer==1,"sentinel identity");gxc_value_free(&s);}
 {gxc_value a=hv(c),k;CHECK(call("Counter.PublicKey",&a,1,&k)==0&&k.length>26&&!memcmp(k.bytes,"-----BEGIN PUBLIC KEY-----",26),"native key survives");gxc_value_free(&k);}
 for(int i=0;i<3;i++){CHECK(call("Inits",NULL,0,&r)==0&&r.integer==1,"fresh free functions");}
 CHECK(one("Counter.Self",c)==c,"identity");
 gxc_handle *child=one("Counter.Child",c);
 {gxc_value a=hv(child);CHECK(call("Child.Bump",&a,1,&r)==0&&r.integer==7,"derived handle");}
 CHECK(one("Child.Parent",child)==c,"derived parent identity");
 CHECK(gxc_handle_close(child,NULL)==0,"close child");
 {gxc_value a=hv(child);CHECK(call("Child.Bump",&a,1,&r)==7,"closed derived handle");}
 gxc_handle_free(child);
 CHECK(add(c,1)==8,"instance kept");
 CHECK(call("Nil",NULL,0,&r)==0&&r.kind==GXC_NIL,"nil handle");
 gxc_handle_close(c,NULL);gxc_handle_free(c);
 gxc_handle *a=make("a"),*b=NULL,*other=make("other");
 {gxc_value args[2]={hv(a),str("b")};CHECK(call("Counter.Sibling",args,2,&r)==0,"sibling");b=r.handle;}
 add(a,2);add(b,3);
 {gxc_value args[2]={hv(a),hv(b)};CHECK(call("Sum",args,2,&r)==0&&r.integer==5,"handle parameters");}
 CHECK(field(b,"Created")==2&&field(b,"Inits")==1,"sibling joins");
 {gxc_value args[2]={hv(a),hv(other)};CHECK(call("Sum",args,2,&r)==9,"instance mismatch");}
 {gxc_value args[2]={{0},{0}};CHECK(call("Sum",args,2,&r)==0&&r.integer==-1,"nil arguments");}
 {gxc_value args[2]={hv(a),str("x")};CHECK(call("Sum",args,2,&r)==5,"invalid handle argument");}
 gxc_handle_free(a);gxc_handle_free(b);gxc_handle_free(other);
 c=make("a");
 {gxc_value s=hv(c);CHECK(call("Counter.Name",&s,1,&r)==0&&r.length==1&&r.bytes[0]=='a',"value receiver on a handle with private state");gxc_value_free(&r);}
 gxc_handle_close(c,NULL);gxc_handle_free(c);
 {gxc_value xy[2]={num(1),num(2)};CHECK(call("NewCoord",xy,2,&r)==0&&r.kind==GXC_HANDLE,"NewCoord");}
 gxc_handle *p=r.handle;
 CHECK(total(p)==3&&with("Coord.Shift",p,5)==6&&total(p)==3,"value receiver must not change the handle");
 {gxc_value a[2]={hv(p),num(10)},m;CHECK(call("Coord.Moved",a,2,&m)==0&&gxc_field(&m,"X")->integer==11&&gxc_field(&m,"Y")->integer==2&&total(p)==3,"value result is a copy");
  gxc_value l[2]={hv(p),m};CHECK(call("Coord.Less",l,2,&r)==0&&r.kind==GXC_BOOL&&r.integer==1,"value parameter of the handle type");gxc_value_free(&m);}
 {gxc_value a[2]={hv(p),num(-1)},m;CHECK(call("Coord.Moved",a,2,&m)==0,"Moved");gxc_value l[2]={hv(p),m};CHECK(call("Coord.Less",l,2,&r)==0&&r.integer==0,"value parameter not less");gxc_value_free(&m);}
 CHECK(with("Coord.Move",p,1)==2&&total(p)==4,"pointer receiver changes the handle");
 CHECK(gxc_handle_close(p,NULL)==0,"close coord");gxc_handle_free(p);
 {gxc_value xy[2]={num(-1),num(0)};CHECK(call("NewCoord",xy,2,&r)==0,"NewCoord");gxc_error e;CHECK(gxc_handle_close(r.handle,&e)==1,"value receiver Close");gxc_error_free(&e);gxc_handle_free(r.handle);}
 gxc_handle *x=make("x"),*y=make("y");pthread_t threads[8];
 for(int i=0;i<8;i++)pthread_create(&threads[i],NULL,worker,i%2?y:x);
 for(int i=0;i<8;i++)pthread_join(threads[i],NULL);
 CHECK(field(x,"N")==200&&field(y,"N")==200&&field(x,"Created")==1,"serialized calls");
 gxc_handle_free(x);gxc_handle_free(y);
 c=make("a");gxc_handle *sib=NULL;{gxc_value args[2]={hv(c),str("bad-close")};call("Counter.Sibling",args,2,&r);sib=r.handle;}
 CHECK(gxc_handle_close(c,NULL)==0&&gxc_handle_close(c,NULL)==0,"close idempotent");
 CHECK(call("Counter.Add",(gxc_value[]){hv(c),num(1)},2,&r)==7,"call after close");
 CHECK(field(sib,"Closes")==1,"source Close ran once");
 {gxc_error e;CHECK(gxc_handle_close(sib,&e)==1,"source Close error");gxc_error_free(&e);}
 {gxc_value s=hv(sib);CHECK(call("Counter.Snap",&s,1,&r)==7,"failed Close releases");}
 gxc_handle_free(c);gxc_handle_free(sib);
 c=make("a");
 pending w={c,100000,-1},q={c,0,-1};pthread_t tw,tq;
 pthread_create(&tw,NULL,wait_call,&w);sleep_ms(100);
 pthread_create(&tq,NULL,add_call,&q);sleep_ms(50);
 gxc_handle_close(c,NULL);pthread_join(tw,NULL);pthread_join(tq,NULL);
 CHECK(w.kind==6,"active call canceled by close");
 CHECK(q.kind==7,"queued call failed by close");
 gxc_handle_free(c);
 c=make("a");
 {gxc_value s=hv(c);CHECK(call("Counter.Boom",&s,1,&r)==2,"panic");}
 CHECK(call("Counter.Add",(gxc_value[]){hv(c),num(1)},2,&r)==8,"poisoned");
 CHECK(gxc_handle_close(c,NULL)==0,"close poisoned");
 CHECK(call("Counter.Add",(gxc_value[]){hv(c),num(1)},2,&r)==7,"closed poisoned");
 gxc_handle_free(c);
 gxc_set_warning_handler(warn,NULL);
 c=make("a");{gxc_value s=hv(c);CHECK(call("Counter.Leak",&s,1,&r)==0,"leak");}
 CHECK(strstr(warnings,"goroutine")!=NULL,"abandoned goroutine warning");
 CHECK(add(c,1)==1,"usable after abandoning goroutines");
 gxc_handle_free(c);
 puts("PASS handles");
 return 0;
}
`
