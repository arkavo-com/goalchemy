package integration

import (
	"context"
	"encoding/pem"
	"fmt"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/testutil"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func cHostInject(t *testing.T, path, marker, replacement string) {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Count(string(b), marker) != 1 {
		t.Fatalf("nonunique test-only marker %q in %s", marker, path)
	}
	if e = os.WriteFile(path, []byte(strings.Replace(string(b), marker, replacement, 1)), 0600); e != nil {
		t.Fatal(e)
	}
}
func cHostFixture(t *testing.T, source string) string {
	t.Helper()
	fixture, out := t.TempDir(), t.TempDir()
	root, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	for name, data := range map[string]string{"go.mod": fmt.Sprintf("module chostprobe\n\ngo 1.25\n\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => %s\n", root), "main.go": source} {
		if e = os.WriteFile(filepath.Join(fixture, name), []byte(data), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if ds := testutil.CompileGate(fixture, "c", out, "cooperative"); len(ds) > 0 {
		t.Fatal(ds)
	}
	return out
}
func cHostCommand(t *testing.T, out string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = out
	cmd.Env = driver.ToolEnv()
	b, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("actual C %v: %v\n%s", args, e, b)
	}
	return string(b)
}
func cHostAdapter(t *testing.T, out, url, tls, cert string) {
	t.Helper()
	s := strings.NewReplacer("HTTP_URL", url, "TLS_URL", tls, "CA_FILE", cert).Replace(cTestOnlyTransport)
	if e := os.WriteFile(filepath.Join(out, "rt/runtime/test_only_transport.c"), []byte(s), 0600); e != nil {
		t.Fatal(e)
	}
	cHostInject(t, filepath.Join(out, "run.sh"), "set --", "set -- 'rt/runtime/test_only_transport.c'")
	cHostInject(t, filepath.Join(out, "rt/types/sched.c"), "        if (!s->fault)\n            s->fault = GC_strdup(message);", "        extern void test_only_fault_frame(gx_Frame *); test_only_fault_frame(s->cur ? s->cur->frame : NULL);\n        if (!s->fault)\n            s->fault = GC_strdup(message);")
	cHostInject(t, filepath.Join(out, "rt/runtime/task_spawn.c"), "    s->escape = NULL;", "    s->escape = NULL;\n    extern void test_only_final_cleanup(void); test_only_final_cleanup();")
	cHostInject(t, filepath.Join(out, "rt/runtime/std_time_sleep.c"), "    gx_set_rv(t, 0, NULL);", "    if (d.u.i >= 123 && d.u.i <= 131) { extern void test_only_sleep(gx_Task *, int); test_only_sleep(t, (int)d.u.i); return; }\n    gx_set_rv(t, 0, NULL);")
	cHostInject(t, filepath.Join(out, "rt/runtime/std_context_with_cancel.c"), "    gx_V c = gx_new_child(parent);", "    gx_V c = gx_new_child(parent);\n    extern void test_only_remember_context(gx_V); test_only_remember_context(c);")
}

// Real stdlib servers plus a test-only native curl subprocess adapter. This is
// intentionally not production HTTP URL/header/redirect/body-limit validation.
func TestGeneratedCHostOperations(t *testing.T) {
	var normal, release, cancel, contact, started, tlsContact atomic.Int32
	var mu sync.Mutex
	var body, seen, start chan struct{}
	var gates []chan struct{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		b, s, g := body, seen, start
		mu.Unlock()
		switch r.URL.Path {
		case "/reset":
			mu.Lock()
			body = make(chan struct{})
			seen = make(chan struct{})
			start = make(chan struct{})
			gates = append(gates, body, seen, start)
			mu.Unlock()
			w.Write([]byte("reset"))
		case "/normal":
			normal.Add(1)
			close(g)
			w.Write([]byte{0, 255, 128, 3})
			w.(http.Flusher).Flush()
			select {
			case <-b:
			case <-r.Context().Done():
			}
		case "/started":
			started.Add(1)
			select {
			case <-g:
			case <-r.Context().Done():
				return
			}
			w.Write([]byte("started"))
		case "/release":
			release.Add(1)
			select {
			case <-g:
			case <-r.Context().Done():
				return
			}
			close(b)
			w.Write([]byte("released"))
		case "/cancel":
			cancel.Add(1)
			close(s)
			w.Write([]byte{0, 255, 128, 3})
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case "/contact":
			contact.Add(1)
			select {
			case <-s:
			case <-r.Context().Done():
				return
			}
			w.Write([]byte("contact"))
		default:
			t.Errorf("unexpected contact %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer func() {
		mu.Lock()
		for _, g := range gates {
			select {
			case <-g:
			default:
				close(g)
			}
		}
		mu.Unlock()
		srv.Close()
	}()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { tlsContact.Add(1); w.Write([]byte{0, 255, 128, 3}) }))
	defer tls.Close()
	out := cHostFixture(t, `package main
import("github.com/eugenioenko/goalchemy/lib/context";"github.com/eugenioenko/goalchemy/lib/time")
var counter int
func init(){counter++;println("init",counter)}
func main(){counter++;println("main",counter)
 b:=[]byte{0,255,128,3};println(b[0],b[1],b[2],b[3]);c:=make(chan *int,1);c<-nil;println(<-c==nil)
 var missing context.Context;println(missing==nil,nil==missing)
 go func(){time.Sleep(125);println("source progress");time.Sleep(124)}()
 time.Sleep(123);println("transport returned")
 ctx,stop:=context.WithCancel(context.Background());go func(){time.Sleep(127);stop()}();time.Sleep(126);println(ctx.Err()==context.Canceled)
 time.Sleep(128);time.Sleep(129);println("TLS done")
 expired,ce:=context.WithTimeout(context.Background(),-1);defer ce();child,cc:=context.WithTimeout(expired,time.Second);defer cc();println(expired.Err()==context.DeadlineExceeded,child.Err()==context.DeadlineExceeded)
}`)
	want := "init 1\nmain 2\n0 255 128 3\ntrue\ntrue true\ntransport returned\nsource progress\nfalse\nTLS done\ntrue true\n"
	if got := cHostCommand(t, out, "sh", "run.sh"); got != want {
		t.Fatalf("unchanged virtual source %q", got)
	}
	cert := filepath.Join(out, "test-only-ca.pem")
	if e := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tls.Certificate().Raw}), 0600); e != nil {
		t.Fatal(e)
	}
	cHostAdapter(t, out, srv.URL, tls.URL, cert)
	cHostInject(t, filepath.Join(out, "main.c"), "int main(void) {", `extern void test_only_begin(void); extern int test_only_cleaned(void);
int main(void) { test_only_begin(); if(goalchemy_run_host())return 7; test_only_begin(); if(goalchemy_run_host())return 8; return test_only_cleaned()==14?0:9; }
int unchanged_virtual_main(void) {`)
	got := cHostCommand(t, out, "sh", "run.sh")
	host := strings.Replace(strings.Replace(want, "transport returned\nsource progress", "source progress\ntransport returned", 1), "false\nTLS done", "true\nTLS done", 1)
	if got != host+host {
		t.Fatalf("actual emitted repeated C host source %q", got)
	}
	if normal.Load() != 2 || release.Load() != 2 || cancel.Load() != 2 || contact.Load() != 2 || started.Load() != 2 || tlsContact.Load() != 2 {
		t.Fatalf("exact contacts normal=%d release=%d cancel=%d contact=%d started=%d TLS=%d", normal.Load(), release.Load(), cancel.Load(), contact.Load(), started.Load(), tlsContact.Load())
	}
}

