namespace Rt;
public static partial class R
{
 public static long libOsRemove(string name)
 {
  var path=osPath(name,out long bad);
  if(path==null)return bad;
  try{
   if(System.IO.Directory.Exists(path)){System.IO.Directory.Delete(path);return 0;}
   if(!System.IO.File.Exists(path))return fileAncestor(path)?5:1;
   System.IO.File.Delete(path);
   return 0;
  }catch(System.Exception e) when (osFailure(e)){return osStatusOf(e,path);}
 }
}
