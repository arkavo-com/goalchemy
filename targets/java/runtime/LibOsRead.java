package rt;
import java.io.IOException;
import java.nio.ByteBuffer;
public final class LibOsRead {
 private LibOsRead() {}
 static Object[] chunk(ByteBuffer buffer,int n){
  if(n<0)return new Object[]{new Slice(new byte[0],0,0,0),true,0L};
  return new Object[]{new Slice(java.util.Arrays.copyOf(buffer.array(),n),0,n,n),false,0L};
 }
 public static Object[] libOsRead(long file,long max){
  LibOsOpen.Entry e=LibOsOpen.lookup(file);
  if(e==null||max<1||max>LibOsOpen.MAX_CHUNK)return new Object[]{Slice.BYTE_NIL,false,8L};
  ByteBuffer buffer=ByteBuffer.allocate((int)max);
  try{return chunk(buffer,e.channel().read(buffer));}
  catch(IOException|RuntimeException ex){return new Object[]{Slice.BYTE_NIL,false,ex instanceof java.nio.channels.NonReadableChannelException?3L:LibOsOpen.status(ex)};}
 }
}
