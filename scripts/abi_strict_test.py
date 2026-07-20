#!/usr/bin/env python3

import argparse
import base64
import ctypes
import json
import resource
import statistics
import sys
import time


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
        raise AssertionError(
            f"{method} returned {rc}: {data.decode(errors='replace')}"
        )
    envelope = json.loads(data)
    if not envelope.get("ok"):
        raise AssertionError(f"{method} failed: {envelope}")
    return envelope["result"]


def load_plugin(path):
    lib = ctypes.CDLL(path)
    lib.cliproxy_plugin_init.argtypes = [
        ctypes.c_void_p,
        ctypes.POINTER(PluginAPI),
    ]
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
        raise AssertionError("cliproxy_plugin_init failed")
    if api.abi_version != 1 or not all(
        (api.call, api.free_buffer, api.shutdown)
    ):
        raise AssertionError(f"invalid plugin API: {api.abi_version}")

    config = (
        b"models:\n"
        b"  - gpt-5.6-sol\n"
        b"  - p/gpt-5.6-sol\n"
        b"source_formats:\n"
        b"  - openai\n"
        b"target_formats:\n"
        b"  - codex\n"
    )
    registration = invoke(
        lib,
        "plugin.register",
        {
            "config_yaml": base64.b64encode(config).decode(),
            "schema_version": 1,
        },
    )
    metadata = registration["metadata"]
    if metadata["Name"] != "image-compatible":
        raise AssertionError(f"unexpected metadata: {metadata}")
    if metadata["Version"] != "0.2.0":
        raise AssertionError(f"unexpected version: {metadata['Version']}")
    fields = [field["Name"] for field in metadata["ConfigFields"]]
    if fields != ["models", "source_formats", "target_formats"]:
        raise AssertionError(f"unexpected config fields: {fields}")
    if not registration["capabilities"]["request_normalizer"]:
        raise AssertionError("request_normalizer capability missing")
    return lib, registration


def normalize(
    lib,
    body,
    model="p/gpt-5.6-sol",
    source="openai",
    target="codex",
):
    result = invoke(
        lib,
        "request.normalize",
        {
            "FromFormat": source,
            "ToFormat": target,
            "Model": model,
            "Stream": False,
            "Body": base64.b64encode(
                json.dumps(body, separators=(",", ":")).encode()
            ).decode(),
        },
    )
    encoded = result.get("Body")
    if not encoded:
        return None
    return json.loads(base64.b64decode(encoded))


def stringified(parts):
    return json.dumps(parts, separators=(",", ":"))


def assert_no_data_image_in_text(value, path="$"):
    if isinstance(value, dict):
        for key, child in value.items():
            child_path = f"{path}.{key}"
            if (
                isinstance(child, str)
                and "data:image/" in child
                and key not in ("image_url", "file_data")
            ):
                raise AssertionError(
                    f"base64 image remained in text at {child_path}"
                )
            assert_no_data_image_in_text(child, child_path)
    elif isinstance(value, list):
        for index, child in enumerate(value):
            assert_no_data_image_in_text(child, f"{path}[{index}]")


def assert_image(part, expected_url, expected_detail=None):
    if part.get("type") != "input_image":
        raise AssertionError(f"expected input_image, got {part}")
    if part.get("image_url") != expected_url:
        raise AssertionError("image URL was not preserved exactly")
    if expected_detail is not None and part.get("detail") != expected_detail:
        raise AssertionError(f"image detail was not preserved: {part}")


def transformed_output(lib, parts, call_id="call-1"):
    body = {
        "model": "gpt-5.6-sol",
        "input": [
            {
                "type": "function_call_output",
                "call_id": call_id,
                "output": stringified(parts),
            }
        ],
    }
    repaired = normalize(lib, body)
    if repaired is None:
        raise AssertionError("expected stringified image output to be repaired")
    output = repaired["input"][0]["output"]
    if not isinstance(output, list):
        raise AssertionError(
            f"expected output array, got {type(output).__name__}"
        )
    assert_no_data_image_in_text(repaired)
    return repaired, output


