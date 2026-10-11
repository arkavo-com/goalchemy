namespace Rt;

public static partial class R
{
    public static void libHttpExchangeResponse(GoTask task,Exchange ex)
    {
        object[] zero={0L,Slice.NIL,null};
        if(ex==null){task.rv=Native.failure(zero,"http: invalid request or limit");return;}
        if(ex.closed){task.rv=Native.failure(zero,EXCHANGE_CLOSED);return;}
        bool first=!ex.responded;ex.responded=true;
        if(ex.length>=0&&ex.written!=ex.length){ex.Abort();task.rv=Native.failure(zero,EXCHANGE_STATE);return;}
        if(first)ex.content?.End();
        exchangeOp(task,ex,zero,()=>{
            var response=ex.send.GetAwaiter().GetResult();
            if(ex.body==null)ex.body=response.Content.ReadAsStream(ex.cancel.Token);
            return new object[]{null,(long)(int)response.StatusCode,exchangeHeaders(response)};
        },v=>new object[]{(long)v[1],Native.strings(Array.ConvertAll((object[])v[2],x=>(string)x)),null});
    }
}
