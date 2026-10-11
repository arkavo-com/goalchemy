package rt;

import java.io.*;
import java.net.*;
import java.net.http.*;
import java.nio.charset.StandardCharsets;
import java.security.KeyStore;
import java.time.Duration;
import java.util.*;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.zip.GZIPInputStream;

/** Bounded JDK transport. Reader and client termination precede native cleanup ACK. */
public final class LibHttpDo {
    private LibHttpDo() {}
    // Package-private isolated native-resource fixture seam; production is identity.
    static volatile java.util.function.Function<InputStream,InputStream> bodyWrapper=java.util.function.Function.identity();
    private static final String INVALID="http: invalid request or limit";
    private static final Object[] ZERO={0L,Slice.NIL,Slice.BYTE_NIL,null};
    private static final Set<String> FORBIDDEN=Set.of("host","content-length","transfer-encoding","connection","proxy-authorization","proxy-connection","upgrade","trailer","te");
    private static boolean token(String value){if(value.isEmpty())return false;for(int i=0;i<value.length();i++){char c=value.charAt(i);if(!(c>='A'&&c<='Z'||c>='a'&&c<='z'||c>='0'&&c<='9'||"!#$%&'*+-.^_`|~".indexOf(c)>=0))return false;}return true;}
    private static boolean badValue(String value){for(int i=0;i<value.length();i++){char c=value.charAt(i);if(c==127||c<32&&c!='\t')return true;}return false;}
    static String canonical(String value){StringBuilder s=new StringBuilder();boolean upper=true;for(int i=0;i<value.length();i++){char c=value.charAt(i);s.append(upper?Character.toUpperCase(c):Character.toLowerCase(c));upper=c=='-';}return s.toString();}
    private static final class Lease {
        private Thread worker;private HttpClient client;private final ArrayList<InputStream> bodies=new ArrayList<>();
        private boolean stopped,sealed;private Throwable cleanupFault;private IOException readFailure;
        private final ArrayList<Thread> stoppers=new ArrayList<>();
        synchronized void attachWorker(){worker=Thread.currentThread();if(stopped)worker.interrupt();}
        synchronized void attachClient(HttpClient value)throws InterruptedException {client=value;if(stopped){value.shutdownNow();throw new InterruptedException("request stopped before send");}}
        synchronized void attachBody(InputStream value)throws InterruptedException {if(bodies.stream().noneMatch(reader->reader==value))bodies.add(value);if(stopped)throw new InterruptedException("request stopped before body read");}
        void stop(){
            Thread thread;
            synchronized(this){
                if(stopped)return;stopped=true;thread=worker;
                if(!sealed){
                    HttpClient http=client;InputStream[] readers=bodies.reversed().toArray(InputStream[]::new);
                    Thread helper=Thread.ofVirtual().name("goalchemy-http-stop").unstarted(()->{
                        for(InputStream reader:readers)try{reader.close();}catch(Throwable e){recordFault(e);}
                        try{if(http!=null)http.shutdownNow();}catch(Throwable e){recordFault(e);}
                    });
                    // Register/start atomically before an interrupted worker can seal cleanup.
                    stoppers.add(helper);try{helper.start();}catch(Throwable launchFailure){stoppers.remove(helper);recordFault(launchFailure);}
                }
            }
            if(thread!=null)thread.interrupt();
        }
        private synchronized void recordFault(Throwable error){if(cleanupFault==null||cleanupFault==readFailure&&error!=readFailure)cleanupFault=error;}
        synchronized void failed(IOException e){readFailure=e;}
        void cleanup() {
            HttpClient http;InputStream[] readers;Thread[] helpers;
            synchronized(this){sealed=true;http=client;readers=bodies.reversed().toArray(InputStream[]::new);helpers=stoppers.toArray(Thread[]::new);}
            Throwable fault=null;boolean interrupted=Thread.interrupted();
            for(InputStream reader:readers)try{reader.close();}catch(Throwable e){synchronized(this){if(e!=readFailure)fault=e;}}
            if(http!=null){
                try{http.close();}catch(Throwable e){fault=e;}
                // A cleanup exception never skips termination/resource settlement.
                if(!http.isTerminated()){
                    try{http.shutdownNow();}catch(Throwable e){fault=e;}
                    while(!http.isTerminated())try{http.awaitTermination(Duration.ofMillis(100));}catch(InterruptedException e){interrupted=true;}
                }
            }
            for(Thread helper:helpers)for(;;)try{helper.join();break;}catch(InterruptedException e){interrupted=true;}
            synchronized(this){bodies.clear();client=null;worker=null;stoppers.clear();if(cleanupFault!=null&&cleanupFault!=readFailure)fault=cleanupFault;}
            if(interrupted)Thread.currentThread().interrupt();
            if(fault!=null)throw new TaskSpawn.HostFault("HTTP cleanup: "+fault);
        }
    }
    private static final String BAD_TLS="http: invalid TLS option";
    private static final Set<String> OPTIONS=Set.of("client-cert","client-key","root-cas","insecure-skip-verify","unix-socket");
    static final String NO_UNIX="http: unix sockets are not supported on this host";
    private static Native.Reject badTls(){return new Native.Reject(BAD_TLS);}
    private static byte[] pemBlock(String text,String label)throws Native.Reject {
        String begin="-----BEGIN "+label+"-----",end="-----END "+label+"-----";int a=text.indexOf(begin);if(a<0)return null;int b=text.indexOf(end,a);if(b<0)throw badTls();
        try{return Base64.getMimeDecoder().decode(text.substring(a+begin.length(),b));}catch(IllegalArgumentException e){throw badTls();}
    }
    private static java.security.PrivateKey privateKey(String text)throws Exception {
        byte[] der=pemBlock(text,"PRIVATE KEY");
        if(der==null&&(der=pemBlock(text,"RSA PRIVATE KEY"))!=null)der=Der.seq(Der.integer(java.math.BigInteger.ZERO),Der.RSA_ALGORITHM,Der.value(4,der));
        else if(der==null&&(der=pemBlock(text,"EC PRIVATE KEY"))!=null){
            Der.Node root=Der.one(der);byte[] curve=null;if(root.tag()!=48)throw badTls();for(Der.Node n:root.children())if(n.tag()==160)curve=n.content();if(curve==null)throw badTls();
            der=Der.seq(Der.integer(java.math.BigInteger.ZERO),Der.seq(HexFormat.of().parseHex("06072a8648ce3d0201"),curve),Der.value(4,der));
        }
        if(der==null)throw badTls();var spec=new java.security.spec.PKCS8EncodedKeySpec(der);
        for(String algorithm:new String[]{"RSA","EC","Ed25519"})try{return java.security.KeyFactory.getInstance(algorithm).generatePrivate(spec);}catch(java.security.spec.InvalidKeySpecException e){}
        throw badTls();
    }
    private static java.security.cert.X509Certificate[] certificates(String text)throws Exception {
        var all=java.security.cert.CertificateFactory.getInstance("X.509").generateCertificates(new ByteArrayInputStream(text.getBytes(StandardCharsets.ISO_8859_1)));
        if(all.isEmpty())throw badTls();return all.stream().map(c->(java.security.cert.X509Certificate)c).toArray(java.security.cert.X509Certificate[]::new);
    }
    private static final class TrustAll extends javax.net.ssl.X509ExtendedTrustManager {
        public void checkClientTrusted(java.security.cert.X509Certificate[] c,String a){}
        public void checkServerTrusted(java.security.cert.X509Certificate[] c,String a){}
        public void checkClientTrusted(java.security.cert.X509Certificate[] c,String a,Socket s){}
        public void checkServerTrusted(java.security.cert.X509Certificate[] c,String a,Socket s){}
        public void checkClientTrusted(java.security.cert.X509Certificate[] c,String a,javax.net.ssl.SSLEngine e){}
        public void checkServerTrusted(java.security.cert.X509Certificate[] c,String a,javax.net.ssl.SSLEngine e){}
        public java.security.cert.X509Certificate[] getAcceptedIssuers(){return new java.security.cert.X509Certificate[0];}
    }
    /** The SSL context for DoWith options, or null for the default. */
    static javax.net.ssl.SSLContext sslContext(String[] options)throws Native.Reject {
        if(options.length==0)return null;if(options.length%2!=0)throw new Native.Reject(INVALID);
        Map<String,String> values=new HashMap<>();long total=0;
        for(int i=0;i<options.length;i+=2){total+=options[i].length()+options[i+1].length();if(total>1<<20||!OPTIONS.contains(options[i])||values.put(options[i],options[i+1])!=null)throw new Native.Reject(INVALID);}
        String skip=values.get("insecure-skip-verify"),socket=values.get("unix-socket");
        if(values.containsKey("client-cert")!=values.containsKey("client-key")||skip!=null&&!skip.equals("true")&&!skip.equals("false")||socket!=null&&(socket.isEmpty()||socket.length()>4096||socket.indexOf(0)>=0))throw new Native.Reject(INVALID);
        // java.net.http.HttpClient connects only over TCP.
        if(socket!=null)throw new Native.Reject(NO_UNIX);
        try{
            javax.net.ssl.KeyManager[] keys=null;javax.net.ssl.TrustManager[] trust=null;
            if(values.containsKey("client-cert")){
                var store=KeyStore.getInstance("PKCS12");store.load(null,null);char[] password=new char[0];
                store.setKeyEntry("client",privateKey(values.get("client-key")),password,certificates(values.get("client-cert")));
                var factory=javax.net.ssl.KeyManagerFactory.getInstance(javax.net.ssl.KeyManagerFactory.getDefaultAlgorithm());factory.init(store,password);keys=factory.getKeyManagers();
            }
            if("true".equals(skip))trust=new javax.net.ssl.TrustManager[]{new TrustAll()};
            else if(values.containsKey("root-cas")){
                var system=javax.net.ssl.TrustManagerFactory.getInstance(javax.net.ssl.TrustManagerFactory.getDefaultAlgorithm());system.init((KeyStore)null);
                var store=KeyStore.getInstance("PKCS12");store.load(null,null);int n=0;
                for(var m:system.getTrustManagers())if(m instanceof javax.net.ssl.X509TrustManager x)for(var c:x.getAcceptedIssuers())store.setCertificateEntry("system-"+(n++),c);
                for(var c:certificates(values.get("root-cas")))store.setCertificateEntry("extra-"+(n++),c);
                var factory=javax.net.ssl.TrustManagerFactory.getInstance(javax.net.ssl.TrustManagerFactory.getDefaultAlgorithm());factory.init(store);trust=factory.getTrustManagers();
            }
            if("true".equals(skip)&&values.containsKey("root-cas"))certificates(values.get("root-cas"));
            var context=javax.net.ssl.SSLContext.getInstance("TLS");context.init(keys,trust,null);return context;
        }catch(Native.Reject e){throw e;}catch(Exception e){throw badTls();}
    }
    public static void libHttpDo(TaskSpawn.Task task,StdContextErr.Context context,String method,String raw,
                                  Slice headers,Slice input,long max,long timeoutMillis) {
        httpDo(task,context,method,raw,headers,input,max,timeoutMillis,new String[0]);
    }
    /** A request builder for a checked URL and headers, as Do checks them. */
    static HttpRequest.Builder checkedRequest(String raw,String[] pairs)throws Native.Reject {
        if(raw.length()>8192||pairs.length%2!=0||pairs.length>32768)throw new Native.Reject(INVALID);
        URI uri;try{uri=new URI(Native.utf8(raw));}catch(URISyntaxException|IllegalArgumentException e){throw new Native.Reject(INVALID);}
        if(!Set.of("http","https").contains(uri.getScheme())||uri.getHost()==null||uri.getRawUserInfo()!=null||uri.getRawFragment()!=null||uri.getPort()>65535||uri.getPort()==0)throw new Native.Reject(INVALID);
        HttpRequest.Builder builder=HttpRequest.newBuilder(uri);int total=0;
        for(int i=0;i<pairs.length;i+=2){String name=pairs[i],value=pairs[i+1];total+=name.length()+value.length()+4;if(total>65536||!token(name)||badValue(value)||FORBIDDEN.contains(name.toLowerCase(Locale.ROOT)))throw new Native.Reject(INVALID);for(int j=0;j<value.length();j++)if(value.charAt(j)>127)throw new Native.Reject(INVALID);try{builder.header(name,value);}catch(IllegalArgumentException e){throw new Native.Reject(INVALID);}}
        return builder;
    }
    static void httpDo(TaskSpawn.Task task,StdContextErr.Context context,String method,String raw,
                                  Slice headers,Slice input,long max,long timeoutMillis,String[] options) {
        if(context==null||context.owner!=null&&context.owner!=TaskSpawn.sched||!method.equals("GET")&&!method.equals("POST")||max<0||max>Native.MAX||timeoutMillis<1||timeoutMillis>300000||raw.length()>8192){task.rv=Native.failure(ZERO,INVALID);return;}
        StdContextErr.observe(context);if(context.err!=null){task.rv=new Object[]{0L,Slice.NIL,Slice.BYTE_NIL,context.err};return;}
        Lease lease=new Lease();StdContextErr.Boundary boundary=new StdContextErr.Boundary(context,timeoutMillis*1000000L,lease::stop);
        final HttpRequest request;final boolean automaticGzip;final int limit=(int)max;final javax.net.ssl.SSLContext tls;
        try {
            byte[] body=Native.bytes(input);String[] pairs=Native.strings(headers);
            if(method.equals("GET")&&body.length!=0||pairs.length%2!=0||pairs.length>32768)throw new Native.Reject(INVALID);
            URI uri=new URI(Native.utf8(raw));if(!Set.of("http","https").contains(uri.getScheme())||uri.getHost()==null||uri.getRawUserInfo()!=null||uri.getRawFragment()!=null||uri.getPort()>65535||uri.getPort()==0)throw new Native.Reject(INVALID);
            HttpRequest.Builder builder=HttpRequest.newBuilder(uri);int total=0;boolean encoded=false;Set<String> seen=new java.util.HashSet<>();
            for(int i=0;i<pairs.length;i+=2){String name=pairs[i],value=pairs[i+1];total+=name.length()+value.length()+4;if(total>65536||!token(name)||badValue(value)||FORBIDDEN.contains(name.toLowerCase(Locale.ROOT)))throw new Native.Reject(INVALID);for(int j=0;j<value.length();j++)if(value.charAt(j)>127)throw new Native.Reject(INVALID);builder.header(name,value);String lower=name.toLowerCase(Locale.ROOT);if((lower.equals("accept-encoding")||lower.equals("range"))&&seen.add(lower)&&!value.isEmpty())encoded=true;}
            automaticGzip=!encoded;if(automaticGzip)builder.header("Accept-Encoding","gzip");
            long nanos=Math.max(1,boundary.deadline-TaskSpawn.sched.now());builder.timeout(Duration.ofNanos(nanos));builder.method(method,HttpRequest.BodyPublishers.ofByteArray(body));request=builder.build();
            tls=sslContext(options);
        }catch(Native.Reject|URISyntaxException|IllegalArgumentException e){boundary.close();task.rv=Native.failure(ZERO,e instanceof Native.Reject r&&(BAD_TLS.equals(r.getMessage())||NO_UNIX.equals(r.getMessage()))?r.getMessage():INVALID);return;}
        TaskSpawn.HostToken token=TaskSpawn.sched.registerHost(task,lease::stop,boundary::close,
            ()->boundary.error()==null?null:new Object[]{0L,Slice.NIL,Slice.BYTE_NIL,boundary.error()},
            values->{String error=(String)values[0];if(error!=null)return Native.failure(ZERO,error);return new Object[]{(Long)values[1],Native.strings((String[])values[2]),Native.slice((byte[])values[3]),null};});
        HttpWork work=new HttpWork(lease,token,request,automaticGzip,limit,tls);
        Thread worker=null;
        try {
            worker=Thread.ofVirtual().name("goalchemy-http").unstarted(work);worker.start();
        }catch(Throwable e){work.request=null;token.complete(new Object[0],new TaskSpawn.HostFault("HTTP submission: "+e));token.acknowledgeCleanup();return;}
        Native.acknowledgeAfter(worker,token);
    }
    private static final class HttpWork implements Runnable {
        final Lease lease;final TaskSpawn.HostToken token;HttpRequest request;final boolean automaticGzip;final int limit;final javax.net.ssl.SSLContext tls;
        HttpWork(Lease lease,TaskSpawn.HostToken token,HttpRequest request,boolean automaticGzip,int limit,javax.net.ssl.SSLContext tls){this.lease=lease;this.token=token;this.request=request;this.automaticGzip=automaticGzip;this.limit=limit;this.tls=tls;}
        public void run(){
                Object[] wire={null,0L,new String[0],new byte[0]};TaskSpawn.HostFault fault=null;
                try {
                    HttpRequest submitted=request;request=null;lease.attachWorker();
                    HttpClient.Builder clientBuilder=HttpClient.newBuilder();if(tls!=null)clientBuilder.sslContext(tls);
                    HttpClient client=clientBuilder.followRedirects(HttpClient.Redirect.NEVER).version(HttpClient.Version.HTTP_1_1).connectTimeout(Duration.ofSeconds(30)).build();lease.attachClient(client);
                    HttpResponse<InputStream> response=client.send(submitted,HttpResponse.BodyHandlers.ofInputStream());lease.attachBody(response.body());
                    int status=response.statusCode();boolean bodyless=status==204||status==304||status/100==1||response.headers().firstValue("Content-Length").map(v->v.trim().equals("0")).orElse(false);
                    boolean gzip=automaticGzip&&!bodyless&&response.headers().firstValue("Content-Encoding").orElse("").equalsIgnoreCase("gzip");
                    InputStream reader=response.body();
                    if(gzip){java.io.PushbackInputStream peek=new java.io.PushbackInputStream(reader);int first=peek.read();if(first>=0){peek.unread(first);reader=new GZIPInputStream(peek);}else reader=peek;lease.attachBody(reader);}
                    reader=bodyWrapper.apply(reader);if(reader==null)throw new IllegalStateException("null HTTP body wrapper");lease.attachBody(reader);
                    byte[] body=reader.readNBytes(limit+1);if(body.length>limit)wire[0]="http: response body exceeds limit";
                    else {
                        TreeMap<String,List<String>> exposed=new TreeMap<>();int total=0;
                        for(var e:response.headers().map().entrySet()){
                            if(gzip&&(e.getKey().equalsIgnoreCase("Content-Encoding")||e.getKey().equalsIgnoreCase("Content-Length")))continue;
                            String name=canonical(e.getKey());for(String value:e.getValue()){total+=name.length()+value.getBytes(StandardCharsets.ISO_8859_1).length+4;if(total>65536)throw new IOException("response headers exceed limit");}
                            exposed.put(name,e.getValue());
                        }
                        ArrayList<String> pairs=new ArrayList<>();for(var e:exposed.entrySet())for(String value:e.getValue()){pairs.add(e.getKey());pairs.add(Library.binaryString(value));}
                        wire=new Object[]{null,(long)response.statusCode(),pairs.toArray(String[]::new),body};
                    }
                }catch(HttpTimeoutException e){wire[0]="context deadline exceeded";}
                catch(IOException e){lease.failed(e);wire[0]="http: transport failure";}
                catch(InterruptedException e){wire[0]="http: transport failure";}
                catch(Throwable e){fault=new TaskSpawn.HostFault("HTTP adapter: "+e);}
                finally {
                    try{lease.cleanup();}catch(Throwable e){fault=new TaskSpawn.HostFault("HTTP resource cleanup: "+e);}
                }
                request=null;token.complete(wire,fault);
        }
    }
}
