package rt;
import java.io.IOException;
import java.nio.file.*;
import java.nio.file.attribute.*;
public final class LibOsStat {
 private LibOsStat() {}
 static long mode(java.util.Set<PosixFilePermission> perms){
  PosixFilePermission[] bits={PosixFilePermission.OTHERS_EXECUTE,PosixFilePermission.OTHERS_WRITE,PosixFilePermission.OTHERS_READ,PosixFilePermission.GROUP_EXECUTE,PosixFilePermission.GROUP_WRITE,PosixFilePermission.GROUP_READ,PosixFilePermission.OWNER_EXECUTE,PosixFilePermission.OWNER_WRITE,PosixFilePermission.OWNER_READ};
  long mode=0;
  for(int i=0;i<9;i++)if(perms.contains(bits[i]))mode|=1L<<i;
  return mode;
 }
 public static Object[] libOsStat(String name){
  Object p=LibOsReadFile.path(name);
  if(p instanceof Long s)return new Object[]{0L,0L,false,s};
  Path path=(Path)p;
  try{
   BasicFileAttributes attrs;long mode;
   try{PosixFileAttributes posix=Files.readAttributes(path,PosixFileAttributes.class);attrs=posix;mode=mode(posix.permissions());}
   catch(UnsupportedOperationException e){attrs=Files.readAttributes(path,BasicFileAttributes.class);mode=attrs.isDirectory()?0x1ff:0x1b6;}
   return new Object[]{attrs.size(),mode,attrs.isDirectory(),0L};
  }catch(IOException|RuntimeException e){return new Object[]{0L,0L,false,LibOsOpen.status(e)};}
 }
}
