namespace Rt;

public sealed class Deferred
{
    public readonly Fn f;
    public readonly object[] args;
    public readonly object fid;
    public readonly bool start;

    public Deferred(Fn f, object[] args, object fid, bool start = false)
    {
        this.f = f;
        this.args = args;
        this.fid = fid;
        this.start = start;
    }
}

/// <summary>Per-task recover state.</summary>
public class PanicState
{
    public GoPanic curPanic;
    public object deferTarget;
}

/// <summary>Program-level runtime: deferred calls, recover, closures, pointers
/// to fields, and the entry point reporting unrecovered panics as Go does.</summary>
public sealed class HostFault : Exception { public HostFault(string message) : base(message) { } }
public sealed class HostFatal : Exception { public HostFatal(string message) : base(message) { } }

public sealed class SourceStackFatal : Exception { }

public static class Program
{
    [ThreadStatic] static int sourceDepth;
    public readonly struct SourceScope : IDisposable
    {
        public void Dispose() => sourceDepth--;
    }
    // Managed guard, not a catch for uncatchable native CLR stack exhaustion.
    public static SourceScope enterSource()
    {
        if (sourceDepth >= 4096 || !System.Runtime.CompilerServices.RuntimeHelpers.TryEnsureSufficientExecutionStack()) throw new SourceStackFatal();
        sourceDepth++; return new SourceScope();
    }
    /// <summary>The guard of an async function: parked bodies keep no stack,
    /// so only the running thread's remaining stack is checked.</summary>
    public static void checkStack()
    {
        if (!System.Runtime.CompilerServices.RuntimeHelpers.TryEnsureSufficientExecutionStack()) throw new SourceStackFatal();
    }
    public static void resetPanicBinding()
    {
        MAIN_STATE.curPanic = null; MAIN_STATE.deferTarget = null;
        panicState = () => MAIN_STATE;
    }

    static readonly PanicState MAIN_STATE = new();

    public static Func<PanicState> panicState = () => MAIN_STATE;

    public static readonly TypeDesc PANIC_NIL_ERROR = new TypeDesc("*runtime.PanicNilError", "runtime_error",
        (a, b) => a == b, a => new IdKey(a),
        TypeDesc.MethodsOf("Error", new Fn(a => "runtime error: panic called with nil argument"), "RuntimeError", new Fn(a => null)),
        null, true);

    public static readonly TypeDesc STRING_TYPE = new TypeDesc("string", "string", (a, b) => (string)a == (string)b, a => a, null, "string", true);

    public static GoPanic goPanic(Box v) => new GoPanic(v ?? new Box(PANIC_NIL_ERROR, new object()));

    public static GoPanic catchPanic(Exception e)
    {
        if (e is GoPanic g) return g;
        System.Runtime.ExceptionServices.ExceptionDispatchInfo.Capture(e).Throw();
        return null;
    }

    public static void runDefers(List<Deferred> ds, GoPanic p)
    {
        var panicking = p;
        while (ds.Count > 0)
        {
            var d = ds[ds.Count - 1];
            ds.RemoveAt(ds.Count - 1);
            var st = panicState();
            var savedPanic = st.curPanic;
            var savedTarget = st.deferTarget;
            st.curPanic = panicking;
            st.deferTarget = d.fid;
            try
            {
                if (d.f == null) throw Panics.nilDeref();
                d.f.Call(d.args);
            }
            catch (GoPanic np)
            {
                if (np != panicking && np.prev == null) np.prev = panicking;
                panicking = np;
                continue;
            }
            finally
            {
                st.curPanic = savedPanic;
                st.deferTarget = savedTarget;
            }
            if (panicking != null && panicking.recovered) panicking = null;
        }
        if (panicking != null) throw panicking;
    }

    public static Box recover(object fid)
    {
        var st = panicState();
        var p = st.curPanic;
        if (p == null || p.recovered || !Equals(st.deferTarget, fid)) return null;
        p.recovered = true;
        return p.value;
    }

    /// <summary>A function value tagged with the identity recover compares against.</summary>
    public static Fn closure(object fid, Fn f) => new Fn(f.F, fid);

    public static Fn bound(object fid, Fn f, object recv) => new Fn(a =>
    {
        var all = new object[a.Length + 1];
        all[0] = recv;
        Array.Copy(a, 0, all, 1, a.Length);
        return f.F(all);
    }, fid);

    public static Box ichk(Box x) => x ?? throw Panics.nilDeref();

