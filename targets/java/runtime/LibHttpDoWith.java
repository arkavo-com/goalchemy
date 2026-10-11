package rt;

public final class LibHttpDoWith {
    private LibHttpDoWith() {}
    public static void libHttpDoWith(TaskSpawn.Task task,StdContextErr.Context context,String method,String raw,
                                      Slice headers,Slice input,long max,long timeoutMillis,Slice options) {
        String[] values;
        try{values=Native.strings(options);}catch(Native.Reject e){values=new String[1];}
        LibHttpDo.httpDo(task,context,method,raw,headers,input,max,timeoutMillis,values);
    }
}
