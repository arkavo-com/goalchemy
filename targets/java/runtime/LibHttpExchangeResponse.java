package rt;

import java.io.InputStream;
import java.net.http.HttpResponse;

public final class LibHttpExchangeResponse {
    private LibHttpExchangeResponse() {}
    public static void libHttpExchangeResponse(TaskSpawn.Task task,LibHttpExchangeClose.Exchange ex){
        Object[] zero={0L,Slice.NIL,null};
        if(ex==null){task.rv=Native.failure(zero,"http: invalid request or limit");return;}
        if(ex.closed){task.rv=Native.failure(zero,LibHttpExchangeClose.CLOSED);return;}
        boolean first=!ex.responded;ex.responded=true;
        if(ex.length>=0&&ex.written!=ex.length){ex.abort();task.rv=Native.failure(zero,LibHttpExchangeClose.STATE);return;}
        if(first&&ex.pipe!=null)ex.pipe.end();
        LibHttpExchangeClose.op(task,ex,zero,()->{
            HttpResponse<InputStream> response=ex.future.get();ex.body=response.body();
            return new Object[]{null,(long)response.statusCode(),LibHttpExchangeClose.headers(response.headers())};
        },v->new Object[]{v[1],Native.strings((String[])v[2]),null});
    }
}
