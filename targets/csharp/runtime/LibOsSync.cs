namespace Rt;
public static partial class R
{
 public static long libOsSync(long file)
 {
  var f=osLookup(file);
  if(f==null)return 8;
  try{f.stream.Flush(true);return 0;}
  catch(System.Exception e) when (osFailure(e)){return osStatusOf(e,f.stream.Name);}
 }
}
