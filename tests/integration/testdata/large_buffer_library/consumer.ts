import * as g from './main.ts';

function check(ok: boolean): void { if (!ok) throw new Error('large buffer boundary'); }
const input = new Uint8Array(100 << 20);
input[0] = 7; input[input.length - 1] = 19;
const output = await g.Echo(input);
check(output !== null && output.length === input.length && output[0] === 8 && output[output.length - 1] === 20);
check(input[0] === 7 && input[input.length - 1] === 19);
check(await g.StringSize('x'.repeat(65 << 20)) === BigInt(65 << 20));
let rejected = false;
try { await g.Echo([] as unknown as Uint8Array); } catch (e) { rejected = e instanceof g.LibraryError && e.kind === 'invalid_argument'; }
check(rejected);
console.log('PASS large buffer library');
