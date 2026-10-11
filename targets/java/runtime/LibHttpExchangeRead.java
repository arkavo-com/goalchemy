package rt;

import java.util.Arrays;

public final class LibHttpExchangeRead {
    private LibHttpExchangeRead() {}
    public static void libHttpExchangeRead(TaskSpawn.Task task,LibHttpExchangeClose.Exchange ex,long max){
        Object[] zero={Slice.BYTE_NIL,false,null};
        if(ex==null||max<1||max>1<<20){task.rv=Native.failure(zero,"http: invalid request or limit");return;}
        if(ex.closed){task.rv=Native.failure(zero,LibHttpExchangeClose.CLOSED);return;}
        if(!ex.responded){task.rv=Native.failure(zero,LibHttpExchangeClose.STATE);return;}
        int limit=(int)max;
        LibHttpExchangeClose.op(task,ex,zero,()->{
            if(ex.body==null)ex.body=ex.future.get().body();
            byte[] buf=new byte[limit];int n;
            do{n=ex.body.read(buf,0,limit);}while(n==0);
            return n<0?new Object[]{null,new byte[0],true}:new Object[]{null,Arrays.copyOf(buf,n),false};
        },v->(Boolean)v[2]?new Object[]{Slice.BYTE_NIL,true,null}:new Object[]{Native.slice((byte[])v[1]),false,null});
    }
}
