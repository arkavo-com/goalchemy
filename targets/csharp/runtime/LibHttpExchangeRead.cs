namespace Rt;

public static partial class R
{
    public static void libHttpExchangeRead(GoTask task,Exchange ex,long max)
    {
        object[] zero={Slice.BYTE_NIL,false,null};
        if(ex==null||max<1||max>1<<20){task.rv=Native.failure(zero,"http: invalid request or limit");return;}
        if(ex.closed){task.rv=Native.failure(zero,EXCHANGE_CLOSED);return;}
        if(!ex.responded){task.rv=Native.failure(zero,EXCHANGE_STATE);return;}
        int limit=(int)max;
        exchangeOp(task,ex,zero,()=>{
            var body=ex.body??=ex.send.GetAwaiter().GetResult().Content.ReadAsStream(ex.cancel.Token);
            var buf=new byte[limit];
            int n=body.ReadAsync(buf,0,limit,ex.cancel.Token).GetAwaiter().GetResult();
            return n==0?new object[]{null,Array.Empty<byte>(),true}:new object[]{null,buf[..n],false};
        },v=>(bool)v[2]?new object[]{Slice.BYTE_NIL,true,null}:new object[]{Native.slice((byte[])v[1]),false,null});
    }
}
