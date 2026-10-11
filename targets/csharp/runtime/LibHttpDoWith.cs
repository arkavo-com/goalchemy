namespace Rt;

public static partial class R
{
    public static void libHttpDoWith(GoTask task,GoContext context,string method,string raw,Slice headers,Slice input,long max,long timeoutMillis,Slice options)
    {
        string[] values;
        try{values=Native.strings(options);}catch(Native.Reject){values=new string[1];}
        httpDo(task,context,method,raw,headers,input,max,timeoutMillis,values);
    }
}
