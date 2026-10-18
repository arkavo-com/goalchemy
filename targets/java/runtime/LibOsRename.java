package rt;
import java.io.IOException;
import java.nio.file.*;
public final class LibOsRename {
 private LibOsRename() {}
 public static long libOsRename(String oldpath,String newpath){
  Object a=LibOsReadFile.path(oldpath),b=LibOsReadFile.path(newpath);
  if(a instanceof Long s)return s;
  if(b instanceof Long s)return s;
  try{
   try{Files.move((Path)a,(Path)b,StandardCopyOption.ATOMIC_MOVE);}
   catch(AtomicMoveNotSupportedException e){Files.move((Path)a,(Path)b,StandardCopyOption.REPLACE_EXISTING);}
   return 0;
  }catch(IOException|RuntimeException e){return LibOsOpen.status(e);}
 }
}
