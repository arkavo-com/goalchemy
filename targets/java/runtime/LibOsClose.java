package rt;
import java.io.IOException;
public final class LibOsClose {
 private LibOsClose() {}
 public static long libOsClose(long file){
  LibOsOpen.Entry e=LibOsOpen.forget(file);
  if(e==null)return 8;
  try{e.channel().close();return 0;}
  catch(IOException|RuntimeException ex){return LibOsOpen.status(ex);}
 }
}
