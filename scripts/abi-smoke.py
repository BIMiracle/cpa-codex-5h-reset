"""Load the release library against mock CPA callbacks; no network or real tokens."""
import base64
import ctypes as C
import json
import pathlib
import sys
import tempfile
import time

class Buffer(C.Structure):
    _fields_ = [('ptr', C.c_void_p), ('length', C.c_size_t)]
CALL = C.CFUNCTYPE(C.c_int, C.c_void_p, C.c_char_p, C.c_void_p, C.c_size_t, C.POINTER(Buffer))
FREE = C.CFUNCTYPE(None, C.c_void_p, C.c_size_t)
PCALL = C.CFUNCTYPE(C.c_int, C.c_char_p, C.c_void_p, C.c_size_t, C.POINTER(Buffer))
STOP = C.CFUNCTYPE(None)
class Host(C.Structure):
    _fields_ = [('version', C.c_uint32), ('ctx', C.c_void_p), ('call', CALL), ('free', FREE)]
class Plugin(C.Structure):
    _fields_ = [('version', C.c_uint32), ('call', PCALL), ('free', FREE), ('stop', STOP)]
held = {}
executions = 0
errors = []
@CALL
def host_call(ctx, method, data, length, output):
    global executions
    try:
        method = method.decode()
        request = json.loads(C.string_at(data, length))
        if method == 'host.auth.list':
            result = {'files': [{'id': 'mock-account', 'auth_index': 'mock-index', 'provider': 'codex'}]}
        elif method == 'host.model.execute':
            assert request['auth_id'] == 'mock-account'
            assert request['forced_provider'] == 'codex'
            executions += 1
            result = {'status_code': 500 if executions == 1 else 200}
        elif method == 'host.auth.get':
            result = {'json': {'access_token': 'mock-secret', 'account_id': 'mock-account'}}
        elif method == 'host.http.do':
            assert request['URL'] == 'https://chatgpt.com/backend-api/wham/usage'
            quota = {'rate_limit': {'primary_window': {'limit_window_seconds': 18000,
                'reset_at': int(time.time()) + (2 if executions == 1 else 18000), 'used_percent': 0}}}
            result = {'StatusCode': 200, 'Body': base64.b64encode(json.dumps(quota).encode()).decode()}
        elif method == 'host.log':
            result = {}
        else:
            raise AssertionError(method)
        body = json.dumps({'ok': True, 'result': result}).encode()
    except Exception as exc:
        errors.append(str(exc))
        body = b'{"ok":false,"error":{"code":"mock_error","message":"mock failure"}}'
    memory = C.create_string_buffer(body)
    address = C.addressof(memory)
    held[address] = memory
    output[0].ptr, output[0].length = address, len(body)
    return 0
@FREE
def free_host(ptr, length):
    held.pop(ptr, None)
lib = C.CDLL(str(pathlib.Path(sys.argv[1]).resolve()))
lib.cliproxy_plugin_init.argtypes = [C.POINTER(Host), C.POINTER(Plugin)]
lib.cliproxy_plugin_init.restype = C.c_int
host, plugin = Host(1, None, host_call, free_host), Plugin()
assert lib.cliproxy_plugin_init(C.byref(host), C.byref(plugin)) == 0
assert plugin.version == 1

def call(method, payload):
    data = json.dumps(payload).encode()
    memory = C.create_string_buffer(data)
    output = Buffer()
    code = plugin.call(method.encode(), memory, len(data), C.byref(output))
    try:
        result = json.loads(C.string_at(output.ptr, output.length))
    finally:
        plugin.free(output.ptr, output.length)
    assert code == 0 and result['ok'], result
    return result['result']

def management(method, path, body=None):
    req = {'Method': method, 'Path': path}
    if body is not None:
        req['Body'] = base64.b64encode(json.dumps(body).encode()).decode()
    result = call('management.handle', req)
    return result['StatusCode'], base64.b64decode(result['Body'])

with tempfile.TemporaryDirectory() as tmp:
    state = str(pathlib.Path(tmp) / 'state.json')
    config = {'enabled': True, 'state_file': state, 'schedules': [], 'max_retries': 3}
    encoded = base64.b64encode(json.dumps(config).encode()).decode()
    call('plugin.register', {'config_yaml': encoded})
    routes = call('management.register', {})
    assert any(r.get('Menu') == 'Codex 五小时重置唤醒' for r in routes['resources'])
    api = '/v0/management/plugins/cpa-codex-5h-reset'
    assert management('GET', '/v0/resource/plugins/cpa-codex-5h-reset/ui')[0] == 200
    assert management('POST', api + '/run', {})[0] == 202
    assert management('POST', api + '/run', {})[0] == 409
    deadline = time.time() + 12
    while time.time() < deadline:
        status, raw = management('GET', api + '/status')
        assert status == 200
        slots = json.loads(raw)['slots']
        if slots and slots[0]['status'] == 'fresh_window':
            break
        time.sleep(0.1)
    assert slots[0]['status'] == 'fresh_window', slots
    assert slots[0]['attempts'] == 2 and executions == 2, slots
    plugin.stop()
    assert 'mock-secret' not in pathlib.Path(state).read_text()
    call('plugin.register', {'config_yaml': encoded})
    _, raw = management('GET', api + '/status')
    assert json.loads(raw)['slots'][0]['attempts'] == 2
    plugin.stop()
assert not errors, errors
assert not held, 'host buffers leaked'
print('Native ABI: registration, resource, bound execution, reset wait, retry, conflict, persistence and shutdown passed')
