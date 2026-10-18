namespace Rt;
public static partial class R
{
 public static object[] libOsCreateTemp(string dir,string prefix,string suffix)
 {
  if((dir+prefix+suffix).IndexOf('\0')>=0||(prefix+suffix).IndexOf('/')>=0)return new object[]{0L,"",8L};
  string baseDir;
  if(dir.Length==0)baseDir=System.IO.Path.GetTempPath().TrimEnd(System.IO.Path.DirectorySeparatorChar);
  else{baseDir=osPath(dir,out long bad);if(baseDir==null)return new object[]{0L,"",bad};}
  string pre=osPath("x"+prefix,out _).Substring(1),suf=osPath("x"+suffix,out _).Substring(1);
  string attempted="";
  try{
   for(int i=0;;i++){
    attempted=System.IO.Path.Combine(baseDir,pre+System.IO.Path.GetRandomFileName().Replace(".","")+suf);
    var options=new System.IO.FileStreamOptions{Mode=System.IO.FileMode.CreateNew,Access=System.IO.FileAccess.ReadWrite,Share=System.IO.FileShare.ReadWrite|System.IO.FileShare.Delete,BufferSize=0};
    if(!System.OperatingSystem.IsWindows())options.UnixCreateMode=(System.IO.UnixFileMode)0x180;
    try{return new object[]{osRegister(new System.IO.FileStream(attempted,options),false),osName(attempted),0L};}
    catch(System.IO.IOException) when (i<10000&&System.IO.File.Exists(attempted)){}
   }
  }catch(System.Exception e) when (osFailure(e)){return new object[]{0L,osName(attempted),osStatusOf(e,attempted)};}
 }
}
