namespace Rt;
public static partial class R
{
 public static object[] libOsSeek(long file,long offset,long whence)
 {
  var f=osLookup(file);
  if(f==null||whence<0||whence>2)return new object[]{0L,8L};
  try{
   long target=(whence==0?0:whence==1?f.stream.Position:f.stream.Length)+offset;
   if(target<0)return new object[]{0L,8L};
   return new object[]{f.stream.Seek(target,System.IO.SeekOrigin.Begin),0L};
  }catch(System.Exception e) when (osFailure(e)){return new object[]{0L,osStatusOf(e,f.stream.Name)};}
 }
}
