package rt;
import java.io.IOException;
public final class LibOsSync {
 private LibOsSync() {}
 public static long libOsSync(long file){
  LibOsOpen.Entry e=LibOsOpen.lookup(file);
  if(e==null)return 8;
  try{e.channel().force(true);return 0;}
  catch(IOException|RuntimeException ex){return LibOsOpen.status(ex);}
 }
}