def percentile(values, fraction):
    ordered = sorted(values)
    index = min(
        len(ordered) - 1,
        int(round((len(ordered) - 1) * fraction)),
    )
    return ordered[index]


def benchmark_case(lib, body, iterations):
    samples = []
    for _ in range(iterations):
        started = time.perf_counter()
        repaired = normalize(lib, body)
        elapsed = (time.perf_counter() - started) * 1000
        if repaired is None:
            raise AssertionError("benchmark request was not repaired")
        samples.append(elapsed)
    return {
        "iterations": iterations,
        "median_ms": round(statistics.median(samples), 3),
        "p95_ms": round(percentile(samples, 0.95), 3),
        "max_ms": round(max(samples), 3),
    }


def peak_rss_bytes():
    value = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
    if sys.platform == "darwin":
        return value
    return value * 1024


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("plugin")
    args = parser.parse_args()
    lib, registration = load_plugin(args.plugin)
    passed = []

    flat_url = "data:image/png;base64,QUJD"
    _, output = transformed_output(
        lib,
        [
            {
                "type": "input_image",
                "image_url": flat_url,
                "detail": "original",
            }
        ],
    )
    assert_image(output[0], flat_url, "original")
    passed.append("flat_input_image")

    nested_url = "data:image/jpeg;base64,REVG"
    _, output = transformed_output(
        lib,
        [
            {
                "type": "image_url",
                "image_url": {
                    "url": nested_url,
                    "detail": "high",
                },
            }
        ],
    )
    assert_image(output[0], nested_url, "high")
    passed.append("nested_chat_image_url")

    _, output = transformed_output(
        lib,
        [
            {"type": "text", "text": "before"},
            {"type": "input_image", "image_url": flat_url},
            {"type": "output_text", "text": "after"},
            {
                "type": "image_url",
                "image_url": nested_url,
                "detail": "low",
            },
        ],
    )
    expected_types = [
        "input_text",
        "input_image",
        "input_text",
        "input_image",
    ]
    if [part["type"] for part in output] != expected_types:
        raise AssertionError(f"mixed part order changed: {output}")
    if output[0]["text"] != "before" or output[2]["text"] != "after":
        raise AssertionError(f"mixed text changed: {output}")
    assert_image(output[3], nested_url, "low")
    passed.append("mixed_text_image_order")

    sequential_input = []
    sequential_urls = []
    for index in range(64):
        url = "data:image/png;base64," + base64.b64encode(
            f"image-{index:03d}".encode()
        ).decode()
        sequential_urls.append(url)
        sequential_input.append(
            {
                "type": "function_call_output",
                "call_id": f"call-{index:03d}",
                "output": stringified(
                    [{"type": "input_image", "image_url": url}]
                ),
            }
        )
    repaired = normalize(
        lib,
        {
            "model": "gpt-5.6-sol",
            "input": sequential_input,
        },
    )
    if repaired is None:
        raise AssertionError("sequential outputs were not repaired")
    actual_urls = []
    for item in repaired["input"]:
        output = item["output"]
        if not isinstance(output, list) or len(output) != 1:
            raise AssertionError(f"sequential output remained text: {output}")
        actual_urls.append(output[0]["image_url"])
    if actual_urls != sequential_urls:
        raise AssertionError("sequential images changed or were reordered")
    assert_no_data_image_in_text(repaired)
    passed.append("sixty_four_sequential_outputs")

    for ordinary in (
        '{"ok":true}',
        '[{"type":"text","text":"ordinary tool output"}]',
        '[{"type":"input_image","image_url":',
        '[{"type":"input_image"}]',
    ):
        body = {
            "model": "gpt-5.6-sol",
            "input": [
                {
                    "type": "function_call_output",
                    "call_id": "ordinary",
                    "output": ordinary,
                }
            ],
        }
        if normalize(lib, body) is not None:
            raise AssertionError(f"ordinary output was modified: {ordinary}")
    passed.append("ordinary_and_malformed_strings_unchanged")

    structured = normalize(
        lib,
        {
            "model": "gpt-5.6-sol",
            "input": [
                {
                    "type": "function_call_output",
                    "call_id": "structured",
                    "output": [
                        {
                            "type": "input_image",
                            "image_url": flat_url,
                        }
                    ],
                }
            ],
        },
    )
    if structured is not None:
        raise AssertionError("already structured image was rewritten")
    passed.append("already_structured_unchanged")

    scoped_body = {
        "model": "gpt-5.6-sol",
        "input": [
            {
                "type": "function_call_output",
                "call_id": "scope",
                "output": stringified(
                    [{"type": "input_image", "image_url": flat_url}]
                ),
            }
        ],
    }
    if normalize(lib, scoped_body, model="gpt-5.5") is not None:
        raise AssertionError("wrong model was not bypassed")
    if normalize(lib, scoped_body, source="openai-response") is not None:
        raise AssertionError("wrong source format was not bypassed")
    if normalize(lib, scoped_body, target="gemini") is not None:
        raise AssertionError("wrong target format was not bypassed")
    passed.append("scope_bypass")

    large_bytes = b"\x89PNG\r\n\x1a\n" + (
        b"A" * (16 * 1024 * 1024 - 8)
    )
    large_url = (
        "data:image/png;base64,"
        + base64.b64encode(large_bytes).decode()
    )
    large_body = {
        "model": "gpt-5.6-sol",
        "input": [
            {
                "type": "function_call_output",
                "call_id": "large",
                "output": stringified(
                    [
                        {
                            "type": "input_image",
                            "image_url": large_url,
                            "detail": "original",
                        }
                    ]
                ),
            }
        ],
    }
    large_repaired = normalize(lib, large_body)
    if large_repaired is None:
        raise AssertionError("16 MiB stringified image was not repaired")
    large_part = large_repaired["input"][0]["output"][0]
    assert_image(large_part, large_url, "original")
    assert_no_data_image_in_text(large_repaired)
    passed.append("sixteen_mib_image_lossless")

    structured_large_body = {
        "model": "gpt-5.6-sol",
        "input": [
            {
                "type": "function_call_output",
                "call_id": "structured-large",
                "output": [
                    {
                        "type": "input_image",
                        "image_url": large_url,
                        "detail": "original",
                    }
                ],
            }
        ],
    }
    if normalize(lib, structured_large_body) is not None:
        raise AssertionError("16 MiB structured image was rewritten")
    passed.append("sixteen_mib_structured_image_unchanged")

    small_body = {
        "model": "gpt-5.6-sol",
        "input": [
            {
                "type": "function_call_output",
                "call_id": "bench-small",
                "output": stringified(
                    [{"type": "input_image", "image_url": flat_url}]
                ),
            }
        ],
    }
    one_mib_bytes = b"\x89PNG\r\n\x1a\n" + (
        b"B" * (1024 * 1024 - 8)
    )
    one_mib_url = (
        "data:image/png;base64,"
        + base64.b64encode(one_mib_bytes).decode()
    )
    one_mib_body = {
        "model": "gpt-5.6-sol",
        "input": [
            {
                "type": "function_call_output",
                "call_id": "bench-one-mib",
                "output": stringified(
                    [
                        {
                            "type": "input_image",
                            "image_url": one_mib_url,
                        }
                    ]
                ),
            }
        ],
    }
    performance = {
        "small_image": benchmark_case(lib, small_body, 500),
        "one_mib_image": benchmark_case(lib, one_mib_body, 10),
        "sixteen_mib_image": benchmark_case(lib, large_body, 3),
    }

    peak_bytes = peak_rss_bytes()
    print(
        json.dumps(
            {
                "plugin": registration["metadata"]["Name"],
                "version": registration["metadata"]["Version"],
                "abi": 1,
                "passed": passed,
                "performance": performance,
                "process_peak_rss_bytes": peak_bytes,
                "process_peak_rss_mib": round(
                    peak_bytes / (1024 * 1024),
                    3,
                ),
            },
            ensure_ascii=False,
            sort_keys=True,
        )
    )


if __name__ == "__main__":
    main()
