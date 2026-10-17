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
	"github.com/eugenioenko/goalchemy/internal/subset"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

func TestGeneratedCSharpPackageLibrary(t *testing.T) {
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
	// Add a real native resource barrier without changing the common API fixture.
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
	dotnet, env := filepath.Join(driver.ToolchainRoot(), "dotnet", "dotnet"), driver.ToolEnv()
	run := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, dotnet, args...)
		cmd.Dir, cmd.Env = dir, env
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dotnet %v: %v\n%s", args, err, data)
		} else {
			t.Log(string(data))
		}
	}
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%v", compact), func(t *testing.T) {
			out, consumer := t.TempDir(), t.TempDir()
			if ds := driver.EmitWithOptions("csharp", res, out, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
				t.Fatal(ds)
			}
			// SDK adapters remain ordinary root source members of main.csproj.
			adapter := `using Rt;using Generated=Goalchemy.Generated.GoProgram;
namespace IndependentAdapter;
public static class TDF3 {public static Library.Operation<Generated.Value> Export(Generated.Value value)=>Generated.Export(value,null);}
`
			if err := os.WriteFile(filepath.Join(out, "TDF3.cs"), []byte(adapter), 0600); err != nil {
				t.Fatal(err)
			}
			// Unlisted runtime sources must not enter the manifest-owned inventory.
			if err := os.WriteFile(filepath.Join(out, "rt", "types", "Caller.cs"), []byte("caller-owned unrelated source\n"), 0600); err != nil {
				t.Fatal(err)
			}
			run(t, out, "build", "main.csproj", "-c", "Release", "-o", "lib", "--nologo")
			if err := os.WriteFile(filepath.Join(consumer, "PackageLibraryConsumer.cs"), []byte(csharpPackageLibraryConsumer), 0600); err != nil {
				t.Fatal(err)
			}
			// The consumer references only the built DLL, never generated source.
			project := `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net8.0</TargetFramework><ImplicitUsings>disable</ImplicitUsings></PropertyGroup><ItemGroup><Reference Include="main"><HintPath>` + filepath.Join(out, "lib", "main.dll") + `</HintPath></Reference></ItemGroup></Project>`
			if err := os.WriteFile(filepath.Join(consumer, "consumer.csproj"), []byte(project), 0600); err != nil {
				t.Fatal(err)
			}
			run(t, consumer, "build", "consumer.csproj", "-o", "bin", "--nologo")
			run(t, consumer, filepath.Join(consumer, "bin", "consumer.dll"))
		})
	}
}

const csharpPackageLibraryConsumer = `using System;
using System.Linq;
using System.Reflection;
using System.Threading;
using System.Threading.Tasks;
using Rt;
using Generated=Goalchemy.Generated.GoProgram;
namespace Independent;
public static class PackageLibraryConsumer {
 const string ORDER="order-var/order-init/state-var/state-init/api-var/api-init";
 static void Check(bool ok,string message){if(!ok)throw new Exception(message);}
 static T Get<T>(Library.Operation<T> op)=>op.Completion.WaitAsync(TimeSpan.FromSeconds(10)).GetAwaiter().GetResult();
 static Library.Failure Failure<T>(Library.Operation<T> op,string kind){try{Get(op);throw new Exception("missing "+kind);}catch(Library.Failure failed){Check(failed.Kind==kind,"category "+failed.Kind+" expected "+kind);return failed;}}
 static Generated.Value Value()=>new(){Count=2,Bytes=new byte[]{7},Order="caller"};
 static void Output(Generated.Value value){Check(value.Count==29&&value.Order==ORDER&&value.Bytes.SequenceEqual(new byte[]{3,255,128}),"source-package initialization/closure/method/global result");}
 public static void Main(){
  var descriptorFields=typeof(Generated).GetFields(BindingFlags.Static|BindingFlags.NonPublic).Where(f=>f.FieldType==typeof(TypeDesc)).ToArray();
  var descriptors=descriptorFields.Select(f=>f.GetValue(null)).ToArray();
  Check(descriptors.Length>0&&descriptors.All(d=>d!=null),"single canonical descriptor graph");
  var input=Value();var first=Get(IndependentAdapter.TDF3.Export(input));Output(first);
  Check(input.Count==2&&input.Bytes[0]==7&&input.Order=="caller","input remains caller owned");
  var saved=Failure(Generated.ErrorGlobal(null),"source");Check((string)saved.Fields["Code"]=="saved"&&((byte[])saved.Fields["Bytes"])[0]==9,"owned source error fields");
  var start=new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
  var jobs=Enumerable.Range(0,12).Select(_=>Task.Run(async()=>{await start.Task;Output(await Generated.Export(Value(),null).Completion.WaitAsync(TimeSpan.FromSeconds(10)));})).ToArray();
  start.SetResult();Task.WhenAll(jobs).WaitAsync(TimeSpan.FromSeconds(15)).GetAwaiter().GetResult();
  Output(first);Check(((byte[])saved.Fields["Bytes"])[0]==9,"retained results/errors survive resets");
  for(int i=0;i<descriptors.Length;i++)Check(ReferenceEquals(descriptors[i],descriptorFields[i].GetValue(null)),"descriptor identity survives source reset");
  first.Bytes[0]=99;((byte[])saved.Fields["Bytes"])[0]=88;Output(Get(Generated.Export(Value(),null)));
  Check(((byte[])Failure(Generated.ErrorGlobal(null),"source").Fields["Bytes"])[0]==9,"error storage belongs to publication");
  var acquired=new ManualResetEventSlim();var stopped=new ManualResetEventSlim();Callback.Request request=null;
  var options=new Library.Options{Callbacks=new[]{new Callback.Registration("hold",r=>{request=r;r.OnStop(stopped.Set);acquired.Set();})}};
  var held=Generated.Hold(options);Check(acquired.Wait(5000),"real native callback acquired");
  var queuedInput=Value();var queued=Generated.Export(queuedInput,null);queuedInput.Count=99;queuedInput.Bytes[0]=99;
  var canceled=Generated.ErrorGlobal(null);Check(canceled.Cancel(),"queued cancellation requested");Failure(canceled,"canceled");
  Check(!held.Completion.IsCompleted&&!queued.Completion.IsCompleted,"queued cancellation isolated");
  Check(held.Cancel()&&stopped.Wait(5000),"active stop delivered");
  Check(!held.Completion.IsCompleted&&!queued.Completion.IsCompleted,"active completion waits real resource ACK");
  request.Resolve(new byte[]{1});Failure(held,"canceled");Output(Get(queued));Output(Get(Generated.Export(Value(),null)));
  Console.WriteLine("PASS independent native partial files/canonical descriptors/init/overlap/owned results/errors/cancellation/ACK/root adapter");
 }
}
`
