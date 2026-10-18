package consumer;

import io.goalchemy.generated.Generated;
import java.util.concurrent.TimeUnit;

public final class LargeBufferConsumer {
    static void check(boolean ok) { if (!ok) throw new AssertionError("large buffer boundary"); }
    public static void main(String[] args) throws Exception {
        io.goalchemy.runtime.Library.length(100 << 20);
        try { io.goalchemy.runtime.Library.length(-1); throw new AssertionError("negative length accepted"); }
        catch (io.goalchemy.runtime.Library.Failure error) { check(error.kind.equals("invalid_argument")); }
        byte[] input = new byte[100 << 20];
        input[0] = 7; input[input.length - 1] = 19;
        byte[] output = Generated.Echo(input, null).completion().toCompletableFuture().get(30, TimeUnit.SECONDS);
        check(output.length == input.length && output[0] == 8 && output[output.length - 1] == 20);
        check(input[0] == 7 && input[input.length - 1] == 19);
        check(Generated.StringSize("x".repeat(65 << 20), null).completion().toCompletableFuture().get(30, TimeUnit.SECONDS) == 65 << 20);
        System.out.println("PASS large buffer library");
    }
}
