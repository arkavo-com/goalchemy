namespace Rt;
public static partial class R
{
 public static long libOsClose(long file)
 {
  var f=osForget(file);
  if(f==null)return 8;
  try{f.stream.Dispose();return 0;}
  catch(System.Exception e) when (osFailure(e)){return osStatusOf(e,f.stream.Name);}
 }
}
