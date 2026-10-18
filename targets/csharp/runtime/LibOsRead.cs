namespace Rt;
public static partial class R
{
 internal static object[] osChunk(byte[] buffer,int n)=>n<=0?new object[]{new Slice(System.Array.Empty<byte>(),0,0,0),true,0L}:new object[]{new Slice(buffer[..n],0,n,n),false,0L};
 public static object[] libOsRead(long file,long max)
 {
  var f=osLookup(file);
  if(f==null||max<1||max>MAX_CHUNK)return new object[]{Slice.BYTE_NIL,false,8L};
  var buffer=new byte[(int)max];
  try{return osChunk(buffer,f.stream.Read(buffer,0,buffer.Length));}
  catch(System.Exception e) when (osFailure(e)){return new object[]{Slice.BYTE_NIL,false,e is System.NotSupportedException?3L:osStatusOf(e,f.stream.Name)};}
 }
}
