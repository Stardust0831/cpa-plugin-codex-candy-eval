#!/usr/bin/env python3
"""Load the Home store library through its real C ABI, with no CPA/Home server."""
import base64
import ctypes as C
import json
from pathlib import Path
import signal
import sys
import threading


class Buffer(C.Structure):
    _fields_ = [("ptr", C.c_void_p), ("len", C.c_size_t)]


HostCall = C.CFUNCTYPE(C.c_int, C.c_void_p, C.c_char_p, C.c_void_p, C.c_size_t, C.POINTER(Buffer))
Free = C.CFUNCTYPE(None, C.c_void_p, C.c_size_t)
Call = C.CFUNCTYPE(C.c_int, C.c_char_p, C.c_void_p, C.c_size_t, C.POINTER(Buffer))
Shutdown = C.CFUNCTYPE(None)


class HostAPI(C.Structure):
    _fields_ = [("abi_version", C.c_uint32), ("host_ctx", C.c_void_p), ("call", HostCall), ("free_buffer", Free)]


class PluginAPI(C.Structure):
    _fields_ = [("abi_version", C.c_uint32), ("call", Call), ("free_buffer", Free), ("shutdown", Shutdown)]


class Plugin:
    def __init__(self, library):
        self.host_calls = []

        def reject(_ctx, method, _body, _length, _response):
            self.host_calls.append(method.decode())
            return 1

        self.host = HostAPI(1, None, HostCall(reject), Free(lambda _ptr, _size: None))
        self.api = PluginAPI()
        self.library = C.CDLL(str(Path(library).resolve()))
        self.library.cliproxy_plugin_init.argtypes = [C.POINTER(HostAPI), C.POINTER(PluginAPI)]
        self.library.cliproxy_plugin_init.restype = C.c_int
        assert self.library.cliproxy_plugin_init(C.byref(self.host), C.byref(self.api)) == 0
        assert self.api.abi_version == 1

    def call(self, method, body=None):
        raw = json.dumps(body or {}).encode()
        response = Buffer()
        assert self.api.call(method.encode(), C.c_char_p(raw), len(raw), C.byref(response)) == 0
        try:
            return json.loads(C.string_at(response.ptr, response.len))
        finally:
            self.api.free_buffer(response.ptr, response.len)

    def register(self, method="plugin.register"):
        # Home passes its plugin config subtree as base64-encoded YAML.
        return self.call(method, {"schema_version": 6, "config_yaml": base64.b64encode(b"enabled: true\nload-in-home: true\n").decode()})

    def close(self):
        assert self.call("plugin.quiesce")["ok"]
        self.api.shutdown()
        assert not self.host_calls, self.host_calls


def main():
    stopped = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stopped.set())
    signal.signal(signal.SIGINT, lambda *_: stopped.set())
    plugin = Plugin(sys.argv[1])
    result = plugin.register()
    if not result.get("ok"):
        raise RuntimeError(result)
    try:
        stopped.wait()
    finally:
        plugin.close()


if __name__ == "__main__":
    main()
