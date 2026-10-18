import importlib
import sys

sys.path.insert(0, sys.argv[1])
g = importlib.import_module('probe')
value = bytearray(100 << 20)
value[0], value[-1] = 7, 19
result = g.Echo(value).result(30)
assert len(result) == len(value) and result[0] == 8 and result[-1] == 20
assert value[0] == 7 and value[-1] == 19
assert g.StringSize(b'x' * (65 << 20)).result(30) == 65 << 20
try:
    g.Echo([]).result(30)
    raise AssertionError('invalid bytes accepted')
except g.main.rt.LibraryFailure as error:
    assert error.kind == 'invalid_argument'
print('PASS large buffer library')
