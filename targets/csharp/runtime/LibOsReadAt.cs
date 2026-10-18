namespace Rt;
public static partial class R
{
 public static object[] libOsReadAt(long file,long offset,long max)
 {
  var f=osLookup(file);
  if(f==null||max<1||max>MAX_CHUNK||offset<0)return new object[]{Slice.BYTE_NIL,false,8L};
  var buffer=new byte[(int)max];
  try{return osChunk(buffer,System.IO.RandomAccess.Read(f.stream.SafeFileHandle,buffer,offset));}
  catch(System.Exception e) when (osFailure(e)){return new object[]{Slice.BYTE_NIL,false,e is System.NotSupportedException?3L:osStatusOf(e,f.stream.Name)};}
 }
}
