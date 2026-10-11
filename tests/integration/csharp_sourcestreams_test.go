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

func TestGeneratedCSharpSourceStreams(t *testing.T) {
	res := buildSourceStreams(t)
	out, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("csharp", res, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	dotnet := filepath.Join(driver.ToolchainRoot(), "dotnet", "dotnet")
	runTool(t, out, dotnet, "build", "main.csproj", "-c", "Release", "-o", "lib", "--nologo")
	project := `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net8.0</TargetFramework><ImplicitUsings>disable</ImplicitUsings></PropertyGroup><ItemGroup><Reference Include="main"><HintPath>` + filepath.Join(out, "lib", "main.dll") + `</HintPath></Reference></ItemGroup></Project>`
	for name, data := range map[string]string{"consumer.csproj": project, "SourceStreamsConsumer.cs": csharpSourceStreamsConsumer} {
		if err := os.WriteFile(filepath.Join(consumer, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runTool(t, consumer, dotnet, "build", "consumer.csproj", "-o", "bin", "--nologo")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, dotnet, filepath.Join(consumer, "bin", "consumer.dll"))
	cmd.Dir, cmd.Env = consumer, append(driver.ToolEnv(), fmt.Sprintf("THROUGHPUT_MIB=%d", throughputMiB(256)))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dotnet: %v\n%s", err, output)
	}
	t.Logf("%s", output)
}

const csharpSourceStreamsConsumer = `using System;
using System.IO;
using System.Linq;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using Rt;
using G=Goalchemy.Generated.GoProgram;
namespace Independent;
sealed class Source:Stream{
 long left,at;readonly byte[] block=SourceStreamsConsumer.Pattern(251*1024,0);
 public Source(long size){left=size;}
 public override bool CanRead=>true;public override bool CanSeek=>false;public override bool CanWrite=>false;
 public override long Length=>throw new NotSupportedException();public override long Position{get=>throw new NotSupportedException();set=>throw new NotSupportedException();}
 public override void Flush(){}public override long Seek(long o,SeekOrigin s)=>throw new NotSupportedException();public override void SetLength(long v)=>throw new NotSupportedException();public override void Write(byte[] b,int o,int n)=>throw new NotSupportedException();
 public override int Read(byte[] b,int o,int n){if(left==0)return 0;int k=(int)Math.Min(Math.Min(n,left),128<<10);Array.Copy(block,(int)(at%251),b,o,k);left-=k;at+=k;return k;}
}
public static class SourceStreamsConsumer {
 static void Check(bool ok,string message){if(!ok)throw new Exception(message);}
 static T Get<T>(Library.Operation<T> op)=>op.Completion.WaitAsync(TimeSpan.FromSeconds(120)).GetAwaiter().GetResult();
 static string Kind(Action a){try{a();return "";}catch(Library.Failure f){return f.Kind;}catch(IOException e) when (e.InnerException is Library.Failure f){return f.Kind;}catch(NotSupportedException){return "unsupported";}}
 public static byte[] Pattern(int n,int from)=>Enumerable.Range(0,n).Select(i=>(byte)((from+i)%251)).ToArray();
 static string Text(byte[] b)=>Encoding.UTF8.GetString(b);
 static byte[] All(Stream s){var m=new MemoryStream();s.CopyTo(m);return m.ToArray();}
 public static async Task Main(){
  using(var r=Get(G.Pattern(3<<20,null))){
   Check(r.CanRead&&!r.CanWrite&&!r.CanSeek,"kind");
   Check(Kind(()=>r.Write(new byte[1],0,1))=="unsupported","write on a reader");
   Check(All(r).SequenceEqual(Pattern(3<<20,0)),"CopyTo");
   Check(r.Read(new byte[4],0,4)==0,"EOF");
  }
  await using(var r=Get(G.Pattern(1000,null))){
   var m=new MemoryStream();await r.CopyToAsync(m);
   Check(m.ToArray().SequenceEqual(Pattern(1000,0)),"CopyToAsync");
  }
  using(var rs=Get(G.Text("hello, world",null))){
   Check(rs.Seek(7,SeekOrigin.Begin)==7&&rs.Position==7,"seek");
   Check(Text(All(rs))=="world","read after seek");
   Check(rs.Seek(-5,SeekOrigin.End)==7&&rs.Length==12,"seek from end");
  }
  using(var f=Get(G.Failing(10,null))){
   Check(f.Read(new byte[64],0,64)==10,"data before the error");
   Check(Kind(()=>f.Read(new byte[64],0,64))=="source","source error");
  }
  Check(Get(G.Nil(null))==null,"nil reader");
  var big=Pattern(3<<20,0);
  var rc=Get(G.Through(new MemoryStream(big),null));
  Check(All(rc).SequenceEqual(big),"host to source to host");
  await rc.CloseAsync();
  Check(Kind(()=>rc.Read(new byte[4],0,4))=="closed","read after close");
  rc.Dispose();

  using(var sink=Get(G.NewSink(true,null))){
   var w=Get(sink.Writer(null));
   Check(w.CanWrite&&!w.CanRead,"writer kind");
   var data=Pattern(5<<20,0);
   using(w){new MemoryStream(data).CopyTo(w);}
   Check(Get(sink.Closes(null))==1,"disposing the writer runs the source Close");
   Check(Kind(()=>w.Write(new byte[1],0,1))=="closed","write after close");
   var view=sink.AsStream();view.Write(Encoding.UTF8.GetBytes("tail"));
   Check(Get(sink.Count(null))==data.Length+4,"count");
   using var contents=Get(sink.Contents(null));
   var back=All(contents);
   Check(back.Take(data.Length).SequenceEqual(data)&&Text(back.Skip(data.Length).ToArray())=="tail","contents");
  }
  var doc=Get(G.Open(Encoding.UTF8.GetBytes("persistent document"),null));
  var ds=doc.AsStream();
  Check(ds.Seek(11,SeekOrigin.Begin)==11&&Text(All(ds))=="document","handle stream");
  Check(Get(doc.Reads(null))>=2,"reads");
  ds.Dispose();
  doc.Dispose();
  Check(Kind(()=>Get(doc.Reads(null)))=="closed","handle closed by its stream");

  int n=1<<20;
  using(var r=Get(G.Pattern(n,null))){
   int total=0;
   var ts=Enumerable.Range(0,8).Select(_=>Task.Run(()=>{var buf=new byte[4096];for(;;){int k=r.Read(buf,0,buf.Length);if(k==0)return;Interlocked.Add(ref total,k);}})).ToArray();
   Task.WaitAll(ts);
   Check(total==n,"concurrent reads "+total);
  }

  int mib=int.Parse(Environment.GetEnvironmentVariable("THROUGHPUT_MIB")??"256");
  long size=(long)mib<<20,count=0,peak=0;
  var sw=System.Diagnostics.Stopwatch.StartNew();
  uint h=2166136261;
  using(var r=Get(G.Through(new Source(size),null))){
   var buf=new byte[1<<20];
   for(int k;(k=r.Read(buf,0,buf.Length))>0;){for(int i=0;i<k;i++)h=(h^buf[i])*16777619;if((count>>24)!=((count+k)>>24))peak=Math.Max(peak,GC.GetTotalMemory(false));count+=k;}
  }
  double seconds=sw.Elapsed.TotalSeconds;
  uint want=2166136261;var src=new Source(size);var b2=new byte[64<<10];
  for(int k;(k=src.Read(b2,0,b2.Length))>0;)for(int i=0;i<k;i++)want=(want^b2[i])*16777619;
  Check(count==size&&h==want,"throughput copy "+count);
  Check(peak<512L<<20,"peak heap "+peak);
  Console.WriteLine($"throughput: {mib} MiB in {seconds:F1}s ({mib/seconds:F1} MiB/s), peak managed heap {peak>>20} MiB");
  Console.WriteLine("PASS source streams");
 }
}
`
