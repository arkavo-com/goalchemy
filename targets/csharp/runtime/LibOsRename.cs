namespace Rt;
public static partial class R
{
 public static long libOsRename(string oldpath,string newpath)
 {
  var from=osPath(oldpath,out long bad);
  if(from==null)return bad;
  var to=osPath(newpath,out bad);
  if(to==null)return bad;
  try{
   if(System.IO.Directory.Exists(from)){System.IO.Directory.Move(from,to);return 0;}
   if(!System.IO.File.Exists(from))return fileAncestor(from)?5:1;
   System.IO.File.Move(from,to,true);
   return 0;
  }catch(System.Exception e) when (osFailure(e)){return osStatusOf(e,from);}
 }
}
