package rt;

public final class LibHttpExchangeWrite {
    private LibHttpExchangeWrite() {}
    public static void libHttpExchangeWrite(TaskSpawn.Task task,LibHttpExchangeClose.Exchange ex,Slice data){
        Object[] zero={null};byte[] chunk;
        try{chunk=Native.bytes(data);}catch(Native.Reject e){chunk=null;}
        if(ex==null||chunk==null||chunk.length>1<<20){task.rv=Native.failure(zero,"http: invalid request or limit");return;}
        if(ex.closed){task.rv=Native.failure(zero,LibHttpExchangeClose.CLOSED);return;}
        if(ex.responded||ex.pipe==null&&chunk.length!=0||ex.length>=0&&chunk.length>ex.length-ex.written){task.rv=Native.failure(zero,LibHttpExchangeClose.STATE);return;}
        ex.written+=chunk.length;if(chunk.length==0){task.rv=new Object[]{null};return;}
        byte[] owned=chunk;
        LibHttpExchangeClose.op(task,ex,zero,()->{ex.pipe.write(owned);return new Object[]{null};},v->new Object[]{null});
    }
}
