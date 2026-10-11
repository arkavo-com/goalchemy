using System.Net.Http;
namespace Rt;

public static partial class R
{
    const long MAX_EXCHANGE_MILLIS=24L*60*60*1000;
    public static void libHttpOpen(GoTask task,GoContext context,string method,string raw,Slice headers,long length,long timeoutMillis,Slice options)
    {
        const string invalid="http: invalid request or limit";object[] zero={null,null};
        if(context==null||context.owner!=null&&context.owner!=sched||method!="GET"&&method!="POST"||length< -1||method=="GET"&&length!=0||timeoutMillis<1||timeoutMillis>MAX_EXCHANGE_MILLIS){task.rv=Native.failure(zero,invalid);return;}
        observe(context);if(context.err!=null){task.rv=new object[]{null,context.err};return;}
        ExchangeContent content=length==0?null:new ExchangeContent(length);HttpRequestMessage request;HttpTls tls;
        try
        {
            string[] values;try{values=Native.strings(options);}catch(Native.Reject){values=new string[1];}
            tls=httpTls(values);
            request=httpCheckedRequest(method,raw,Native.strings(headers),content!=null?content:method=="POST"?new ByteArrayContent(Array.Empty<byte>()):null);
        }
        catch(Native.Reject e){task.rv=Native.failure(zero,e.Message==HTTP_BAD_TLS?HTTP_BAD_TLS:invalid);return;}
        var ex=new Exchange(sched,length,content);
        ex.boundary=new HostBoundary(context,timeoutMillis*1000000,ex.Abort);
        ex.client=new HttpClient(httpHandler(tls),true){Timeout=System.Threading.Timeout.InfiniteTimeSpan};
        if(length<0)request.Headers.TransferEncodingChunked=true;
        ex.send=ex.client.SendAsync(request,HttpCompletionOption.ResponseHeadersRead,ex.cancel.Token);
        ex.dispose=ex.Release;sched.disposers.Add(ex.dispose);
        task.rv=new object[]{ex,null};
    }
}
