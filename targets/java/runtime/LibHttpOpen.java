package rt;

import java.net.http.*;
import java.time.Duration;

public final class LibHttpOpen {
    private LibHttpOpen() {}
    static final long MAX_EXCHANGE_MILLIS=24L*60*60*1000;
    public static void libHttpOpen(TaskSpawn.Task task,StdContextErr.Context context,String method,String raw,Slice headers,long length,long timeoutMillis,Slice options){
        final String invalid="http: invalid request or limit";Object[] zero={null,null};
        if(context==null||context.owner!=null&&context.owner!=TaskSpawn.sched||!method.equals("GET")&&!method.equals("POST")||length<-1||method.equals("GET")&&length!=0||timeoutMillis<1||timeoutMillis>MAX_EXCHANGE_MILLIS){task.rv=Native.failure(zero,invalid);return;}
        StdContextErr.observe(context);if(context.err!=null){task.rv=new Object[]{null,context.err};return;}
        HttpRequest.Builder builder;javax.net.ssl.SSLContext tls;
        try{
            builder=LibHttpDo.checkedRequest(raw,Native.strings(headers));
            String[] values;try{values=Native.strings(options);}catch(Native.Reject e){values=new String[1];}
            tls=LibHttpDo.sslContext(values);
        }catch(Native.Reject e){task.rv=Native.failure(zero,e.getMessage().startsWith("http: ")?e.getMessage():invalid);return;}
        LibHttpExchangeClose.Pipe pipe=length==0?null:new LibHttpExchangeClose.Pipe();
        LibHttpExchangeClose.Exchange ex=new LibHttpExchangeClose.Exchange(TaskSpawn.sched,length,pipe);
        ex.boundary=new StdContextErr.Boundary(context,timeoutMillis*1000000L,ex::abort);
        HttpRequest.BodyPublisher publisher=pipe==null?HttpRequest.BodyPublishers.noBody():length<0?HttpRequest.BodyPublishers.ofInputStream(()->pipe):HttpRequest.BodyPublishers.fromPublisher(HttpRequest.BodyPublishers.ofInputStream(()->pipe),length);
        HttpRequest request=builder.method(method,publisher).build();
        HttpClient.Builder clientBuilder=HttpClient.newBuilder();if(tls!=null)clientBuilder.sslContext(tls);
        ex.client=clientBuilder.followRedirects(HttpClient.Redirect.NEVER).version(HttpClient.Version.HTTP_1_1).connectTimeout(Duration.ofSeconds(30)).build();
        ex.future=ex.client.sendAsync(request,HttpResponse.BodyHandlers.ofInputStream());
        ex.dispose=ex::release;TaskSpawn.sched.disposers.add(ex.dispose);
        task.rv=new Object[]{ex,null};
    }
}
