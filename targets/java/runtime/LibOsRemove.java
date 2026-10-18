package rt;
import java.io.IOException;
import java.nio.file.*;
public final class LibOsRemove {
 private LibOsRemove() {}
 public static long libOsRemove(String name){
  Object p=LibOsReadFile.path(name);
  if(p instanceof Long s)return s;
  try{Files.delete((Path)p);return 0;}
  catch(IOException|RuntimeException e){return LibOsOpen.status(e);}
 }
}
