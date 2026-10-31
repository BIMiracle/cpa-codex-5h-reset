"""Load the release library against mock CPA callbacks; no network or real tokens."""
import base64
import ctypes as C
import datetime
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
business_refreshed = False
credential_refreshes = 0
errors = []
@CALL
def host_call(ctx, method, data, length, output):
    global executions, business_refreshed, credential_refreshes
    try:
        method = method.decode()
        request = json.loads(C.string_at(data, length))
        if method == 'host.auth.list':
            result = {'files': [
                {'id': 'mock-account', 'auth_index': 'mock-index', 'provider': 'codex'},
                {'id': 'business-account', 'auth_index': 'business-index', 'provider': 'codex'}]}
        elif method == 'host.model.execute':
            assert request['auth_id'] == 'mock-account'
            assert request['forced_provider'] == 'codex'
            executions += 1
            result = {'status_code': 500 if executions == 1 else 200}
        elif method == 'host.auth.get':
            business = request['auth_index'] == 'business-index'
            plan = 'business_premium' if business else 'plus'
            claims = {'https://api.openai.com/auth': {'chatgpt_plan_type': plan}}
            token = 'header.' + base64.urlsafe_b64encode(json.dumps(claims).encode()).decode().rstrip('=') + '.signature'
            access_token = ('business-fresh-secret' if business_refreshed else 'business-expired-secret') if business else 'mock-secret'
            result = {'json': {'access_token': access_token, 'account_id': request['auth_index'], 'id_token': token}}
        elif method == 'host.auth.refresh':
            assert request['auth_index'] == 'business-index'
            assert request['access_token'] == 'business-expired-secret'
            credential_refreshes += 1
            business_refreshed = True
            result = {'json': {'access_token': 'business-fresh-secret', 'account_id': 'business-index'}}
        elif method == 'host.http.do':
            assert request['URL'] == 'https://chatgpt.com/backend-api/wham/usage'
            bearer = request['Headers']['Authorization'][0]
            business = bearer.startswith('Bearer business-')
            quota = {'rate_limit': {'primary_window': {'limit_window_seconds': 604800 if business else 18000,
                'reset_at': int(time.time()) + (2 if executions == 1 else 18000), 'used_percent': 0}}}
            result = {'StatusCode': 401 if bearer == 'Bearer business-expired-secret' else 200,
                      'Body': base64.b64encode(json.dumps(quota).encode()).decode()}
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
    assert any(r['Path'] == '/plugins/cpa-codex-5h-reset/holidays/import' for r in routes['routes'])
    assert management('GET', '/v0/resource/plugins/cpa-codex-5h-reset/ui')[0] == 200
    # Import all possible display years before querying the calendar: this smoke
    # test must never contact real holiday services, even across New Year.
    year = datetime.datetime.now(datetime.timezone.utc).year
    for annual_year in (year - 1, year, year + 1):
        holiday = {'year': annual_year, 'days': [{'date': f'{annual_year}-01-01', 'name': 'ABI test holiday', 'isOffDay': True}]}
        assert management('POST', api + '/holidays/import', {'data': holiday, 'preview': True})[0] == 200
        if annual_year == year - 1:
            assert not pathlib.Path(state + '.holidays.json').exists(), 'preview wrote holiday state'
        assert management('POST', api + '/holidays/import', {'data': holiday, 'preview': False})[0] == 200
    code, calendar_raw = management('GET', api + '/holidays')
    calendar = json.loads(calendar_raw)
    assert code == 200 and len(calendar['days']) == 30
    display = calendar['display_days']
    assert len(display) >= 84 and datetime.date.fromisoformat(display[0]['date']).weekday() == 0
    assert datetime.date.fromisoformat(display[-1]['date']).weekday() == 6
    assert all(day['data_known'] for day in display), 'month display lost imported annual data'
    assert management('POST', api + '/run', {'auth_id': 'business-account'})[0] == 400
    assert executions == 0, 'Business Premium must never execute a wake request'
    code, quota_raw = management('GET', api + '/account-quotas')
    quota_data = json.loads(quota_raw)
    assert code == 200 and quota_data['quotas']['business-account']['weekly']
    assert not quota_data['quotas']['business-account']['known']
    assert credential_refreshes == 1 and executions == 0, 'usage recovery must refresh once without waking'
    assert b'secret' not in quota_raw, 'quota response leaked a credential'
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
    assert len(slots) == 1 and slots[0]['auth_id'] == 'mock-account', slots
    assert slots[0]['attempts'] == 2 and executions == 2, slots
    plugin.stop()
    assert 'mock-secret' not in pathlib.Path(state).read_text()
    assert 'mock-secret' not in pathlib.Path(state + '.holidays.json').read_text()
    call('plugin.register', {'config_yaml': encoded})
    _, raw = management('GET', api + '/status')
    assert json.loads(raw)['slots'][0]['attempts'] == 2
    _, calendar_raw = management('GET', api + '/holidays')
    assert all(y['manual'] for y in json.loads(calendar_raw)['years']), 'holiday import lost after restart'
    plugin.stop()
assert not errors, errors
assert not held, 'host buffers leaked'
print('Native ABI: registration, resource, holiday persistence, OAuth 401 recovery, Plus-only wake, bound execution, reset wait, retry, conflict, persistence and shutdown passed')