const cTestOnlyTransport = `/* Test-only curl wire adapter; no production HTTP mapping. */
#include "gx.h"
#include <assert.h>
#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <spawn.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/wait.h>
#include <unistd.h>
extern char **environ;
static gx_V remembered_context; /* scanned executable source root, owner writes only */
static atomic_int cleaned,leases,sequence;
void test_only_remember_context(gx_V context){remembered_context=context;}
int test_only_cleaned(void){assert(atomic_load(&leases)==0);return atomic_load(&cleaned);}
static gx_Frame **fault_frames;static size_t fault_count;
/* Publish only complete markers: observers must not see fopen's empty-file window. */
static void publish_marker(const char *path,const char *text){char temporary[1200];int n=snprintf(temporary,sizeof(temporary),"%s.tmp",path);assert(n>=0&&(size_t)n<sizeof(temporary));FILE*f=fopen(temporary,"wb");assert(f);assert(fputs(text,f)>=0);assert(!fclose(f));assert(!rename(temporary,path));}
void test_only_fault_frame(gx_Frame *first){if(fault_frames||!first)return;size_t cap=16;fault_frames=GC_MALLOC(cap*sizeof(gx_Frame*));fault_frames[fault_count++]=first;for(size_t i=0;i<fault_count;i++){gx_Frame*f=fault_frames[i];gx_Frame*children[]={f->parent,f->a,f->b};for(int j=0;j<3;j++){if(!children[j])continue;bool seen=false;for(size_t k=0;k<fault_count;k++)if(fault_frames[k]==children[j])seen=true;if(seen)continue;if(fault_count==cap){cap*=2;gx_Frame**next=GC_MALLOC(cap*sizeof(gx_Frame*));memcpy(next,fault_frames,fault_count*sizeof(gx_Frame*));fault_frames=next;}fault_frames[fault_count++]=children[j];}}GC_gcollect();}
void test_only_final_cleanup(void){assert(atomic_load(&leases)==0);if(getenv("C_HOST_EXPECT_DEFER_GRAPH"))assert(fault_count>=3);for(size_t i=0;i<fault_count;i++){gx_Frame*f=fault_frames[i];assert(!f->parent&&!f->a&&!f->b&&!f->l&&!f->nl&&!f->defers&&!f->panicking&&!f->prim&&!f->prim_arg);}fault_frames=NULL;fault_count=0;const char*marker=getenv("C_HOST_CLEANUP_MARKER");if(marker){char path[1024];snprintf(path,sizeof(path),"%s-owner",marker);publish_marker(path,"owner cleanup complete\n");}}
static void poll_native(void){struct timespec delay={0,1000000};nanosleep(&delay,NULL);}
static pid_t curl_start(const char *url,const char *output,const char *header,const char *cert){
 char *args[20]={"curl","--silent","--show-error","--http1.1","--max-time","25","--output",(char*)output,"--dump-header",(char*)header};int n=10;if(cert){args[n++]="--cacert";args[n++]=(char*)cert;}args[n++]=(char*)url;args[n]=NULL;
 posix_spawn_file_actions_t actions;assert(!posix_spawn_file_actions_init(&actions));assert(!posix_spawn_file_actions_addopen(&actions,1,"/dev/null",O_WRONLY,0));assert(!posix_spawn_file_actions_addopen(&actions,2,"/dev/null",O_WRONLY,0));pid_t pid;int rc=posix_spawnp(&pid,"curl",&actions,NULL,args,environ);posix_spawn_file_actions_destroy(&actions);assert(!rc);return pid;
}
static uint8_t *read_file(const char *path,size_t *n){FILE *f=fopen(path,"rb");if(!f){*n=0;return NULL;}assert(!fseek(f,0,SEEK_END));long len=ftell(f);assert(len>=0);rewind(f);uint8_t *bytes=malloc((size_t)len+1);assert(bytes);*n=fread(bytes,1,(size_t)len,f);bytes[*n]=0;fclose(f);return bytes;}
void test_only_begin(void){char output[128],header[128];snprintf(output,sizeof(output),"/tmp/c-host-reset-%ld",(long)getpid());snprintf(header,sizeof(header),"%s.headers",output);pid_t pid=curl_start("HTTP_URL/reset",output,header,NULL);int status;assert(waitpid(pid,&status,0)==pid&&WIFEXITED(status)&&WEXITSTATUS(status)==0);size_t n;uint8_t *bytes=read_file(output,&n);assert(n==5&&!memcmp(bytes,"reset",5));free(bytes);unlink(output);unlink(header);remembered_context=gx_nil();}
typedef struct NativeJob{gx_HostToken token;pthread_t thread;atomic_bool stop;bool launched;int mode;char url[512],output[128],header[128];}NativeJob;
static const char *cancel_native(void *arg){atomic_store(&((NativeJob*)arg)->stop,true);return NULL;}
static const char *cleanup_native(void *arg){NativeJob*j=arg;if(j->launched)assert(!GC_pthread_join(j->thread,NULL));free(j);return NULL;}
static void *native_work(void *arg){
 NativeJob*j=arg;pid_t pid=curl_start(j->url,j->output,j->header,j->mode==128?"CA_FILE":NULL);atomic_fetch_add(&leases,1);int status=0;bool native_fault=false;
 for(;;){if(atomic_load(&j->stop)){kill(pid,SIGTERM);assert(waitpid(pid,&status,0)==pid);break;}pid_t done=waitpid(pid,&status,WNOHANG);assert(done>=0);if(done==pid)break;
  if(j->mode==130){size_t n;uint8_t*header=read_file(j->header,&n);bool contact=header&&strstr((char*)header,"HTTP/1.1 200");free(header);if(contact){native_fault=true;gx_host_publish(j->token,NULL,0,"test native worker fault after acquisition");kill(pid,SIGTERM);assert(waitpid(pid,&status,0)==pid);break;}}
  poll_native();
 }
 size_t n=0;uint8_t*bytes=read_file(j->output,&n);bool good=WIFEXITED(status)&&WEXITSTATUS(status)==0;
 if(!native_fault)gx_host_publish(j->token,good?bytes:NULL,good?n:0,NULL);
 free(bytes);unlink(j->output);unlink(j->header);memset(j->url,0,sizeof(j->url));memset(j->output,0,sizeof(j->output));memset(j->header,0,sizeof(j->header));atomic_fetch_sub(&leases,1);atomic_fetch_add(&cleaned,1);
 const char*marker=getenv("C_HOST_CLEANUP_MARKER");const char*gate=getenv("C_HOST_ACK_GATE");if(marker&&(j->mode==130||j->mode==131)){char operation_marker[1024];snprintf(operation_marker,sizeof(operation_marker),"%s-%d",marker,j->mode);publish_marker(operation_marker,"released native body/process/input\n");while(gate&&access(gate,F_OK))poll_native();}
 gx_host_ack(j->token);gx_host_token_release(j->token);return NULL;
}
static const char *decode(gx_Task*t,const uint8_t*b,size_t n,gx_V cancellation,gx_V roots){int mode=(int)roots.u.i;if(mode==126){assert(cancellation.t!=GX_NIL);}else if(mode==129){assert(n==0);}else if(mode==123||mode==128||mode==131){const uint8_t want[]={0,255,128,3};assert(n==4&&!memcmp(b,want,4));}else if(mode==124){assert(n==8&&!memcmp(b,"released",8));}else if(mode==125){assert(n==7&&!memcmp(b,"started",7));}else if(mode==127){assert(n==7&&!memcmp(b,"contact",7));}gx_set_rv(t,0,NULL);return NULL;}
void test_only_sleep(gx_Task*t,int mode){
 NativeJob*j=calloc(1,sizeof(*j));assert(j);j->mode=mode;
 const char*path=mode==123||mode==130||mode==131?"/normal":mode==124?"/release":mode==125?"/started":mode==126?"/cancel":mode==127?"/contact":"/";
 snprintf(j->url,sizeof(j->url),"%s%s",mode>=128&&mode<=129?"TLS_URL":"HTTP_URL",path);int seq=atomic_fetch_add(&sequence,1);snprintf(j->output,sizeof(j->output),"/tmp/c-host-native-%ld-%d",(long)getpid(),seq);snprintf(j->header,sizeof(j->header),"%s.headers",j->output);
 gx_HostBoundary boundary=gx_host_boundary(mode==126?remembered_context:gx_background(),30000000000LL);
 j->token=gx_host_register(t,boundary,gx_int(mode),decode,cancel_native,cleanup_native,j);
 if(!gx_host_should_submit(j->token)){gx_host_token_release(j->token);return;}
 assert(!GC_pthread_create(&j->thread,NULL,native_work,j));j->launched=true;
}
`

func TestGeneratedCHostTaskAll(t *testing.T) {
	out := cHostFixture(t, `package main
import("github.com/eugenioenko/goalchemy/lib/task";"github.com/eugenioenko/goalchemy/lib/time")
func main(){task.All(func(){time.Sleep(1);println("first")},func(){time.Sleep(2);println("second")});println("all done")}
`)
	virtual := cHostCommand(t, out, "sh", "run.sh")
	cHostInject(t, filepath.Join(out, "main.c"), "    gx_run_main(_init_zero_globals, _entry_frame);", "    gx_host_main(_init_zero_globals, _entry_frame);")
	host := cHostCommand(t, out, "sh", "run.sh")
	if host != virtual || host != "first\nsecond\nall done\n" {
		t.Fatalf("actual selectively linked task.All %q virtual %q", host, virtual)
	}
}
