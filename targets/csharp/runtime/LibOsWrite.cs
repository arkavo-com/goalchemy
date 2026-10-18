namespace Rt;
public static partial class R
{
 public static object[] libOsWrite(long file,Slice data)
 {
  var f=osLookup(file);
  if(f==null||data.l>MAX_CHUNK)return new object[]{0L,8L};
  try{
   if(f.append)f.stream.Seek(0,System.IO.SeekOrigin.End);
   if(data.l>0)f.stream.Write((byte[])data.a,data.o,data.l);
   f.stream.Flush();
   return new object[]{(long)data.l,0L};
  }catch(System.Exception e) when (osFailure(e)){return new object[]{0L,e is System.NotSupportedException?3L:osStatusOf(e,f.stream.Name)};}
 }
}