    public static Fn ibound(Box x, string id)
    {
        var b = ichk(x);
        var m = b.t.Methods[id];
        return bound(m.Fid, m, b.v);
    }

    public static Fn fnchk(Fn f) => f ?? throw Panics.nilDeref();

    public static object fid(Fn f) => f?.Fid;

    /// <summary>Calls a method from a type's table with the receiver first.</summary>
    public static object icall(Box x, string id, params object[] args)
    {
        var b = ichk(x);
        var all = new object[args.Length + 1];
        all[0] = b.v;
        Array.Copy(args, 0, all, 1, args.Length);
        return b.t.Methods[id].F(all);
    }

    public static GoPanic assertPanic(Box x, string iface, string target, string missing)
    {
        string msg;
        if (x == null) msg = "interface conversion: " + iface + " is nil, not " + target;
        else if (missing != null) msg = "interface conversion: " + x.t.Name + " is not " + target + ": missing method " + missing;
        else msg = "interface conversion: " + iface + " is " + x.t.Name + ", not " + target;
        return new GoPanic(new Box(Panics.TYPE_ASSERTION_ERROR, msg));
    }

    static System.Runtime.CompilerServices.ConditionalWeakTable<object, Dictionary<string, Ref>> refs = new();
    public static void clearFieldRefs() => refs.Clear();

    /// <summary>The canonical pointer to a struct field, so pointer equality holds.</summary>
    public static Ref fieldRef(object o, string k, Func<object, object> get, Action<object, object> set)
    {
        var m = refs.GetValue(o, _ => new Dictionary<string, Ref>());
        if (!m.TryGetValue(k, out var r))
        {
            r = new FieldRef(o, get, set);
            m[k] = r;
        }
        return r;
    }

    static string indented(string s) => s.Replace("\n", "\n\t");

    public static string formatPanicValue(Box v)
    {
        if (v == null) return "nil";
        if (v.t.Methods.TryGetValue("Error", out var err)) return indented((string)err.Call(v.v));
        if (v.t.Methods.TryGetValue("String", out var str)) return indented((string)str.Call(v.v));
        bool builtin = !v.t.Name.Contains('.');
        if (v.t.Basic == "string") return builtin ? indented((string)v.v) : v.t.Name + "(\"" + indented((string)v.v) + "\")";
        if (v.t.Basic == "float32" || v.t.Basic == "float64")
        {
            string s = Floats.print(v.v is float f ? (double)f : (double)v.v, v.t.Basic == "float32" ? 32 : 64);
            return builtin ? s : v.t.Name + "(" + s + ")";
        }
        if (v.t.Basic == "bool" || v.t.Basic == "int" || v.t.Basic == "uint")
        {
            string s = v.v is bool b ? (b ? "true" : "false") : v.t.Basic == "uint" ? ((ulong)(long)v.v).ToString() : v.v.ToString();
            return builtin ? s : v.t.Name + "(" + s + ")";
        }
        return "(" + v.t.Name + ") 0xc000000000";
    }

    public static string formatChain(GoPanic p)
    {
        string s = "";
        if (p.prev != null) s += formatChain(p.prev) + "\t";
        s += "panic: " + formatPanicValue(p.value);
        if (p.recovered) s += " [recovered]";
        return s + "\n";
    }

    public static void reportPanic(GoPanic p)
    {
        Out.stderr(formatChain(p));
        Environment.Exit(2);
    }

    /// <summary>Runs a body on a thread with a large stack so deep recursion behaves.</summary>
    public static void runLarge(Action body)
    {
        Exception fault = null;
        var t = new System.Threading.Thread(() =>
        {
            try
            {
                body();
            }
            catch (GoPanic p)
            {
                reportPanic(p);
            }
            catch (HostFatal e)
            {
                Out.stderr("fatal error: " + e.Message + "\n"); Environment.Exit(2);
            }
            catch (SourceStackFatal)
            {
                Out.stderr("runtime: goroutine stack exceeds limit\nfatal error: stack overflow\n");
                Environment.Exit(2);
            }
            catch (Exception e)
            {
                fault = e;
            }
        }, 1 << 30);
        t.Start();
        t.Join();
        if (fault != null)
        {
            Console.Error.WriteLine(fault);
            Environment.Exit(1);
        }
    }

    public static void main(Action entry)
    {
        runLarge(entry);
        Environment.Exit(0);
    }
}
