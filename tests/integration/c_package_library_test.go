package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/link"
	"github.com/eugenioenko/goalchemy/internal/subset"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

func TestGeneratedCPackageLibrary(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	mod := fmt.Sprintf("module packageconsumer\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => %s\n", root)
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte(mod), 0600); err != nil {
		t.Fatal(err)
	}
	for _, pkg := range []string{"api", "state", "order"} {
		data, err := os.ReadFile(filepath.Join(root, "tests/integration/testdata/package_library", pkg, pkg+".go"))
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(strings.ReplaceAll(string(data), "github.com/eugenioenko/goalchemy/tests/integration/testdata/package_library/", "packageconsumer/"))
		dir := filepath.Join(source, pkg)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, pkg+".go"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	probe := `package api
import("github.com/eugenioenko/goalchemy/lib/context";"github.com/eugenioenko/goalchemy/lib/callback";"github.com/eugenioenko/goalchemy/lib/crypto";"github.com/eugenioenko/goalchemy/lib/checksum")
func Hold(ctx context.Context)([]byte,error){return callback.Request(ctx,"hold",nil)}
func Native(ctx context.Context)([]byte,error){k,e:=crypto.GenerateP256();if e!=nil{return nil,e};defer k.Close();public:=k.PublicPEM;p,e:=public();if e!=nil{return nil,e};if len(p)==0{panic("public key")};hash:=crypto.SHA256;return hash([]byte{0,255,128})}
func CRC(ctx context.Context,data []byte)(uint32,error){return checksum.CRC32IEEE(data),nil}
`
	if err := os.WriteFile(filepath.Join(source, "api", "probe.go"), []byte(probe), 0600); err != nil {
		t.Fatal(err)
	}
	res, ds := driver.Build(context.Background(), driver.Options{Dir: filepath.Join(source, "api"), Gate: subset.Cooperative})
	if diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	env := driver.ToolEnv()
	run := func(t *testing.T, dir, name string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir, cmd.Env = dir, env
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, data)
		}
		return string(data)
	}
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%v", compact), func(t *testing.T) {
			out, consumer := t.TempDir(), t.TempDir()
			if ds := driver.EmitWithOptions("c", res, out, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
				t.Fatal(ds)
			}
			adapter := `#include "goalchemy.h"
int tdf3_test_export(const gxc_value *input,gxc_value *output,gxc_error *error){return goalchemy_invoke("Export",input,1,NULL,output,error);}
`
			for name, data := range map[string]string{"tdf3.c": adapter, "Caller.c": "caller source outside the inventory\n", "rt/types/caller.c": "unlisted runtime source\n"} {
				if err := os.WriteFile(filepath.Join(out, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			run(t, out, "sh", "build.sh")
			members := run(t, out, "ar", "t", "libtdf3.a")
			for _, file := range cSourceFiles(t, out) {
				if !strings.Contains(members, filepath.Base(file)+".o\n") {
					t.Fatalf("archive omits generated translation unit %s: %s", file, members)
				}
			}
			if !strings.Contains(members, "tdf3.o\n") || strings.Contains(members, "caller") {
				t.Fatal("native adapter/inventory archive mismatch", members)
			}
			// Only the public headers and built archive cross into the consumer.
			for _, name := range []string{"goalchemy.h", "library.h"} {
				data, err := os.ReadFile(filepath.Join(out, name))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(consumer, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(consumer, "consumer.c"), []byte(cPackageLibraryConsumer), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(consumer, "build.sh"), []byte(cPackageConsumerBuild), 0600); err != nil {
				t.Fatal(err)
			}
			run(t, consumer, "sh", "build.sh", filepath.Join(out, "libtdf3.a"))
			t.Log(run(t, consumer, filepath.Join(consumer, "consumer")))
		})
	}
}

