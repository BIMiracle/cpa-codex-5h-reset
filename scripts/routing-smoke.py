"""Exercise the native scheduler ABI with fake credentials and no network."""
import base64
import ctypes as C
import json
import pathlib
import sys
import tempfile
import time


class Buffer(C.Structure):
    _fields_ = [("ptr", C.c_void_p), ("length", C.c_size_t)]


CALL = C.CFUNCTYPE(C.c_int, C.c_void_p, C.c_char_p, C.c_void_p, C.c_size_t, C.POINTER(Buffer))
FREE = C.CFUNCTYPE(None, C.c_void_p, C.c_size_t)
PCALL = C.CFUNCTYPE(C.c_int, C.c_char_p, C.c_void_p, C.c_size_t, C.POINTER(Buffer))
STOP = C.CFUNCTYPE(None)


class Host(C.Structure):
    _fields_ = [("version", C.c_uint32), ("ctx", C.c_void_p), ("call", CALL), ("free", FREE)]


class Plugin(C.Structure):
    _fields_ = [("version", C.c_uint32), ("call", PCALL), ("free", FREE), ("stop", STOP)]


held, errors = {}, []
used = {"a": 76, "b": 21}
queries = 0


@CALL
def host_call(ctx, method, data, length, output):
    global queries
    try:
        method, req = method.decode(), json.loads(C.string_at(data, length))
        if method == "host.auth.list":
            result = {"files": [{"id": k, "auth_index": k, "provider": "codex"} for k in used]}
        elif method == "host.auth.get":
            result = {"json": {"access_token": "fake-secret-" + req["auth_index"]}}
        elif method == "host.http.do":
            assert req["URL"] == "https://chatgpt.com/backend-api/wham/usage"
            key = req["Headers"]["Authorization"][0].rsplit("-", 1)[-1]
            body = {"rate_limit": {"primary_window": {"limit_window_seconds": 18000,
                    "used_percent": used[key], "reset_at": int(time.time()) + 18000}}}
            result = {"StatusCode": 200, "Body": base64.b64encode(json.dumps(body).encode()).decode()}
            queries += 1
        elif method == "host.log":
            result = {}
        else:
            raise AssertionError("unexpected callback: " + method)
        raw = json.dumps({"ok": True, "result": result}).encode()
    except Exception as exc:
        errors.append(str(exc))
        raw = b'{"ok":false,"error":{"code":"mock_failed","message":"mock failed"}}'
    memory = C.create_string_buffer(raw)
    held[C.addressof(memory)] = memory
    output[0].ptr, output[0].length = C.addressof(memory), len(raw)
    return 0


@FREE
def free_host(ptr, length):
    held.pop(ptr, None)


lib = C.CDLL(str(pathlib.Path(sys.argv[1]).resolve()))
lib.cliproxy_plugin_init.argtypes = [C.POINTER(Host), C.POINTER(Plugin)]
lib.cliproxy_plugin_init.restype = C.c_int
host, plugin = Host(1, None, host_call, free_host), Plugin()
assert lib.cliproxy_plugin_init(C.byref(host), C.byref(plugin)) == 0


def invoke(method, payload):
    raw = json.dumps(payload).encode()
    memory, output = C.create_string_buffer(raw), Buffer()
    code = plugin.call(method.encode(), memory, len(raw), C.byref(output))
    try:
        result = json.loads(C.string_at(output.ptr, output.length))
    finally:
        plugin.free(output.ptr, output.length)
    assert code == 0, result
    return result


def pick(session, key="general", candidates=("a", "b"), metadata=None):
    return invoke("scheduler.pick", {"Providers": ["codex"], "Model": "gpt-6.1-sol",
        "Options": {"Headers": {"Authorization": ["Bearer " + key], "Session_id": [session]},
                    "Metadata": metadata or {}},
        "Candidates": [{"ID": k, "Provider": "codex"} for k in candidates]})


with tempfile.TemporaryDirectory() as tmp:
    config = {"enabled": False, "state_file": str(pathlib.Path(tmp) / "wake.json"),
              "quota_routing": {"enabled": True, "quota_cache_ttl": "1s",
                                "bindings": [{"key": "dedicated", "allow": ["a"]}]}}
    payload = {"config_yaml": base64.b64encode(json.dumps(config).encode()).decode()}
    caps = invoke("plugin.register", payload)["result"]["capabilities"]
    assert caps["scheduler"] and caps["scheduler_across_priorities"]
    assert pick("first")["result"]["AuthID"] == "b"
    used.update(a=0, b=90)
    assert pick("first")["result"]["AuthID"] == "b"
    assert queries == 2, "sticky session refreshed quota"
    time.sleep(1.05)
    assert pick("second")["result"]["AuthID"] == "a"
    assert pick("first")["result"]["AuthID"] == "b"
    assert pick("fixed", key="dedicated")["result"]["AuthID"] == "a"
    denied = pick("fixed", key="dedicated", candidates=("b",))
    assert not denied["ok"] and denied["error"]["code"] == "bound_auth_unavailable", denied
    assert pick("wake", metadata={"pinned_auth_id": "b"})["result"]["AuthID"] == "b"
    state = pathlib.Path(config["state_file"] + ".routing.json").read_text()
    assert "fake-secret" not in state and "dedicated" not in state and "general" not in state
    invoke("plugin.reconfigure", payload)
    assert pick("first")["result"]["AuthID"] == "b"
    plugin.stop()

assert not errors, errors
assert not held, "host callback buffers leaked"
print("Routing ABI: headroom selection, sticky sessions, cache refresh, key isolation, pinning, persistence and reconfigure passed")
