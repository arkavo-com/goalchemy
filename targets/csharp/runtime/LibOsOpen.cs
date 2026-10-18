namespace Rt;
public static partial class R
{
 public const int MAX_CHUNK=1<<20;
 internal sealed class OsFile{public System.IO.FileStream stream;public bool append;}
 static readonly System.Collections.Generic.Dictionary<long,OsFile> osFiles=new();
 static long osLast;
 internal static long osRegister(System.IO.FileStream stream,bool append){lock(osFiles){osFiles[++osLast]=new OsFile{stream=stream,append=append};return osLast;}}
 internal static OsFile osLookup(long handle){lock(osFiles){return osFiles.TryGetValue(handle,out var f)?f:null;}}
 internal static OsFile osForget(long handle){lock(osFiles){if(!osFiles.TryGetValue(handle,out var f))return null;osFiles.Remove(handle);return f;}}
 internal static bool osFailure(System.Exception e)=>e is System.IO.IOException||e is System.UnauthorizedAccessException||e is System.Security.SecurityException||e is System.PlatformNotSupportedException||e is System.ArgumentException||e is System.NotSupportedException||e is System.ObjectDisposedException;
 internal static long osStatusOf(System.Exception e,string path)=>e is System.NotSupportedException||e is System.ObjectDisposedException?8:osStatus(e,path);
 internal static string osName(string path)=>System.Text.Encoding.Latin1.GetString(System.Text.Encoding.UTF8.GetBytes(path));
 public static object[] libOsOpen(string name,long flag,long perm)
 {
  var path=osPath(name,out long bad);
  if(path==null)return new object[]{0L,bad};
  if((flag&~63L)!=0||(flag&3)==3)return new object[]{0L,8L};
  int access=(int)(flag&3);
  var options=new System.IO.FileStreamOptions{Access=access==0?System.IO.FileAccess.Read:access==1?System.IO.FileAccess.Write:System.IO.FileAccess.ReadWrite,Share=System.IO.FileShare.ReadWrite|System.IO.FileShare.Delete,BufferSize=0};
  bool create=(flag&8)!=0,excl=(flag&16)!=0,trunc=(flag&32)!=0&&access!=0;
  options.Mode=create&&excl?System.IO.FileMode.CreateNew:create&&trunc?System.IO.FileMode.Create:create?System.IO.FileMode.OpenOrCreate:trunc?System.IO.FileMode.Truncate:System.IO.FileMode.Open;
  if(create&&!System.OperatingSystem.IsWindows())options.UnixCreateMode=(System.IO.UnixFileMode)(perm&0x1ff);
  try{
   if(System.IO.Directory.Exists(path))return new object[]{0L,4L};
   return new object[]{osRegister(new System.IO.FileStream(path,options),(flag&4)!=0),0L};
  }catch(System.IO.IOException) when (create&&excl&&System.IO.File.Exists(path)){return new object[]{0L,2L};}
  catch(System.Exception e) when (osFailure(e)){return new object[]{0L,osStatusOf(e,path)};}
 }
}
