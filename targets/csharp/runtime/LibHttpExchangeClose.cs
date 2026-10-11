using System.Net.Http;
using System.IO;
namespace Rt;

/// Streamed lib/http exchanges on HttpClient; one thread per operation.
public static partial class R
{
    internal const string EXCHANGE_CLOSED="http: exchange closed",EXCHANGE_STATE="http: invalid exchange operation";
    /// Request body handoff: each write completes once the chunk reached the connection.
    internal sealed class ExchangeContent : HttpContent
    {
        readonly long length;readonly System.Threading.Channels.Channel<(byte[] data,System.Threading.Tasks.TaskCompletionSource done)> chunks=System.Threading.Channels.Channel.CreateBounded<(byte[],System.Threading.Tasks.TaskCompletionSource)>(1);
        internal ExchangeContent(long length){this.length=length;}
        internal async System.Threading.Tasks.Task Write(byte[] data,System.Threading.CancellationToken cancel)
        {
            var done=new System.Threading.Tasks.TaskCompletionSource(System.Threading.Tasks.TaskCreationOptions.RunContinuationsAsynchronously);
            await chunks.Writer.WriteAsync((data,done),cancel);
            await done.Task.WaitAsync(cancel);
        }
        internal void End()=>chunks.Writer.TryComplete();
        internal void Fail()=>chunks.Writer.TryComplete(new IOException("exchange aborted"));
        protected override async System.Threading.Tasks.Task SerializeToStreamAsync(Stream stream,System.Net.TransportContext context)
        {
            await foreach(var (data,done) in chunks.Reader.ReadAllAsync())
            {
                try{await stream.WriteAsync(data);await stream.FlushAsync();done.TrySetResult();}
                catch(Exception e){done.TrySetException(e);throw;}
            }
        }
        protected override bool TryComputeLength(out long size){size=length;return length>=0;}
    }
    public sealed class Exchange
    {
        internal readonly Scheduler owner;internal HostBoundary boundary;internal readonly long length;internal long written;internal bool closed,responded;
        internal readonly ExchangeContent content;internal readonly System.Threading.CancellationTokenSource cancel=new();
        internal HttpClient client;internal System.Threading.Tasks.Task<HttpResponseMessage> send;internal volatile Stream body;internal Action dispose;
        internal Exchange(Scheduler owner,long length,ExchangeContent content){this.owner=owner;this.length=length;this.content=content;}
        /// Nonblocking abort from any thread: blocked operations fail.
        internal void Abort(){content?.Fail();try{cancel.Cancel();}catch(ObjectDisposedException){}var b=body;if(b!=null)System.Threading.Tasks.Task.Run(()=>{try{b.Dispose();}catch{}});}
        internal void Release()
        {
            if(closed)return;closed=true;Abort();boundary?.Dispose();owner.disposers.Remove(dispose);
            var c=client;var s=send;System.Threading.Tasks.Task.Run(()=>{try{s?.Wait();}catch{}try{s?.Result.Dispose();}catch{}try{c?.Dispose();}catch{}});
        }
    }
    internal delegate object[] ExchangeWork();
    /// Runs work on a thread; its wire result starts with an error message or null.
    internal static void exchangeOp(GoTask task,Exchange ex,object[] zero,ExchangeWork work,Func<object[],object[]> decode)
    {
        if(ex.owner!=sched)throw new HostFault("HTTP exchange belongs to another owner");
        object[] failed(Box e){var r=(object[])zero.Clone();r[^1]=e;return r;}
        var token=sched.registerHost(task,ex.Abort,()=>{},()=>{var e=ex.boundary.error();return e==null?null:failed(e);},
            values=>{var e=ex.boundary.error();if(e!=null)return failed(e);return values[0]!=null?Native.failure(zero,(string)values[0]):decode(values);});
        System.Threading.Thread worker;
        try
        {
            worker=new System.Threading.Thread(()=>{
                object[] wire;HostFault fault=null;
                try{wire=work();}
                catch(Exception e)when(e is IOException||e is HttpRequestException||e is OperationCanceledException||e is AggregateException||e is System.Threading.Channels.ChannelClosedException||e is ObjectDisposedException){wire=new object[]{"http: transport failure"};}
                catch(Exception e){wire=Array.Empty<object>();fault=new HostFault("HTTP exchange adapter: "+e);}
                token.complete(wire,fault);
            }){IsBackground=true,Name="goalchemy-http"};
            worker.Start();
        }
        catch(Exception e){token.complete(Array.Empty<object>(),new HostFault("HTTP submission: "+e));token.acknowledgeCleanup();return;}
        Native.acknowledgeAfter(worker,token);
    }
    /// Sorted canonical headers, without Transfer-Encoding and Connection: close, as Go reports them.
    internal static string[] exchangeHeaders(HttpResponseMessage response)
    {
        var map=new SortedDictionary<string,List<string>>(StringComparer.Ordinal);int total=0;
        void add(System.Net.Http.Headers.HttpHeaders headers)
        {
            foreach(var header in headers.NonValidated)
            {
                string name=httpCanonical(header.Key);var values=new List<string>();foreach(var v in header.Value)values.Add(v);
                if(name=="Transfer-Encoding"||name=="Connection"&&values.Exists(v=>Array.Exists(v.Split(','),t=>t.Trim().Equals("close",StringComparison.OrdinalIgnoreCase))))continue;
                foreach(var v in values){total+=name.Length+v.Length+4;if(total>65536)throw new IOException("response headers exceed limit");}
                if(!map.TryGetValue(name,out var list))map[name]=list=new List<string>();list.AddRange(values);
            }
        }
        add(response.Headers);add(response.Content.Headers);
        var pairs=new List<string>();foreach(var p in map)foreach(var v in p.Value){Native.binary(v);pairs.Add(p.Key);pairs.Add(v);}
        return pairs.ToArray();
    }
    public static void libHttpExchangeClose(GoTask task,Exchange ex){ex?.Release();task.rv=Array.Empty<object>();}
}
