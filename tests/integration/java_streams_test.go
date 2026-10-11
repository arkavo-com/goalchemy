package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
)

func TestGeneratedJavaStreams(t *testing.T) {
	res := buildStreams(t)
	out, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("java", res, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	bin := ""
	for _, entry := range driver.ToolEnv() {
		if strings.HasPrefix(entry, "JAVA_HOME=") {
			bin = filepath.Join(strings.TrimPrefix(entry, "JAVA_HOME="), "bin")
		}
	}
	runTool(t, out, "sh", "build.sh")
	if err := os.WriteFile(filepath.Join(consumer, "StreamsConsumer.java"), []byte(javaStreamsConsumer), 0600); err != nil {
		t.Fatal(err)
	}
	jar := filepath.Join(out, "goalchemy-generated.jar")
	runTool(t, consumer, filepath.Join(bin, "javac"), "-encoding", "UTF-8", "-cp", jar, "-d", consumer, "StreamsConsumer.java")
	runTool(t, consumer, filepath.Join(bin, "java"), "-cp", consumer+string(os.PathListSeparator)+jar, "consumer.StreamsConsumer")
}

const javaStreamsConsumer = `package consumer;
import io.goalchemy.generated.Generated;
import io.goalchemy.runtime.*;
import java.io.*;
import java.nio.charset.StandardCharsets;
import java.util.*;
import java.util.concurrent.*;
public final class StreamsConsumer {
 static void check(boolean ok,String message){if(!ok)throw new AssertionError(message);}
 static <T>T get(Library.Operation<T> op)throws Exception{return op.completion().toCompletableFuture().get(60,TimeUnit.SECONDS);}
 static String kind(Library.Operation<?> op)throws Exception{try{get(op);return "";}catch(ExecutionException e){return e.getCause() instanceof Library.Failure f?f.kind:"other:"+e.getCause();}}
 static byte[] utf8(String s){return s.getBytes(StandardCharsets.UTF_8);}
 public static void main(String[] args)throws Exception{
  byte[] big=new byte[3<<20];for(int i=0;i<big.length;i++)big[i]=(byte)(i%251);
  var out=new ByteArrayOutputStream();
  check(get(Generated.Copy(out,new ByteArrayInputStream(big),null))==big.length&&Arrays.equals(out.toByteArray(),big),"chunked copy");
  check(get(Generated.Sum(new byte[]{1,2,3},null))==6,"bytes reader");
  check(new String(get(Generated.Section(utf8("hello, world"),7,5,null)),StandardCharsets.UTF_8).equals("world"),"seek");
  check(kind(Generated.Section(utf8("short"),3,5,null)).equals("source"),"io.ErrUnexpectedEOF");
  int[] closes={0};
  var closing=new ByteArrayInputStream(utf8("abc")){@Override public void close(){closes[0]++;}};
  check(get(Generated.Drain(closing,null))==3&&closes[0]==1,"close");
  check(get(Generated.AtEOF(new byte[0],null)),"host EOF is io.EOF");
  var failing=new InputStream(){public int read()throws IOException{throw new IOException("disk on fire");}public int read(byte[] b,int o,int n)throws IOException{throw new IOException("disk on fire");}};
  check(get(Generated.FirstError(failing,null)).equals("disk on fire"),"host error");
  check(get(Generated.Nil(null,null)),"nil stream");
  var reentrant=new InputStream(){public int read(){return -1;}public int read(byte[] b,int o,int n)throws IOException{try{get(Generated.Sum(new byte[]{1},null));}catch(Exception e){throw new IOException(e.getCause()!=null?e.getCause().getMessage():e.getMessage());}return -1;}};
  check(get(Generated.FirstError(reentrant,null)).contains("reentrant"),"reentrant host stream");
  check(kind(Generated.Sum(new Object(),null)).equals("invalid_argument"),"unsupported host object");
  var doc=get(Generated.Open(utf8("persistent document"),null));
  check(get(doc.Size(null))==19,"size");
  check(new String(get(doc.ReadAt(11,8,null)),StandardCharsets.UTF_8).equals("document"),"stream kept by the instance");
  var whole=new ByteArrayOutputStream();
  check(get(doc.WriteTo(whole,null))==19&&whole.toString(StandardCharsets.UTF_8).equals("persistent document"),"WriteTo");
  doc.close();
  System.out.println("PASS streams");
 }
}
`