// Match collector discovery in the generated build script: local toolchains
// expose GOALCHEMY_BDWGC, while CI supplies the system libgc-dev package.
const cPackageConsumerBuild = `#!/bin/sh
set -eu
gc=-lgc
inc=""
if [ -n "${GOALCHEMY_BDWGC:-}" ]; then
 gc="$GOALCHEMY_BDWGC/lib/libgc.a"
 inc="-I$GOALCHEMY_BDWGC/include"
elif pkg-config --exists bdw-gc 2>/dev/null; then
 gc=$(pkg-config --libs bdw-gc)
 inc=$(pkg-config --cflags bdw-gc)
fi
${CC:-cc} -std=c17 -Wall -Wextra -Werror -I. $inc consumer.c "$1" $gc -lssl -lcrypto -lpthread -ldl -o consumer
`

func cSourceFiles(t *testing.T, out string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(out, "goalchemy.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest link.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	var sources []string
	for _, name := range manifest.GeneratedFiles {
		if filepath.Ext(name) == ".c" {
			sources = append(sources, name)
		}
	}
	return sources
}

const cPackageLibraryConsumer = `#define _POSIX_C_SOURCE 200809L
#define GC_THREADS
#include "goalchemy.h"
#include <assert.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <gc.h>
extern int tdf3_test_export(const gxc_value *,gxc_value *,gxc_error *);
static gxc_value bytes(const void *p,size_t n){return(gxc_value){.kind=GXC_BYTES,.bytes=(uint8_t *)p,.length=n};}
static void same(const gxc_value *v,const void *p,size_t n){assert(v&&v->kind==GXC_BYTES&&v->length==n&&(!n||!memcmp(v->bytes,p,n)));}
static gxc_value input(void){static uint8_t b[]={7};static char *names[]={"Count","Bytes","Order"};static gxc_value fields[]={{.kind=GXC_INT,.integer=2},{.kind=GXC_BYTES,.bytes=b,.length=1},{.kind=GXC_BYTES,.bytes=(uint8_t *)"caller",.length=6}};return(gxc_value){.kind=GXC_RECORD,.items=fields,.names=names,.length=3};}
static void output(const gxc_value *v){static uint8_t b[]={3,255,128};static const char order[]="order-var/order-init/state-var/state-init/api-var/api-init";assert(gxc_field(v,"Count")->integer==29);same(gxc_field(v,"Bytes"),b,sizeof b);same(gxc_field(v,"Order"),order,sizeof order-1);}
static gxc_value call(const char *name,const gxc_value *args,size_t n){gxc_value out={0};gxc_error error={0};assert(!goalchemy_invoke(name,args,n,NULL,&out,&error));gxc_error_free(&error);return out;}
static atomic_bool acquired,stopped,ack,released;
static void pause_ms(void){struct timespec delay={0,1000000};nanosleep(&delay,NULL);}
static void await_flag(const atomic_bool *flag){for(int i=0;i<10000&&!atomic_load(flag);i++)pause_ms();assert(atomic_load(flag));}
static int provider(void *state,const uint8_t *name,size_t names,const uint8_t *data,size_t n,const atomic_bool *cancel,gxc_value *response){(void)state;(void)data;(void)n;response->kind=GXC_BYTES;assert(names==4&&!memcmp(name,"hold",4));atomic_store(&acquired,true);while(!atomic_load(cancel))pause_ms();atomic_store(&stopped,true);await_flag(&ack);atomic_store(&released,true);return 1;}
typedef struct invocation{const char *name;gxc_value argument;size_t count;gxc_options options;gxc_value result;gxc_error error;int rc;atomic_bool calling,done;}invocation;
static void *invoke(void *arg){invocation *v=arg;atomic_store(&v->calling,true);v->rc=goalchemy_invoke(v->name,v->count?&v->argument:NULL,v->count,&v->options,&v->result,&v->error);atomic_store(&v->done,true);return NULL;}
static pthread_barrier_t barrier;
static void *overlap(void *arg){(void)arg;int rc=pthread_barrier_wait(&barrier);assert(rc==0||rc==PTHREAD_BARRIER_SERIAL_THREAD);gxc_value in=input(),out=call("Export",&in,1);output(&out);gxc_value_free(&out);return NULL;}
int main(void){GC_INIT();gxc_value in=input(),first={0};gxc_error error={0};assert(!tdf3_test_export(&in,&first,&error));output(&first);gxc_error_free(&error);assert(gxc_field(&in,"Count")->integer==2&&gxc_field(&in,"Bytes")->bytes[0]==7);
 gxc_value ignored={0};gxc_error saved={0};assert(goalchemy_invoke("ErrorGlobal",NULL,0,NULL,&ignored,&saved)==1);assert(ignored.kind==GXC_NIL);same(&saved.message,"saved",5);same(gxc_field(&saved.fields,"Code"),"saved",5);static uint8_t nine[]={9};same(gxc_field(&saved.fields,"Bytes"),nine,1);
 assert(!pthread_barrier_init(&barrier,NULL,13));pthread_t jobs[12];for(int i=0;i<12;i++)assert(!pthread_create(&jobs[i],NULL,overlap,NULL));int rc=pthread_barrier_wait(&barrier);assert(rc==0||rc==PTHREAD_BARRIER_SERIAL_THREAD);for(int i=0;i<12;i++)assert(!pthread_join(jobs[i],NULL));pthread_barrier_destroy(&barrier);
 for(int i=0;i<8;i++){GC_gcollect();gxc_value out=call("Export",&in,1);output(&out);gxc_value_free(&out);}output(&first);same(gxc_field(&saved.fields,"Code"),"saved",5);same(gxc_field(&saved.fields,"Bytes"),nine,1);
 ((gxc_value *)gxc_field(&first,"Bytes"))->bytes[0]=99;((gxc_value *)gxc_field(&saved.fields,"Bytes"))->bytes[0]=88;gxc_value again=call("Export",&in,1);output(&again);gxc_value_free(&again);gxc_error fresh={0};assert(goalchemy_invoke("ErrorGlobal",NULL,0,NULL,&ignored,&fresh)==1);same(gxc_field(&fresh.fields,"Bytes"),nine,1);gxc_error_free(&fresh);
 gxc_value native=call("Native",NULL,0);static const uint8_t digest[]={247,66,185,101,241,86,193,3,116,188,35,174,169,110,58,138,255,143,172,214,252,7,157,239,234,163,2,25,173,134,242,17};same(&native,digest,sizeof digest);gxc_value_free(&native);gxc_value crc_arg=bytes("123456789",9),crc=call("CRC",&crc_arg,1);assert(crc.kind==GXC_INT&&crc.integer==0xcbf43926);gxc_value_free(&crc);
 atomic_bool active_cancel=false,queue_cancel=true;invocation held={.name="Hold",.options={.canceled=&active_cancel,.provider=provider}};pthread_t holder,next,drop;assert(!pthread_create(&holder,NULL,invoke,&held));await_flag(&acquired);
 invocation queued={.name="Export",.argument=in,.count=1},canceled={.name="ErrorGlobal",.options={.canceled=&queue_cancel}};assert(!pthread_create(&next,NULL,invoke,&queued));assert(!pthread_create(&drop,NULL,invoke,&canceled));assert(!pthread_join(drop,NULL));assert(canceled.rc==6&&!atomic_load(&held.done)&&!atomic_load(&queued.done));gxc_error_free(&canceled.error);
 atomic_store(&active_cancel,true);goalchemy_wake();await_flag(&stopped);assert(!atomic_load(&held.done)&&!atomic_load(&queued.done)&&!atomic_load(&released));atomic_store(&ack,true);assert(!pthread_join(holder,NULL));assert(held.rc==1&&atomic_load(&released));same(&held.error.message,"context canceled",16);gxc_error_free(&held.error);assert(!pthread_join(next,NULL));assert(!queued.rc);output(&queued.result);gxc_value_free(&queued.result);gxc_error_free(&queued.error);
 again=call("Export",&in,1);output(&again);gxc_value_free(&again);gxc_value_free(&first);gxc_error_free(&saved);puts("PASS independent public archive/adapter/TUs/init/overlap/retained values/errors/cancellation/cleanup ACK/native capabilities/CRC/GC");return 0;
}
`
