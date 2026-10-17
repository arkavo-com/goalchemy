package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/emit/artifact"
	"github.com/eugenioenko/goalchemy/internal/subset"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

func TestGeneratedJavaPackageLibrary(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	mod := fmt.Sprintf("module packageconsumer\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => %s\n", root)
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte(mod), 0600); err != nil {
		t.Fatal(err)
	}
	// Reuse the shared three-package fixture in an included source module. A
	// test-only callback export provides actual native cancellation/ACK barriers.
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
	held := `package api
import("github.com/eugenioenko/goalchemy/lib/context";"github.com/eugenioenko/goalchemy/lib/callback")
func Hold(ctx context.Context)([]byte,error){return callback.Request(ctx,"hold",nil)}
`
	if err := os.WriteFile(filepath.Join(source, "api", "hold.go"), []byte(held), 0600); err != nil {
		t.Fatal(err)
	}
	res, ds := driver.Build(context.Background(), driver.Options{Dir: filepath.Join(source, "api"), Gate: subset.Cooperative})
	if diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	layout, err := artifact.NewLayout(res.IR)
	if err != nil {
		t.Fatal(err)
	}
	var holders []string
	for _, pkg := range layout.Packages {
		holders = append(holders, fmt.Sprintf("%q", "io.goalchemy.generated."+layout.Stem(pkg.Path)))
	}
	env := driver.ToolEnv()
	bin := ""
	for _, entry := range env {
		if strings.HasPrefix(entry, "JAVA_HOME=") {
			bin = filepath.Join(strings.TrimPrefix(entry, "JAVA_HOME="), "bin")
		}
	}
	run := func(t *testing.T, dir, name string, args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir, cmd.Env = dir, env
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, data)
		} else {
			t.Log(string(data))
		}
	}
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%v", compact), func(t *testing.T) {
			out, consumer := t.TempDir(), t.TempDir()
			if ds := driver.EmitWithOptions("java", res, out, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
				t.Fatal(ds)
			}
			if err := os.WriteFile(filepath.Join(out, "Caller.java"), []byte("caller-owned source outside inventory\n"), 0600); err != nil {
				t.Fatal(err)
			}
			run(t, out, "sh", "build.sh")
			suite := strings.ReplaceAll(javaPackageLibraryConsumer, "HOLDER_CLASSES", strings.Join(holders, ","))
			if err := os.WriteFile(filepath.Join(consumer, "PackageLibraryConsumer.java"), []byte(suite), 0600); err != nil {
				t.Fatal(err)
			}
			jar := filepath.Join(out, "goalchemy-generated.jar")
			// The consumer sees only the packaged public JAR, never generated source.
			run(t, consumer, filepath.Join(bin, "javac"), "-encoding", "UTF-8", "-cp", jar, "-d", consumer, "PackageLibraryConsumer.java")
			run(t, consumer, filepath.Join(bin, "java"), "-cp", consumer+string(os.PathListSeparator)+jar, "consumer.PackageLibraryConsumer")
		})
	}
}

