package rt;
import java.io.IOException;
import java.nio.channels.FileChannel;
import java.nio.file.*;
import java.nio.file.attribute.PosixFilePermission;
import java.nio.file.attribute.PosixFilePermissions;
import java.util.*;
public final class LibOsOpen {
 private LibOsOpen() {}
 public static final int MAX_CHUNK=1<<20;
 record Entry(FileChannel channel,boolean append){}
 private static final Map<Long,Entry> FILES=new HashMap<>();
 private static long last;
 static synchronized long register(FileChannel channel,boolean append){FILES.put(++last,new Entry(channel,append));return last;}
 static synchronized Entry lookup(long handle){return FILES.get(handle);}
 static synchronized Entry forget(long handle){return FILES.remove(handle);}
 static long status(Exception e){return e instanceof SecurityException?3:e instanceof IllegalArgumentException||e instanceof UnsupportedOperationException?8:LibOsReadFile.status(e);}
 static Set<PosixFilePermission> permissions(long perm){
  PosixFilePermission[] bits={PosixFilePermission.OTHERS_EXECUTE,PosixFilePermission.OTHERS_WRITE,PosixFilePermission.OTHERS_READ,PosixFilePermission.GROUP_EXECUTE,PosixFilePermission.GROUP_WRITE,PosixFilePermission.GROUP_READ,PosixFilePermission.OWNER_EXECUTE,PosixFilePermission.OWNER_WRITE,PosixFilePermission.OWNER_READ};
  Set<PosixFilePermission> mode=EnumSet.noneOf(PosixFilePermission.class);
  for(int i=0;i<9;i++)if((perm>>i&1)!=0)mode.add(bits[i]);
  return mode;
 }
 static FileChannel open(Path path,Set<OpenOption> options,long perm,boolean create)throws IOException{
  if(!create)return FileChannel.open(path,options);
  try{return FileChannel.open(path,options,PosixFilePermissions.asFileAttribute(permissions(perm)));}
  catch(UnsupportedOperationException e){return FileChannel.open(path,options);}
 }
 public static Object[] libOsOpen(String name,long flag,long perm){
  Object p=LibOsReadFile.path(name);
  if(p instanceof Long s)return new Object[]{0L,s};
  if((flag&~63L)!=0||(flag&3)==3)return new Object[]{0L,8L};
  Set<OpenOption> options=new HashSet<>();
  int access=(int)(flag&3);
  if(access!=1)options.add(StandardOpenOption.READ);
  if(access!=0)options.add(StandardOpenOption.WRITE);
  if((flag&8)!=0)options.add((flag&16)!=0?StandardOpenOption.CREATE_NEW:StandardOpenOption.CREATE);
  if((flag&32)!=0&&access!=0)options.add(StandardOpenOption.TRUNCATE_EXISTING);
  Path path=(Path)p;
  try{
   if(Files.isDirectory(path)&&access!=0)return new Object[]{0L,4L};
   return new Object[]{register(open(path,options,perm&0x1ff,(flag&8)!=0),(flag&4)!=0),0L};
  }catch(IOException|RuntimeException e){return new Object[]{0L,status(e)};}
 }
}
