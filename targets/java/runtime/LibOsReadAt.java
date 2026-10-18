package rt;
import java.io.IOException;
import java.nio.ByteBuffer;
public final class LibOsReadAt {
 private LibOsReadAt() {}
 public static Object[] libOsReadAt(long file,long offset,long max){
  LibOsOpen.Entry e=LibOsOpen.lookup(file);
  if(e==null||max<1||max>LibOsOpen.MAX_CHUNK||offset<0)return new Object[]{Slice.BYTE_NIL,false,8L};
  ByteBuffer buffer=ByteBuffer.allocate((int)max);
  try{return LibOsRead.chunk(buffer,e.channel().read(buffer,offset));}
  catch(IOException|RuntimeException ex){return new Object[]{Slice.BYTE_NIL,false,ex instanceof java.nio.channels.NonReadableChannelException?3L:LibOsOpen.status(ex)};}
 }
}
