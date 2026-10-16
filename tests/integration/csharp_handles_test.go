package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
)

func TestGeneratedCSharpHandles(t *testing.T) {
	res := buildHandles(t)
	out, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("csharp", res, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	dotnet := filepath.Join(driver.ToolchainRoot(), "dotnet", "dotnet")
	runTool(t, out, dotnet, "build", "main.csproj", "-c", "Release", "-o", "lib", "--nologo")
	project := `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net8.0</TargetFramework><ImplicitUsings>disable</ImplicitUsings></PropertyGroup><ItemGroup><Reference Include="main"><HintPath>` + filepath.Join(out, "lib", "main.dll") + `</HintPath></Reference></ItemGroup></Project>`
	for name, data := range map[string]string{"consumer.csproj": project, "HandlesConsumer.cs": csharpHandlesConsumer} {
		if err := os.WriteFile(filepath.Join(consumer, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runTool(t, consumer, dotnet, "build", "consumer.csproj", "-o", "bin", "--nologo")
	runTool(t, consumer, dotnet, filepath.Join(consumer, "bin", "consumer.dll"))
}

const csharpHandlesConsumer = `using System;
using System.Collections.Concurrent;
using System.Linq;
using System.Threading;
using System.Threading.Tasks;
using Rt;
using G=Goalchemy.Generated.GoProgram;
namespace Independent;
public static class HandlesConsumer {
 static void Check(bool ok,string message){if(!ok)throw new Exception(message);}
 static T Get<T>(Library.Operation<T> op)=>op.Completion.WaitAsync(TimeSpan.FromSeconds(30)).GetAwaiter().GetResult();
 static string Kind<T>(Library.Operation<T> op){try{Get(op);return "";}catch(Library.Failure f){return f.Kind;}}
 static G.Counter Make(string name)=>Get(G.New(name,null));
 [System.Runtime.CompilerServices.MethodImpl(System.Runtime.CompilerServices.MethodImplOptions.NoInlining)]
 static void Leak(){var c=Make("a");Get(c.Add(1,null));}
 public static void Main(){
  // Instance state, init once, sentinel identity, native key and identity.
  var c=Make("a");
  for(long i=1;i<=3;i++)Check(Get(c.Add(2,null))==2*i,"persistent field");
  Check(Kind(c.Add(-1,null))=="source","source error");
  var s=Get(c.Snap(null));
  Check(s.Name=="a"&&s.N==6&&s.Inits==1&&s.Created==1&&s.LastNeg,"instance globals");
  Check(Get(c.PublicKey(null)).StartsWith("-----BEGIN PUBLIC KEY-----"),"native key survives its creating call");
  for(int i=0;i<3;i++)Check(Get(G.Inits(null))==1,"free functions keep fresh globals");
  Check(ReferenceEquals(Get(c.Self(null)),c),"identity");
  var child=Get(c.Child(null));
  Check(Get(child.Bump(null))==7&&ReferenceEquals(Get(child.Parent(null)),c)&&Get(c.Snap(null)).N==7,"derived handle");
  child.Dispose();
  Check(Kind(child.Bump(null))=="closed","closed derived handle");
  Check(Get(c.Add(1,null))==8,"closing a derived handle keeps the instance");
  Check(Get(G.Nil(null))==null,"nil handle");
  c.Dispose();
  // Handle parameters.
  var a=Make("a");var b=Get(a.Sibling("b",null));var other=Make("other");
  Get(a.Add(2,null));Get(b.Add(3,null));
  Check(Get(G.Sum(a,b,null))==5,"handles of one instance");
  s=Get(b.Snap(null));Check(s.Created==2&&s.Inits==1,"sibling joins the instance");
  Check(Kind(G.Sum(a,other,null))=="instance_mismatch","mixed instances");
  Check(Get(G.Sum(null,null,null))==-1,"nil handle arguments");
  a.Dispose();b.Dispose();other.Dispose();
  // Value receivers run on a copy of the handle's object.
  c=Make("a");
  Check(Get(c.Name(null))=="a","value receiver on a handle with private state");
  c.Dispose();
  var p=Get(G.NewCoord(1,2,null));
  Check(Get(p.Total(null))==3&&Get(p.Shift(5,null))==6&&Get(p.Total(null))==3,"value receiver must not change the handle");
  var m=Get(p.Moved(10,null));
  Check(m.X==11&&m.Y==2&&Get(p.Total(null))==3,"value result is a copy");
  Check(Get(p.Less(m,null))&&!Get(p.Less(Get(p.Moved(-1,null)),null)),"value parameter of the handle type");
  Check(Get(p.Move(1,null))==2&&Get(p.Total(null))==4,"pointer receiver changes the handle");
  p.Dispose();
  Check(Kind(Get(G.NewCoord(-1,0,null)).CloseAsync())=="source","value receiver Close");
  // Calls from several host threads are serialized; instances are independent.
  var x=Make("x");var y=Make("y");
  Parallel.For(0,8,new ParallelOptions{MaxDegreeOfParallelism=8},i=>{var h=i%2==0?x:y;for(int j=0;j<50;j++)Get(h.Add(1,null));});
  var sx=Get(x.Snap(null));var sy=Get(y.Snap(null));
  Check(sx.N==200&&sy.N==200&&sx.Created==1&&sy.Created==1,"serialized calls");
  x.Dispose();y.Dispose();
  // Close.
  c=Make("a");var sib=Get(c.Sibling("bad-close",null));
  using(c){}
  c.Dispose();
  Check(Kind(c.Add(1,null))=="closed","call after close");
  Check(Get(sib.Snap(null)).Closes==1,"source Close ran once");
  Check(Kind(sib.CloseAsync())=="source","source Close error");
  Check(Kind(sib.Snap(null))=="closed","failed Close still releases");
  // Cancellation and Close while pending.
  c=Make("a");
  var op=c.Wait(100000,null);Thread.Sleep(100);Check(op.Cancel(),"cancel requested");
  Check(Kind(op)=="canceled","active cancellation");
  Check(Get(c.Add(1,null))==1,"usable after cancellation");
  var waited=c.Wait(100000,null);Thread.Sleep(100);
  var queued=c.Add(1,null);Thread.Sleep(50);
  c.Dispose();
  Check(Kind(waited)=="canceled","active call canceled by close");
  Check(Kind(queued)=="closed","queued call failed by close");
  // Poisoning.
  c=Make("a");
  Check(Kind(c.Boom(null))=="panic","panic");
  Check(Kind(c.Add(1,null))=="poisoned","poisoned instance");
  c.Dispose();
  Check(Kind(c.Add(1,null))=="closed","closed poisoned");
  // Warnings: abandoned goroutines and unclosed handles.
  var warnings=new ConcurrentQueue<string>();Library.Warn=warnings.Enqueue;
  c=Make("a");Get(c.Leak(null));
  Check(warnings.Any(m=>m.Contains("goroutine")),"abandoned goroutine warning");
  Check(Get(c.Add(1,null))==1,"usable after abandoning goroutines");
  c.Dispose();
  Leak();
  for(int i=0;i<100&&!warnings.Any(m=>m.Contains("Counter handle was not closed"));i++){GC.Collect();GC.WaitForPendingFinalizers();Thread.Sleep(20);}
  Check(warnings.Any(m=>m.Contains("Counter handle was not closed")),"finalizer warning "+string.Join("|",warnings));
  Console.WriteLine("PASS handles");
 }
}
`
