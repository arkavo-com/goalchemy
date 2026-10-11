package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
)

func TestGeneratedCSharpStreams(t *testing.T) {
	res := buildStreams(t)
	out, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("csharp", res, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	dotnet := filepath.Join(driver.ToolchainRoot(), "dotnet", "dotnet")
	runTool(t, out, dotnet, "build", "main.csproj", "-c", "Release", "-o", "lib", "--nologo")
	project := `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net8.0</TargetFramework><ImplicitUsings>disable</ImplicitUsings></PropertyGroup><ItemGroup><Reference Include="main"><HintPath>` + filepath.Join(out, "lib", "main.dll") + `</HintPath></Reference></ItemGroup></Project>`
	for name, data := range map[string]string{"consumer.csproj": project, "StreamsConsumer.cs": csharpStreamsConsumer} {
		if err := os.WriteFile(filepath.Join(consumer, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runTool(t, consumer, dotnet, "build", "consumer.csproj", "-o", "bin", "--nologo")
	runTool(t, consumer, dotnet, filepath.Join(consumer, "bin", "consumer.dll"))
}

const csharpStreamsConsumer = `using System;
using System.IO;
using System.Linq;
using System.Text;
using Rt;
using G=Goalchemy.Generated.GoProgram;
namespace Independent;
sealed class Closing:MemoryStream{public int Closes;public Closing(byte[] b):base(b){}protected override void Dispose(bool d){Closes++;base.Dispose(d);}}
sealed class Failing:MemoryStream{public override int Read(byte[] b,int o,int n)=>throw new IOException("disk on fire");}
sealed class Reentrant:MemoryStream{public override int Read(byte[] b,int o,int n){try{G.Sum(new byte[]{1},null).Completion.GetAwaiter().GetResult();}catch(Library.Failure f){throw new IOException(f.Message);}return 0;}}
public static class StreamsConsumer {
 static void Check(bool ok,string message){if(!ok)throw new Exception(message);}
 static T Get<T>(Library.Operation<T> op)=>op.Completion.WaitAsync(TimeSpan.FromSeconds(60)).GetAwaiter().GetResult();
 static string Kind<T>(Library.Operation<T> op){try{Get(op);return "";}catch(Library.Failure f){return f.Kind;}}
 static byte[] Utf8(string s)=>Encoding.UTF8.GetBytes(s);
 public static void Main(){
  var big=Enumerable.Range(0,3<<20).Select(i=>(byte)(i%251)).ToArray();
  var output=new MemoryStream();
  Check(Get(G.Copy(output,new MemoryStream(big),null))==big.Length&&output.ToArray().SequenceEqual(big),"chunked copy");
  Check(Get(G.Sum(new byte[]{1,2,3},null))==6,"bytes reader");
  Check(Encoding.UTF8.GetString(Get(G.Section(Utf8("hello, world"),7,5,null)))=="world","seek");
  Check(Kind(G.Section(Utf8("short"),3,5,null))=="source","io.ErrUnexpectedEOF");
  var closing=new Closing(Utf8("abc"));Check(Get(G.Drain(closing,null))==3&&closing.Closes>=1,"close");
  Check(Get(G.AtEOF(new byte[0],null)),"host EOF is io.EOF");
  Check(Get(G.FirstError(new Failing(),null))=="disk on fire","host error");
  Check(Get(G.Nil(null,null)),"nil stream");
  Check(Get(G.FirstError(new Reentrant(),null)).Contains("reentrant"),"reentrant host stream");
  Check(Kind(G.Sum(new object(),null))=="invalid_argument","unsupported host object");
  var doc=Get(G.Open(Utf8("persistent document"),null));
  Check(Get(doc.Size(null))==19,"size");
  Check(Encoding.UTF8.GetString(Get(doc.ReadAt(11,8,null)))=="document","stream kept by the instance");
  var whole=new MemoryStream();Check(Get(doc.WriteTo(whole,null))==19&&Encoding.UTF8.GetString(whole.ToArray())=="persistent document","WriteTo");
  doc.Dispose();
  Console.WriteLine("PASS streams");
 }
}
`
