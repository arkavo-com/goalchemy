package rt;
import java.io.IOException;
public final class LibOsSeek {
 private LibOsSeek() {}
 public static Object[] libOsSeek(long file,long offset,long whence){
  LibOsOpen.Entry e=LibOsOpen.lookup(file);
  if(e==null||whence<0||whence>2)return new Object[]{0L,8L};
  try{
   long base=whence==0?0:whence==1?e.channel().position():e.channel().size();
   long target=base+offset;
   if(target<0)return new Object[]{0L,8L};
   e.channel().position(target);
   return new Object[]{target,0L};
  }catch(IOException|RuntimeException ex){return new Object[]{0L,LibOsOpen.status(ex)};}
 }
}