const javaPackageLibraryConsumer = `package consumer;
import io.goalchemy.generated.Generated;
import io.goalchemy.runtime.*;
import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.*;
import java.lang.reflect.*;
public final class PackageLibraryConsumer {
 static final String ORDER="order-var/order-init/state-var/state-init/api-var/api-init";
 static void check(boolean ok,String message){if(!ok)throw new AssertionError(message);}
 static <T>T get(Library.Operation<T> operation)throws Exception{return operation.completion().toCompletableFuture().get(10,TimeUnit.SECONDS);}
 static Library.Failure failure(Library.Operation<?> operation,String kind)throws Exception{
  try{get(operation);throw new AssertionError("missing "+kind);}catch(ExecutionException error){check(error.getCause() instanceof Library.Failure,"typed error");var failed=(Library.Failure)error.getCause();check(failed.kind.equals(kind),"category "+failed.kind+" expected "+kind);return failed;}
 }
 static Generated.Value value(){var value=new Generated.Value();value.Count=2;value.Bytes=new byte[]{7};value.Order="caller";return value;}
 static void output(Generated.Value value){check(value.Count==29&&value.Order.equals(ORDER)&&Arrays.equals(value.Bytes,new byte[]{3,(byte)255,(byte)128}),"source-package initialization/closure/method/global result");}
 public static void main(String[] args)throws Exception{
  Class<?> support=Class.forName("io.goalchemy.generated._GoalchemySupport");
  var descriptors=new ArrayList<Field>();
  for(var field:support.getDeclaredFields())if(field.getType()==TypeDesc.class){field.setAccessible(true);check(field.get(null)==null,"leaf does not eagerly bind descriptors");descriptors.add(field);}
  check(!descriptors.isEmpty(),"canonical descriptor inventory");
  Class.forName("io.goalchemy.generated.Generated");
  for(var descriptor:descriptors)check(descriptor.get(null)!=null,"central descriptor binding after support load");
  for(String name:new String[]{HOLDER_CLASSES}){
   var holder=Class.forName(name);check(holder.getSuperclass()==support,"one canonical representation scope");
   for(var field:holder.getDeclaredFields())check(field.getType()!=TypeDesc.class,"holder must not duplicate descriptors");
  }
  var input=value();var first=get(Generated.Export(input,null));output(first);check(input.Count==2&&input.Bytes[0]==7&&input.Order.equals("caller"),"input remains caller owned");
  var saved=failure(Generated.ErrorGlobal(null),"source");check(saved.fields().get("Code").equals("saved")&&Arrays.equals((byte[])saved.fields().get("Bytes"),new byte[]{9}),"owned source error fields");
  var start=new CountDownLatch(1);var jobs=new ArrayList<CompletableFuture<Void>>();
  for(int i=0;i<12;i++)jobs.add(CompletableFuture.runAsync(()->{try{start.await();output(get(Generated.Export(value(),null)));}catch(Exception error){throw new CompletionException(error);}}));
  start.countDown();CompletableFuture.allOf(jobs.toArray(CompletableFuture[]::new)).get(15,TimeUnit.SECONDS);
  output(first);check(((byte[])saved.fields().get("Bytes"))[0]==9,"retained results/errors survive resets");
  first.Bytes[0]=99;((byte[])saved.fields().get("Bytes"))[0]=88;output(get(Generated.Export(value(),null)));check(((byte[])failure(Generated.ErrorGlobal(null),"source").fields().get("Bytes"))[0]==9,"error storage belongs to publication");
  var acquired=new CountDownLatch(1);var stopped=new CountDownLatch(1);var request=new AtomicReference<Callback.Request>();
  var options=new Library.Options();options.callbacks=new Callback.Registration[]{new Callback.Registration("hold",r->{request.set(r);r.onStop(stopped::countDown);acquired.countDown();})};
  var held=Generated.Hold(options);check(acquired.await(5,TimeUnit.SECONDS),"real native callback acquired");
  var queuedInput=value();var queued=Generated.Export(queuedInput,null);queuedInput.Count=99;queuedInput.Bytes[0]=99;
  var canceled=Generated.ErrorGlobal(null);check(canceled.cancel(),"queued cancellation requested");failure(canceled,"canceled");
  check(!held.completion().toCompletableFuture().isDone()&&!queued.completion().toCompletableFuture().isDone(),"queued cancellation isolated");
  check(held.cancel()&&stopped.await(5,TimeUnit.SECONDS),"active stop delivered");check(!held.completion().toCompletableFuture().isDone()&&!queued.completion().toCompletableFuture().isDone(),"active completion waits real resource ACK");
  request.get().resolve(new byte[]{1});failure(held,"canceled");output(get(queued));output(get(Generated.Export(value(),null)));
  System.out.println("PASS independent native holder files/canonical descriptors/init/overlap/owned results/errors/cancellation/ACK");
 }
}
`
