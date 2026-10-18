package rt;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.*;
import java.nio.file.attribute.PosixFilePermissions;
import java.util.Set;
public final class LibOsCreateTemp {
 private LibOsCreateTemp() {}
 static String name(Path path){return new String(path.toString().getBytes(StandardCharsets.UTF_8),StandardCharsets.ISO_8859_1);}
 public static Object[] libOsCreateTemp(String dir,String prefix,String suffix){
  if((dir+prefix+suffix).indexOf('\0')>=0||(prefix+suffix).indexOf('/')>=0)return new Object[]{0L,"",8L};
  Object base=dir.isEmpty()?null:LibOsReadFile.path(dir);
  if(base instanceof Long s)return new Object[]{0L,"",s};
  String pre=new String(prefix.getBytes(StandardCharsets.ISO_8859_1),StandardCharsets.UTF_8),suf=new String(suffix.getBytes(StandardCharsets.ISO_8859_1),StandardCharsets.UTF_8);
  Path created=null;
  try{
   java.nio.file.attribute.FileAttribute<?>[] attrs;
   try{attrs=new java.nio.file.attribute.FileAttribute<?>[]{PosixFilePermissions.asFileAttribute(PosixFilePermissions.fromString("rw-------"))};}
   catch(RuntimeException e){attrs=new java.nio.file.attribute.FileAttribute<?>[0];}
   try{created=base==null?Files.createTempFile(pre,suf,attrs):Files.createTempFile((Path)base,pre,suf,attrs);}
   catch(UnsupportedOperationException e){created=base==null?Files.createTempFile(pre,suf):Files.createTempFile((Path)base,pre,suf);}
   java.nio.channels.FileChannel channel=java.nio.channels.FileChannel.open(created,Set.of(StandardOpenOption.READ,StandardOpenOption.WRITE));
   return new Object[]{LibOsOpen.register(channel,false),name(created),0L};
  }catch(IOException|RuntimeException e){
   String attempted=created!=null?name(created):e instanceof FileSystemException fse&&fse.getFile()!=null?new String(fse.getFile().getBytes(StandardCharsets.UTF_8),StandardCharsets.ISO_8859_1):"";
   return new Object[]{0L,attempted,LibOsOpen.status(e)};
  }
 }
}
