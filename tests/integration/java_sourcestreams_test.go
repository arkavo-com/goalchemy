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
)

func TestGeneratedJavaSourceStreams(t *testing.T) {
	res := buildSourceStreams(t)
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
	if err := os.WriteFile(filepath.Join(consumer, "SourceStreamsConsumer.java"), []byte(javaSourceStreamsConsumer), 0600); err != nil {
		t.Fatal(err)
	}
	jar := filepath.Join(out, "goalchemy-generated.jar")
	runTool(t, consumer, filepath.Join(bin, "javac"), "-encoding", "UTF-8", "-cp", jar, "-d", consumer, "SourceStreamsConsumer.java")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(bin, "java"), "-Xmx256m", "-cp", consumer+string(os.PathListSeparator)+jar, "consumer.SourceStreamsConsumer")
	cmd.Dir, cmd.Env = consumer, append(driver.ToolEnv(), fmt.Sprintf("THROUGHPUT_MIB=%d", throughputMiB(256)))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("java: %v\n%s", err, output)
	}
	t.Logf("%s", output)
}

const javaSourceStreamsConsumer = `package consumer;
import io.goalchemy.generated.Generated;
import io.goalchemy.runtime.*;
import java.io.*;
import java.nio.charset.StandardCharsets;
import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.*;
public final class SourceStreamsConsumer {
 static void check(boolean ok,String message){if(!ok)throw new AssertionError(message);}
 static <T>T get(Library.Operation<T> op)throws Exception{return op.completion().toCompletableFuture().get(120,TimeUnit.SECONDS);}
 static String kind(Callable<?> c){try{c.call();return "";}catch(Library.Failure f){return f.kind;}catch(IOException e){return e.getCause() instanceof Library.Failure f?f.kind:"io:"+e;}catch(ExecutionException e){return e.getCause() instanceof Library.Failure f?f.kind:"other:"+e.getCause();}catch(Exception e){return "other:"+e;}}
 static byte[] pattern(int n,int from){byte[] b=new byte[n];for(int i=0;i<n;i++)b[i]=(byte)((from+i)%251);return b;}
 static String text(byte[] b){return new String(b,StandardCharsets.UTF_8);}
 static final class Source extends InputStream {
  long left,at;final byte[] block=pattern(251*1024,0);
  Source(long size){left=size;}
  public int read(){byte[] one=new byte[1];return read(one,0,1)<0?-1:one[0]&0xff;}
  public int read(byte[] b,int off,int len){if(left==0)return -1;int n=(int)Math.min(Math.min(len,left),128<<10);System.arraycopy(block,(int)(at%251),b,off,n);left-=n;at+=n;return n;}
 }
 public static void main(String[] args)throws Exception{
  try(Library.SourceStream r=get(Generated.Pattern(3<<20,null))){
   check(r.canRead()&&!r.canWrite()&&!r.canSeek(),"kind");
   check(kind(()->{r.write(new byte[1],0,1);return null;}).equals("unsupported"),"write on a reader");
   check(Arrays.equals(r.asInputStream().readAllBytes(),pattern(3<<20,0)),"readAllBytes");
   check(r.read(new byte[4],0,4)==-1,"EOF");
  }
  try(Library.SourceStream r=get(Generated.Pattern(1000,null))){
   var out=new ByteArrayOutputStream();r.asInputStream().transferTo(out);
   check(Arrays.equals(out.toByteArray(),pattern(1000,0)),"transferTo");
  }
  try(Library.SourceStream rs=get(Generated.Text("hello, world",null))){
   check(rs.seek(7,0)==7,"seek");
   check(text(rs.asInputStream().readAllBytes()).equals("world"),"read after seek");
   check(rs.seek(-5,2)==7,"seek from end");
   check(get(rs.readAsync(64))!=null&&get(rs.readAsync(64))==null,"readAsync EOF");
  }
  try(Library.SourceStream f=get(Generated.Failing(10,null))){
   check(f.read(new byte[64],0,64)==10,"data before the error");
   check(kind(()->f.read(new byte[64],0,64)).equals("source"),"source error");
   check(kind(()->f.asInputStream().read(new byte[64])).equals("source"),"IOException cause");
  }
  check(get(Generated.Nil(null))==null,"nil reader");
  byte[] big=pattern(3<<20,0);
  Library.SourceStream rc=get(Generated.Through(new ByteArrayInputStream(big),null));
  check(Arrays.equals(rc.asInputStream().readAllBytes(),big),"host to source to host");
  rc.close();
  check(kind(()->rc.read(new byte[4],0,4)).equals("closed"),"read after close");
  rc.close();

  try(Generated.Sink sink=get(Generated.NewSink(true,null))){
   Library.SourceStream w=get(sink.Writer(null));
   check(w.canWrite()&&!w.canRead(),"writer kind");
   byte[] data=pattern(5<<20,0);
   try(OutputStream os=w.asOutputStream()){new ByteArrayInputStream(data).transferTo(os);}
   check(get(sink.Closes(null))==1,"closing the OutputStream closes the writer");
   check(kind(()->{w.write(new byte[1],0,1);return null;}).equals("closed"),"write after close");
   sink.io().write("tail".getBytes(StandardCharsets.UTF_8),0,4);
   check(get(sink.Count(null))==data.length+4,"count");
   try(Library.SourceStream contents=get(sink.Contents(null))){
    byte[] back=contents.asInputStream().readAllBytes();
    check(Arrays.equals(Arrays.copyOf(back,data.length),data)&&text(Arrays.copyOfRange(back,data.length,back.length)).equals("tail"),"contents");
   }
  }
  Generated.Doc doc=get(Generated.Open("persistent document".getBytes(StandardCharsets.UTF_8),null));
  InputStream in=doc.asInputStream();
  check(in.skip(11)==11,"skip seeks");
  check(text(in.readAllBytes()).equals("document"),"handle InputStream");
  check(get(doc.Reads(null))>=2,"reads");
  in.close();
  doc.close();
  check(kind(()->get(doc.Reads(null))).equals("closed"),"handle closed by its stream");

  int n=1<<20;
  try(Library.SourceStream r=get(Generated.Pattern(n,null))){
   AtomicInteger total=new AtomicInteger();
   Thread[] ts=new Thread[8];
   for(int i=0;i<ts.length;i++){ts[i]=new Thread(()->{byte[] buf=new byte[4096];for(;;){int k=r.read(buf,0,buf.length);if(k<0)return;total.addAndGet(k);}});ts[i].start();}
   for(Thread t:ts)t.join();
   check(total.get()==n,"concurrent reads "+total.get());
  }

  int mib=Integer.parseInt(System.getenv().getOrDefault("THROUGHPUT_MIB","256"));
  long size=(long)mib<<20;
  var crc=new java.util.zip.CRC32();long count=0,peak=0;
  long start=System.nanoTime();
  try(Library.SourceStream r=get(Generated.Through(new Source(size),null))){
   InputStream s=r.asInputStream();byte[] buf=new byte[1<<20];
   for(int k;(k=s.read(buf))>=0;){crc.update(buf,0,k);if((count>>24)!=((count+k)>>24)){Runtime rt=Runtime.getRuntime();peak=Math.max(peak,rt.totalMemory()-rt.freeMemory());}count+=k;}
  }
  double seconds=(System.nanoTime()-start)/1e9;
  var want=new java.util.zip.CRC32();var src=new Source(size);byte[] buf=new byte[64<<10];
  for(int k;(k=src.read(buf,0,buf.length))>=0;)want.update(buf,0,k);
  check(count==size&&crc.getValue()==want.getValue(),"throughput copy "+count);
  System.out.printf("throughput: %d MiB in %.1fs (%.1f MiB/s), peak heap %d MiB (-Xmx256m)%n",mib,seconds,mib/seconds,peak>>20);
  System.out.println("PASS source streams");
 }
}
`
