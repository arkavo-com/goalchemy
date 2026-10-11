using System.Net.Http;
using System.IO;
namespace Rt;

/// Dedicated bounded transport. Stream, client and cancellation work release before ACK.
public static partial class R
{
    static readonly object[] HTTP_ZERO={0L,Slice.NIL,Slice.BYTE_NIL,null};
    static readonly HashSet<string> HTTP_FORBIDDEN=new(StringComparer.OrdinalIgnoreCase){"Host","Content-Length","Transfer-Encoding","Connection","Proxy-Authorization","Proxy-Connection","Upgrade","Trailer","TE"};
    static bool httpToken(string value){if(value.Length==0)return false;foreach(char c in value)if(!(c>='A'&&c<='Z'||c>='a'&&c<='z'||c>='0'&&c<='9'||"!#$%&'*+-.^_`|~".Contains(c)))return false;return true;}
    static string httpCanonical(string value){var chars=value.ToCharArray();bool upper=true;for(int i=0;i<chars.Length;i++){chars[i]=upper?char.ToUpperInvariant(chars[i]):char.ToLowerInvariant(chars[i]);upper=chars[i]=='-';}return new string(chars);}
    internal static Func<Stream,Stream> httpBodyWrapper=value=>value;
    static readonly uint[] HTTP_CRC=httpCrcTable();
    static uint[] httpCrcTable(){var t=new uint[256];for(uint i=0;i<256;i++){uint c=i;for(int k=0;k<8;k++)c=(c&1)!=0?0xEDB88320u^(c>>1):c>>1;t[i]=c;}return t;}
    static bool httpTrailer(byte[] data,int n,byte[] output,int start){uint crc=0xFFFFFFFFu;for(int i=start;i<output.Length;i++)crc=HTTP_CRC[(crc^output[i])&0xFF]^(crc>>8);crc^=0xFFFFFFFFu;uint size=(uint)(output.Length-start);return BitConverter.ToUInt32(data,n-8)==crc&&BitConverter.ToUInt32(data,n-4)==size;}
    static byte[] httpGunzip(byte[] data,int offset,int n,int limit)
    {
        using var output=new MemoryStream();using var z=new System.IO.Compression.GZipStream(new MemoryStream(data,offset,n-offset,false),System.IO.Compression.CompressionMode.Decompress);
        var buffer=new byte[16384];for(;;){int r=z.Read(buffer,0,buffer.Length);if(r==0)break;if(output.Length+r>limit)throw new Native.Reject("http: response body exceeds limit");output.Write(buffer,0,r);}
        return output.ToArray();
    }
    // GZipStream returns a truncated final member without error. Complete members are
    // CRC-checked by zlib, so the last member's trailer decides: whole output for one
    // member, otherwise the last gzip header whose own decoding matches the trailer.
    static byte[] httpDecode(byte[] data,int n,int limit)
    {
        if(n==0)return Array.Empty<byte>();
        byte[] output=httpGunzip(data,0,n,limit);
        if(n>=18&&httpTrailer(data,n,output,0))return output;
        for(int c=n-18;c>0;c--)
        {
            if(data[c]!=0x1f||data[c+1]!=0x8b||data[c+2]!=8)continue;
            byte[] last;try{last=httpGunzip(data,c,n,limit);}catch(Exception e)when(e is InvalidDataException||e is Native.Reject){continue;}
            if(last.Length>output.Length||!output.AsSpan(output.Length-last.Length).SequenceEqual(last))continue;
            if(httpTrailer(data,n,output,output.Length-last.Length))return output;
        }
        throw new InvalidDataException("truncated or corrupt gzip stream");
    }
    sealed class HttpLease
    {
        internal readonly System.Threading.CancellationTokenSource cancel=new();readonly List<System.Threading.Tasks.Task> stoppers=new();bool stopped,sealedCleanup;Exception fault;
        internal void Stop(){lock(this){if(stopped)return;stopped=true;if(!sealedCleanup)try{stoppers.Add(cancel.CancelAsync());}catch(Exception e){fault=e;}}}
        internal void Cleanup(){System.Threading.Tasks.Task[] tasks;lock(this){sealedCleanup=true;tasks=stoppers.ToArray();}Exception failure=null;foreach(var task in tasks)try{task.GetAwaiter().GetResult();}catch(Exception e){failure=e;}try{cancel.Dispose();}catch(Exception e){failure=e;}lock(this){stoppers.Clear();failure=fault??failure;}if(failure!=null)throw new HostFault("HTTP cancellation cleanup: "+failure);}
    }
    const string HTTP_BAD_TLS="http: invalid TLS option";
    static readonly HashSet<string> HTTP_OPTIONS=new(StringComparer.Ordinal){"client-cert","client-key","root-cas","insecure-skip-verify"};
    sealed class HttpTls
    {
        internal System.Net.Security.SslStreamCertificateContext client;internal System.Security.Cryptography.X509Certificates.X509Certificate2Collection roots;internal bool insecure;
        internal bool Validate(System.Security.Cryptography.X509Certificates.X509Certificate certificate,System.Security.Cryptography.X509Certificates.X509Chain chain,System.Net.Security.SslPolicyErrors errors)
        {
            if(insecure||errors==System.Net.Security.SslPolicyErrors.None)return true;
            if(roots==null||errors!=System.Net.Security.SslPolicyErrors.RemoteCertificateChainErrors||certificate==null)return false;
            using var custom=new System.Security.Cryptography.X509Certificates.X509Chain();
            custom.ChainPolicy.TrustMode=System.Security.Cryptography.X509Certificates.X509ChainTrustMode.CustomRootTrust;custom.ChainPolicy.RevocationMode=System.Security.Cryptography.X509Certificates.X509RevocationMode.NoCheck;
            custom.ChainPolicy.CustomTrustStore.AddRange(roots);if(chain!=null)foreach(var element in chain.ChainElements)custom.ChainPolicy.ExtraStore.Add(element.Certificate);
            return custom.Build(new System.Security.Cryptography.X509Certificates.X509Certificate2(certificate));
        }
    }
    static System.Security.Cryptography.X509Certificates.X509Certificate2Collection httpCertificates(string pem)
    {
        var all=new System.Security.Cryptography.X509Certificates.X509Certificate2Collection();all.ImportFromPem(pem);if(all.Count==0)throw new Native.Reject(HTTP_BAD_TLS);return all;
    }
    static HttpTls httpTls(string[] options)
    {
        const string invalid="http: invalid request or limit";
        if(options.Length==0)return null;if(options.Length%2!=0)throw new Native.Reject(invalid);
        var values=new Dictionary<string,string>(StringComparer.Ordinal);long total=0;
        for(int i=0;i<options.Length;i+=2){total+=options[i].Length+options[i+1].Length;if(total>1<<20||!HTTP_OPTIONS.Contains(options[i])||!values.TryAdd(options[i],options[i+1]))throw new Native.Reject(invalid);}
        values.TryGetValue("insecure-skip-verify",out var skip);
        if(values.ContainsKey("client-cert")!=values.ContainsKey("client-key")||skip!=null&&skip!="true"&&skip!="false")throw new Native.Reject(invalid);
        var tls=new HttpTls{insecure=skip=="true"};
        try
        {
            if(values.TryGetValue("root-cas",out var roots))tls.roots=httpCertificates(roots);
            if(values.TryGetValue("client-cert",out var cert))
            {
                var chain=httpCertificates(cert);
                using var pair=System.Security.Cryptography.X509Certificates.X509Certificate2.CreateFromPem(cert,values["client-key"]);
                var leaf=new System.Security.Cryptography.X509Certificates.X509Certificate2(pair.Export(System.Security.Cryptography.X509Certificates.X509ContentType.Pkcs12));
                chain.RemoveAt(0);tls.client=System.Net.Security.SslStreamCertificateContext.Create(leaf,chain,true);
            }
        }
        catch(System.Security.Cryptography.CryptographicException){throw new Native.Reject(HTTP_BAD_TLS);}
        catch(ArgumentException){throw new Native.Reject(HTTP_BAD_TLS);}
        return tls;
    }
    public static void libHttpDo(GoTask task,GoContext context,string method,string raw,Slice headers,Slice input,long max,long timeoutMillis)
    {
        httpDo(task,context,method,raw,headers,input,max,timeoutMillis,Array.Empty<string>());
    }
    internal static void httpDo(GoTask task,GoContext context,string method,string raw,Slice headers,Slice input,long max,long timeoutMillis,string[] options)
    {
        const string invalid="http: invalid request or limit";
        if(context==null||context.owner!=null&&context.owner!=sched||method!="GET"&&method!="POST"||max<0||max>Native.MAX||timeoutMillis<1||timeoutMillis>300000||raw.Length>8192){task.rv=Native.failure(HTTP_ZERO,invalid);return;}
        observe(context);if(context.err!=null){task.rv=new object[]{0L,Slice.NIL,Slice.BYTE_NIL,context.err};return;}
        var lease=new HttpLease();var boundary=new HostBoundary(context,timeoutMillis*1000000,lease.Stop);HttpRequestMessage request=null;bool gzip=false;HttpTls tls=null;
        try
        {
            byte[] body=Native.bytes(input);string[] pairs=Native.strings(headers);if(method=="GET"&&body.Length!=0||pairs.Length%2!=0||pairs.Length>32768)throw new Native.Reject(invalid);
            string url=Native.utf8(raw);foreach(char c in url)if(c<=32||c==127||c=='\ufeff')throw new Native.Reject(invalid);
            if(!Uri.TryCreate(url,UriKind.Absolute,out var uri)||uri.Scheme!="http"&&uri.Scheme!="https"||string.IsNullOrEmpty(uri.Host)||uri.UserInfo.Length!=0||uri.Fragment.Length!=0||uri.Port<1||uri.Port>65535||url.Contains('\\'))throw new Native.Reject(invalid);
            request=new HttpRequestMessage(new HttpMethod(method),uri){Version=System.Net.HttpVersion.Version11,VersionPolicy=HttpVersionPolicy.RequestVersionExact};
            if(method=="POST"||body.Length!=0)request.Content=new ByteArrayContent(body);int total=0;bool encoded=false;var seen=new HashSet<string>();
            for(int i=0;i<pairs.Length;i+=2)
            {
                string name=pairs[i],value=pairs[i+1];total+=name.Length+value.Length+4;if(total>65536||!httpToken(name)||HTTP_FORBIDDEN.Contains(name))throw new Native.Reject(invalid);
                foreach(char c in value)if(c>255||c==127||c<32&&c!='\t')throw new Native.Reject(invalid);
                if(!request.Headers.TryAddWithoutValidation(name,value)){request.Content??=new ByteArrayContent(body);if(!request.Content.Headers.TryAddWithoutValidation(name,value))throw new Native.Reject(invalid);}
                if((name.Equals("Accept-Encoding",StringComparison.OrdinalIgnoreCase)||name.Equals("Range",StringComparison.OrdinalIgnoreCase))&&seen.Add(name.ToLowerInvariant())&&value.Length!=0)encoded=true;
            }
            gzip=!encoded;if(gzip)request.Headers.TryAddWithoutValidation("Accept-Encoding","gzip");
            tls=httpTls(options);
        }
        catch(Native.Reject e){request?.Dispose();boundary.Dispose();lease.Cleanup();task.rv=Native.failure(HTTP_ZERO,e.Message==HTTP_BAD_TLS?HTTP_BAD_TLS:invalid);return;}
        if(boundary.error()!=null){request.Dispose();boundary.Dispose();lease.Cleanup();task.rv=new object[]{0L,Slice.NIL,Slice.BYTE_NIL,context.err??CONTEXT_DEADLINE_EXCEEDED};return;}
        var token=sched.registerHost(task,lease.Stop,boundary.Dispose,()=>boundary.error()==null?null:new object[]{0L,Slice.NIL,Slice.BYTE_NIL,boundary.error()},
            values=>values[0]!=null?Native.failure(HTTP_ZERO,(string)values[0]):new object[]{(long)values[1],Native.strings(Array.ConvertAll((object[])values[2],v=>(string)v)),Native.slice((byte[])values[3]),null});
        var work=new HttpWork(lease,token,request,gzip,(int)max,tls);System.Threading.Thread worker;
        try{worker=new System.Threading.Thread(work.Run){IsBackground=true,Name="goalchemy-http"};worker.Start();}
        catch(Exception e){Exception fault=e;try{request.Dispose();}catch(Exception cleanup){fault=cleanup;}try{lease.Cleanup();}catch(Exception cleanup){fault=cleanup;}token.complete(Array.Empty<object>(),new HostFault("HTTP submission/cleanup: "+fault));token.acknowledgeCleanup();return;}
        Native.acknowledgeAfter(worker,token);
    }
    sealed class HttpWork
    {
        readonly HttpLease lease;readonly HostToken token;HttpRequestMessage request;readonly bool gzip;readonly int limit;readonly HttpTls tls;
        internal HttpWork(HttpLease lease,HostToken token,HttpRequestMessage request,bool gzip,int limit,HttpTls tls){this.lease=lease;this.token=token;this.request=request;this.gzip=gzip;this.limit=limit;this.tls=tls;}
        internal void Run()
        {
            HttpClient client=null;SocketsHttpHandler handler=null;HttpResponseMessage response=null;var bodies=new List<Stream>();Exception readFailure=null;HostFault fault=null;object[] result=null;string rejection=null;
            try
            {
                handler=new SocketsHttpHandler{AllowAutoRedirect=false,AutomaticDecompression=System.Net.DecompressionMethods.None,MaxResponseHeadersLength=64,RequestHeaderEncodingSelector=(name,message)=>System.Text.Encoding.Latin1,ResponseHeaderEncodingSelector=(name,message)=>System.Text.Encoding.Latin1};
                if(tls!=null){handler.SslOptions.ClientCertificateContext=tls.client;handler.SslOptions.RemoteCertificateValidationCallback=(sender,certificate,chain,errors)=>tls.Validate(certificate,chain,errors);}
                client=new HttpClient(handler,false){Timeout=System.Threading.Timeout.InfiniteTimeSpan};response=client.SendAsync(request,HttpCompletionOption.ResponseHeadersRead,lease.cancel.Token).GetAwaiter().GetResult();
                var raw=response.Content.ReadAsStreamAsync(lease.cancel.Token).GetAwaiter().GetResult();bodies.Add(raw);Stream body=httpBodyWrapper(raw)??throw new HostFault("null HTTP body wrapper");if(!bodies.Contains(body))bodies.Add(body);
                int code=(int)response.StatusCode;bool bodyless=code==204||code==304||code>=100&&code<200||response.Content.Headers.ContentLength==0;
                string encoding=null;if(response.Content.Headers.NonValidated.TryGetValues("Content-Encoding",out var encodings))foreach(var value in encodings){encoding=value;break;}
                bool decompressed=gzip&&!bodyless&&"gzip".Equals(encoding,StringComparison.OrdinalIgnoreCase);
                var headerMap=new SortedDictionary<string,string>(StringComparer.Ordinal);int count=0;
                foreach(var header in response.Headers){string value=string.Join(", ",header.Value);count+=header.Key.Length+value.Length+4;headerMap[httpCanonical(header.Key)]=value;}
                foreach(var header in response.Content.Headers){if(decompressed&&(header.Key.Equals("Content-Encoding",StringComparison.OrdinalIgnoreCase)||header.Key.Equals("Content-Length",StringComparison.OrdinalIgnoreCase)))continue;string value=string.Join(", ",header.Value);count+=header.Key.Length+value.Length+4;headerMap[httpCanonical(header.Key)]=value;}
                if(count>65536)throw new Native.Reject("http: response headers exceed limit");var pairs=new List<string>();foreach(var p in headerMap){Native.binary(p.Value);pairs.Add(p.Key);pairs.Add(p.Value);}
                long bound=decompressed?(long)limit+limit/64+65536:limit;
                using var output=new MemoryStream();var buffer=new byte[8192];for(;;){int n=body.ReadAsync(buffer.AsMemory(),lease.cancel.Token).AsTask().GetAwaiter().GetResult();if(n==0)break;if(output.Length+n>bound)throw new Native.Reject("http: response body exceeds limit");output.Write(buffer,0,n);}
                byte[] bytes=decompressed?httpDecode(output.GetBuffer(),(int)output.Length,limit):output.ToArray();result=new object[]{null,(long)response.StatusCode,pairs.ToArray(),bytes};
            }
            catch(Native.Reject e){rejection=e.Message;}catch(HttpRequestException e){rejection="http: transport failure";readFailure=e;}catch(IOException e){rejection="http: transport failure";readFailure=e;}catch(InvalidDataException e){rejection="http: transport failure";readFailure=e;}catch(OperationCanceledException e){rejection="http: transport canceled";readFailure=e;}catch(Exception e){fault=new HostFault("HTTP adapter: "+e);}
            finally
            {
                Exception cleanup=null;for(int i=bodies.Count-1;i>=0;i--)try{bodies[i].Dispose();}catch(Exception e){if(!ReferenceEquals(e,readFailure))cleanup=e;}bodies.Clear();try{response?.Dispose();}catch(Exception e){if(!ReferenceEquals(e,readFailure))cleanup=e;}
                try{request?.Dispose();}catch(Exception e){cleanup=e;}request=null;try{client?.Dispose();}catch(Exception e){cleanup=e;}try{handler?.Dispose();}catch(Exception e){cleanup=e;}try{lease.Cleanup();}catch(Exception e){cleanup=e;}if(cleanup!=null)fault=new HostFault("HTTP cleanup: "+cleanup);
            }
            token.complete(result??new object[]{rejection,0L,Array.Empty<string>(),Array.Empty<byte>()},fault);
        }
    }
}
