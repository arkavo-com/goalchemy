using System;
using Goalchemy.Generated;

static class LargeBufferConsumer
{
    static void Check(bool ok) { if (!ok) throw new Exception("large buffer boundary"); }
    static void Main()
    {
        byte[] input = new byte[100 << 20];
        input[0] = 7; input[input.Length - 1] = 19;
        byte[] output = GoProgram.Echo(input, null).Completion.WaitAsync(TimeSpan.FromSeconds(30)).GetAwaiter().GetResult();
        Check(output.Length == input.Length && output[0] == 8 && output[output.Length - 1] == 20);
        Check(input[0] == 7 && input[input.Length - 1] == 19);
        Check(GoProgram.StringSize(new string('x', 65 << 20), null).Completion.WaitAsync(TimeSpan.FromSeconds(30)).GetAwaiter().GetResult() == 65 << 20);
        Console.WriteLine("PASS large buffer library");
    }
}
