namespace Rt;

public static partial class R
{
    public static void libHttpExchangeWrite(GoTask task,Exchange ex,Slice data)
    {
        object[] zero={null};byte[] chunk;
        try{chunk=Native.bytes(data);}catch(Native.Reject){chunk=null;}
        if(ex==null||chunk==null||chunk.Length>1<<20){task.rv=Native.failure(zero,"http: invalid request or limit");return;}
        if(ex.closed){task.rv=Native.failure(zero,EXCHANGE_CLOSED);return;}
        if(ex.responded||ex.content==null&&chunk.Length!=0||ex.length>=0&&chunk.Length>ex.length-ex.written){task.rv=Native.failure(zero,EXCHANGE_STATE);return;}
        ex.written+=chunk.Length;if(chunk.Length==0){task.rv=new object[]{null};return;}
        exchangeOp(task,ex,zero,()=>{ex.content.Write(chunk,ex.cancel.Token).GetAwaiter().GetResult();return new object[]{null};},v=>new object[]{null});
    }
}
