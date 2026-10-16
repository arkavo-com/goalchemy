package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
)

func TestGeneratedJavaHandles(t *testing.T) {
	res := buildHandles(t)
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
	if err := os.WriteFile(filepath.Join(consumer, "HandlesConsumer.java"), []byte(javaHandlesConsumer), 0600); err != nil {
		t.Fatal(err)
	}
	jar := filepath.Join(out, "goalchemy-generated.jar")
	runTool(t, consumer, filepath.Join(bin, "javac"), "-encoding", "UTF-8", "-cp", jar, "-d", consumer, "HandlesConsumer.java")
	runTool(t, consumer, filepath.Join(bin, "java"), "-cp", consumer+string(os.PathListSeparator)+jar, "consumer.HandlesConsumer")
}

const javaHandlesConsumer = `package consumer;
import io.goalchemy.generated.Generated;
import io.goalchemy.runtime.*;
import java.util.*;
import java.util.concurrent.*;
public final class HandlesConsumer {
 static void check(boolean ok,String message){if(!ok)throw new AssertionError(message);}
 static <T>T get(Library.Operation<T> op)throws Exception{return op.completion().toCompletableFuture().get(30,TimeUnit.SECONDS);}
 static String kind(Library.Operation<?> op)throws Exception{
  try{get(op);return "";}catch(ExecutionException e){return e.getCause() instanceof Library.Failure f?f.kind:"other:"+e.getCause();}
 }
 public static void main(String[] args)throws Exception{
  // Instance state, init once, sentinel identity, native key and identity.
  var c=get(Generated.New("a",null));
  for(long i=1;i<=3;i++)check(get(c.Add(2,null))==2*i,"persistent field");
  check(kind(c.Add(-1,null)).equals("source"),"source error");
  var s=get(c.Snap(null));
  check(s.Name.equals("a")&&s.N==6&&s.Inits==1&&s.Created==1&&s.LastNeg,"instance globals");
  check(get(c.PublicKey(null)).startsWith("-----BEGIN PUBLIC KEY-----"),"native key survives its creating call");
  for(int i=0;i<3;i++)check(get(Generated.Inits(null))==1,"free functions keep fresh globals");
  check(get(c.Self(null))==c,"identity");
  var child=get(c.Child(null));
  check(get(child.Bump(null))==7&&get(child.Parent(null))==c&&get(c.Snap(null)).N==7,"derived handle");
  child.close();
  check(kind(child.Bump(null)).equals("closed"),"closed derived handle");
  check(get(c.Add(1,null))==8,"closing a derived handle keeps the instance");
  check(get(Generated.Nil(null))==null,"nil handle");
  c.close();
  // Handle parameters.
  var a=get(Generated.New("a",null));var b=get(a.Sibling("b",null));var other=get(Generated.New("other",null));
  get(a.Add(2,null));get(b.Add(3,null));
  check(get(Generated.Sum(a,b,null))==5,"handles of one instance");
  s=get(b.Snap(null));check(s.Created==2&&s.Inits==1,"sibling joins the instance");
  check(kind(Generated.Sum(a,other,null)).equals("instance_mismatch"),"mixed instances");
  check(get(Generated.Sum(null,null,null))==-1,"nil handle arguments");
  a.close();b.close();other.close();
  // Value receivers run on a copy of the handle's object.
  c=get(Generated.New("a",null));
  check(get(c.Name(null)).equals("a"),"value receiver on a handle with private state");
  c.close();
  var p=get(Generated.NewCoord(1,2,null));
  check(get(p.Total(null))==3&&get(p.Shift(5,null))==6&&get(p.Total(null))==3,"value receiver must not change the handle");
  var moved=get(p.Moved(10,null));
  check(moved.X==11&&moved.Y==2&&get(p.Total(null))==3,"value result is a copy");
  check(get(p.Less(moved,null))&&!get(p.Less(get(p.Moved(-1,null)),null)),"value parameter of the handle type");
  check(get(p.Move(1,null))==2&&get(p.Total(null))==4,"pointer receiver changes the handle");
  p.close();
  check(kind(get(Generated.NewCoord(-1,0,null)).closeAsync()).equals("source"),"value receiver Close");
  // Calls from several host threads are serialized; instances are independent.
  var x=get(Generated.New("x",null));var y=get(Generated.New("y",null));
  var pool=Executors.newFixedThreadPool(8);var jobs=new ArrayList<Future<?>>();
  for(int i=0;i<8;i++){var h=i%2==0?x:y;jobs.add(pool.submit(()->{for(int j=0;j<50;j++)get(h.Add(1,null));return null;}));}
  for(var j:jobs)j.get(120,TimeUnit.SECONDS);pool.shutdown();
  var sx=get(x.Snap(null));var sy=get(y.Snap(null));
  check(sx.N==200&&sy.N==200&&sx.Created==1&&sy.Created==1,"serialized calls");
  x.close();y.close();
  // Close.
  c=get(Generated.New("a",null));var sib=get(c.Sibling("bad-close",null));
  try(var scoped=c){}
  c.close();
  check(kind(c.Add(1,null)).equals("closed"),"call after close");
  check(get(sib.Snap(null)).Closes==1,"source Close ran once");
  check(kind(sib.closeAsync()).equals("source"),"source Close error");
  check(kind(sib.Snap(null)).equals("closed"),"failed Close still releases");
  // Cancellation and Close while pending.
  c=get(Generated.New("a",null));
  var op=c.Wait(100000,null);Thread.sleep(100);check(op.cancel(),"cancel requested");
  check(kind(op).equals("canceled"),"active cancellation");
  check(get(c.Add(1,null))==1,"usable after cancellation");
  var waited=c.Wait(100000,null);Thread.sleep(100);
  var queued=c.Add(1,null);Thread.sleep(50);
  c.close();
  check(kind(waited).equals("canceled"),"active call canceled by close");
  check(kind(queued).equals("closed"),"queued call failed by close");
  // Poisoning.
  c=get(Generated.New("a",null));
  check(kind(c.Boom(null)).equals("source_panic"),"panic");
  check(kind(c.Add(1,null)).equals("poisoned"),"poisoned instance");
  c.close();
  check(kind(c.Add(1,null)).equals("closed"),"closed poisoned");
  // Warnings: abandoned goroutines and unclosed handles.
  var warnings=new CopyOnWriteArrayList<String>();Library.warn=warnings::add;
  c=get(Generated.New("a",null));
  get(c.Leak(null));
  check(warnings.stream().anyMatch(m->m.contains("goroutine")),"abandoned goroutine warning");
  check(get(c.Add(1,null))==1,"usable after abandoning goroutines");
  c=null;
  for(int i=0;i<100&&warnings.stream().noneMatch(m->m.contains("Counter handle was not closed"));i++){System.gc();Thread.sleep(20);}
  check(warnings.stream().anyMatch(m->m.contains("Counter handle was not closed")),"finalizer warning "+warnings);
  System.out.println("PASS handles");
 }
}
`
