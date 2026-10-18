package rt;
import java.io.IOException;
import java.nio.ByteBuffer;
public final class LibOsWrite {
 private LibOsWrite() {}
 public static Object[] libOsWrite(long file,Slice data){
  LibOsOpen.Entry e=LibOsOpen.lookup(file);
  if(e==null||data.l>LibOsOpen.MAX_CHUNK)return new Object[]{0L,8L};
  byte[] body=data.a==null?new byte[0]:java.util.Arrays.copyOfRange((byte[])data.a,data.o,data.o+data.l);
  ByteBuffer buffer=ByteBuffer.wrap(body);
  try{
   if(e.append())e.channel().position(e.channel().size());
   while(buffer.hasRemaining())e.channel().write(buffer);
   return new Object[]{(long)body.length,0L};
  }catch(IOException|RuntimeException ex){return new Object[]{(long)buffer.position(),ex instanceof java.nio.channels.NonWritableChannelException?3L:LibOsOpen.status(ex)};}
 }
}
