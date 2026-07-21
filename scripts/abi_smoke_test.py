#!/usr/bin/env python3

import argparse
import base64
import ctypes
import json


class Buffer(ctypes.Structure):
    _fields_ = [("ptr", ctypes.c_void_p), ("len", ctypes.c_size_t)]


class PluginAPI(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("call", ctypes.c_void_p),
        ("free_buffer", ctypes.c_void_p),
        ("shutdown", ctypes.c_void_p),
    ]


def invoke(lib, method, payload):
    raw = json.dumps(payload, separators=(",", ":")).encode()
    request = (ctypes.c_uint8 * len(raw)).from_buffer_copy(raw)
    response = Buffer()
    rc = lib.cliproxyPluginCall(
        method.encode(),
        request,
        len(raw),
        ctypes.byref(response),
    )
    data = ctypes.string_at(response.ptr, response.len) if response.ptr else b""
    if response.ptr:
        lib.cliproxyPluginFree(response.ptr, response.len)
    if rc != 0:
        raise RuntimeError(f"{method} returned {rc}: {data.decode(errors='replace')}")
    envelope = json.loads(data)
    if not envelope.get("ok"):
        raise RuntimeError(f"{method} failed: {envelope}")
    return envelope["result"]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("plugin")
    args = parser.parse_args()

    lib = ctypes.CDLL(args.plugin)
    lib.cliproxy_plugin_init.argtypes = [ctypes.c_void_p, ctypes.POINTER(PluginAPI)]
    lib.cliproxy_plugin_init.restype = ctypes.c_int
    lib.cliproxyPluginCall.argtypes = [
        ctypes.c_char_p,
        ctypes.POINTER(ctypes.c_uint8),
        ctypes.c_size_t,
        ctypes.POINTER(Buffer),
    ]
    lib.cliproxyPluginCall.restype = ctypes.c_int
    lib.cliproxyPluginFree.argtypes = [ctypes.c_void_p, ctypes.c_size_t]

    api = PluginAPI()
    if lib.cliproxy_plugin_init(None, ctypes.byref(api)) != 0:
        raise RuntimeError("cliproxy_plugin_init failed")
    if api.abi_version != 1 or not all(
        (api.call, api.free_buffer, api.shutdown)
    ):
        raise RuntimeError(f"invalid plugin API: {api.abi_version}")

    config = b"models:\n  - gpt-5.6-sol\n"
    registration = invoke(
        lib,
        "plugin.register",
        {"config_yaml": base64.b64encode(config).decode(), "schema_version": 1},
    )
    metadata = registration["metadata"]
    required_metadata = ("Name", "Version", "Author", "GitHubRepository")
    if metadata["Name"] != "codex-tool-output-normalizer" or any(
        not str(metadata.get(field, "")).strip() for field in required_metadata
    ):
        raise RuntimeError(f"invalid host metadata: {metadata}")
    if not registration["capabilities"]["request_normalizer"]:
        raise RuntimeError("request_normalizer capability missing")
    field_names = [field["Name"] for field in metadata["ConfigFields"]]
    if field_names != ["models", "source_formats", "target_formats"]:
        raise RuntimeError(f"unexpected config fields: {field_names}")

    stringified = json.dumps(
        [
            {
                "detail": "original",
                "image_url": "data:image/png;base64,QUJD",
                "type": "input_image",
            }
        ],
        separators=(",", ":"),
    )
    body = {
        "model": "gpt-5.6-sol",
        "input": [
            {
                "type": "function_call_output",
                "call_id": "call-1",
                "output": stringified,
            }
        ],
    }
    normalized = invoke(
        lib,
        "request.normalize",
        {
            "FromFormat": "openai",
            "ToFormat": "codex",
            "Model": "gpt-5.6-sol",
            "Stream": True,
            "Body": base64.b64encode(
                json.dumps(body, separators=(",", ":")).encode()
            ).decode(),
        },
    )
    repaired = json.loads(base64.b64decode(normalized["Body"]))
    part = repaired["input"][0]["output"][0]
    expected = {
        "detail": "original",
        "image_url": "data:image/png;base64,QUJD",
        "type": "input_image",
    }
    if part != expected:
        raise RuntimeError(f"unexpected normalized image part: {part}")

    print(
        json.dumps(
            {
                "plugin": registration["metadata"]["Name"],
                "abi": api.abi_version,
                "normalized_part": part,
            },
            ensure_ascii=False,
        )
    )


if __name__ == "__main__":
    main()
