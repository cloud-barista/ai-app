"""Capture client-side outcomes for one AI-App vLLM observation window."""

import argparse
from concurrent.futures import ThreadPoolExecutor, wait
from datetime import datetime, timezone
from hashlib import sha256
import http.client
import json
import math
import os
from pathlib import Path
import re
from statistics import median_low
import time
from urllib.parse import urlsplit


CLIENT_LIMIT = 32
REQUEST_TIMEOUT = 15


def request_sample(endpoint, headers, payload, prompt_tokens):
    connect = http.client.HTTPSConnection if endpoint.scheme == "https" else http.client.HTTPConnection
    connection = connect(endpoint.hostname, endpoint.port, timeout=REQUEST_TIMEOUT)
    started = time.monotonic()
    try:
        connection.request("POST", endpoint.path, payload, headers)
        response = connection.getresponse()
        if response.status != 200:
            return None
        body = response.read((1 << 20) + 1)
        if len(body) > 1 << 20:
            return None
        result = json.loads(body)
        choice = result["choices"][0]
        observed_tokens = result["usage"]["prompt_tokens"]
        if (
            choice["finish_reason"] not in ("stop", "length")
            or type(observed_tokens) is not int
            or observed_tokens != prompt_tokens
        ):
            return None
        return time.monotonic() - started
    except (OSError, http.client.HTTPException, ValueError, KeyError, TypeError, IndexError):
        return None
    finally:
        connection.close()


def utc_second(epoch):
    return datetime.fromtimestamp(epoch, timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


def capture(profile, endpoint, rps, seconds, slo, token=""):
    prompts = profile["promptTokenIds"]
    max_output = profile["maxOutputTokens"]
    model = profile["model"]
    payloads = [
        json.dumps({
            "model": model, "prompt": ids, "max_tokens": max_output, "temperature": 0, "stream": False,
        }).encode()
        for ids in prompts
    ]
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token

    # shortcut: one endpoint and a fixed client limit, use an external runner for distributed tests.
    pending = set()
    outcomes = []
    with ThreadPoolExecutor(max_workers=CLIENT_LIMIT) as pool:
        wall_start = time.time()
        started = time.monotonic()
        for i in range(round(rps * seconds)):
            scheduled = started + i / rps
            time.sleep(max(0.0, scheduled - time.monotonic()))
            done, pending = wait(pending, timeout=0)
            outcomes.extend(future.result() for future in done)
            if len(pending) >= CLIENT_LIMIT or time.monotonic() - scheduled >= 1 / rps:
                raise ValueError("client missed the requested rate")
            index = i % len(payloads)
            pending.add(pool.submit(request_sample, endpoint, headers, payloads[index], len(prompts[index])))
        outcomes.extend(future.result() for future in pending)
        time.sleep(max(0.0, started + seconds - time.monotonic()))
        wall_end = time.time()
        elapsed = time.monotonic() - started

    start, end = math.floor(wall_start), math.ceil(wall_end)
    if abs((wall_end - wall_start) - elapsed) > 0.5 or end - start > 1800:
        raise ValueError("measurement window exceeded its limit or the clock changed")
    time.sleep(max(0.0, end - time.time()))
    if time.time() < end:
        raise ValueError("the clock changed before the measurement window closed")
    workload = json.dumps(["vllm-completions/v1", seconds, model, prompts, max_output], separators=(",", ":"))
    return {
        "workloadId": "vllm-" + sha256(workload.encode()).hexdigest(),
        "runtimeImage": profile["runtimeImage"],
        "window": {"start": utc_second(start), "end": utc_second(end)},
        "promptTokensP50": median_low(len(ids) for ids in prompts),
        "maxOutputTokens": max_output,
        "offeredRps": rps,
        "sloSeconds": slo,
        "attempted": len(outcomes),
        "failed": sum(latency is None for latency in outcomes),
        "withinSLO": sum(latency is not None and latency <= slo for latency in outcomes),
    }


def validate(profile, endpoint, rps, seconds, slo):
    if (
        endpoint.scheme not in ("http", "https") or not endpoint.hostname or endpoint.port == 0
        or endpoint.username is not None or endpoint.query or endpoint.fragment
        or not endpoint.path.endswith("/v1/completions")
    ):
        raise ValueError("supply an HTTP(S) vLLM completions URL")
    if not 0 < rps <= CLIENT_LIMIT or not 120 <= seconds <= 1800 - REQUEST_TIMEOUT - 2 or not 0 < slo <= REQUEST_TIMEOUT:
        raise ValueError("invalid request rate, duration or SLO")
    prompts = profile["promptTokenIds"]
    offered = rps * seconds
    attempts = round(offered)
    if (
        not isinstance(prompts, list) or not 1 <= len(prompts) <= 64 or attempts < 1
        or not math.isclose(offered, attempts, rel_tol=0, abs_tol=1e-9) or attempts % len(prompts)
    ):
        raise ValueError("the run must send a whole number of prompt set repetitions")
    for ids in prompts:
        if (
            not isinstance(ids, list) or not 1 <= len(ids) <= 8192
            or any(type(token) is not int or not 0 <= token <= 2**31 - 1 for token in ids)
        ):
            raise ValueError("supply bounded, pretokenized prompts")
    model, max_output = profile["model"], profile["maxOutputTokens"]
    if not isinstance(model, str) or not model.strip() or type(max_output) is not int or not 1 <= max_output <= 4096:
        raise ValueError("invalid model or output token limit")
    if not re.fullmatch(r"[^\s@]+@sha256:[0-9a-f]{64}", profile["runtimeImage"]):
        raise ValueError("supply the deployed runtime image digest")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("profile", type=Path)
    parser.add_argument("--endpoint", required=True)
    parser.add_argument("--flow", required=True, help="Correlation ID of the active AI-App deployment")
    parser.add_argument("--revision", type=int, required=True)
    parser.add_argument("--rps", type=float, required=True)
    parser.add_argument("--seconds", type=int, default=180)
    parser.add_argument("--slo-seconds", type=float, required=True)
    args = parser.parse_args()
    try:
        endpoint = urlsplit(args.endpoint)
        profile = json.loads(args.profile.read_text(encoding="utf-8"))
        if not args.flow.strip() or args.revision < 1:
            raise ValueError("supply an active AI-App flow and revision")
        validate(profile, endpoint, args.rps, args.seconds, args.slo_seconds)
        result = capture(profile, endpoint, args.rps, args.seconds, args.slo_seconds, os.getenv("AIAPP_BENCHMARK_TOKEN", ""))
    except (OSError, ValueError, TypeError, KeyError) as error:
        parser.error(str(error))
    print(json.dumps({"correlationId": args.flow, "revision": args.revision, "load": result}, allow_nan=False))


if __name__ == "__main__":
    main()
