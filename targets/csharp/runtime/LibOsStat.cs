namespace Rt;
public static partial class R
{
 static long osMode(string path,bool dir)
 {
  if(System.OperatingSystem.IsWindows())return dir?0x1ff:0x1b6;
  try{return (long)System.IO.File.GetUnixFileMode(path)&0x1ff;}catch(System.Exception e) when (osFailure(e)){return 0;}
 }
 public static object[] libOsStat(string name)
 {
  var path=osPath(name,out long bad);
  if(path==null)return new object[]{0L,0L,false,bad};
  try{
   if(System.IO.Directory.Exists(path))return new object[]{0L,osMode(path,true),true,0L};
   var info=new System.IO.FileInfo(path);
   if(!info.Exists)return new object[]{0L,0L,false,fileAncestor(path)?5L:1L};
   return new object[]{info.Length,osMode(path,false),false,0L};
  }catch(System.Exception e) when (osFailure(e)){return new object[]{0L,0L,false,osStatusOf(e,path)};}
 }
}
