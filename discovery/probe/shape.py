#!/usr/bin/env python3
"""Generic JSON-shape probe.

Reads a TOML manifest describing endpoints to hit, sends each
request, then emits a deterministic shape signature per endpoint.
The signature contains JSON keys, types, and array-non-emptiness
only: it omits values entirely so it can be diffed across runs and
committed (or not) without leaking response content.

Usage:
    shape.py --manifest path/to/manifest.toml \\
             --out-dir path/to/baselines \\
             [--diff path/to/prior-baselines]

Manifest format (gitignored, lives in discovery/probe/):

    base_url     = "https://example.com"
    auth_header  = "x-vendor-token"
    auth_env     = "VENDOR_TOKEN"      # env var holding the token
    user_agent   = "your-tool/0.0"
    delay_sec    = 1.0                 # pacing between requests

    [[endpoint]]
    id     = "whoami"
    method = "GET"
    path   = "/api/me"

    [[endpoint]]
    id        = "feed-page1"
    method    = "GET"
    path      = "/api/feed?first=10"

    [[endpoint]]
    id        = "graphql-op"
    method    = "POST"
    path      = "/graphql?Foo"
    body_json = '{"operationName":"Foo","variables":{},"query":"..."}'

Path and body_json may reference env vars as ${VAR_NAME}; resolved
at runtime.

Outputs:
    <out-dir>/<endpoint-id>.shape    deterministic JSON-key signature
    <out-dir>/_summary.json          counts, status codes, sizes
"""
from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time
import tomllib
import urllib.request
import urllib.error
from pathlib import Path

ENV_REF = re.compile(r"\$\{([A-Z0-9_]+)\}")


def expand(s: str) -> str:
    def sub(m: re.Match) -> str:
        v = os.environ.get(m.group(1))
        if v is None:
            raise SystemExit(f"manifest references ${{{m.group(1)}}} but env var is not set")
        return v
    return ENV_REF.sub(sub, s)


def shape(v, depth: int = 0, max_depth: int = 6):
    """Recursive shape signature. No values, only types and structure."""
    if depth > max_depth:
        return "..."
    if isinstance(v, dict):
        return {k: shape(v[k], depth + 1, max_depth) for k in sorted(v.keys())}
    if isinstance(v, list):
        if not v:
            return ["<empty>"]
        # Union of shapes across all items, capped to first 5 to bound work
        sample = v[: min(5, len(v))]
        merged: dict | None = None
        for it in sample:
            sh = shape(it, depth + 1, max_depth)
            if isinstance(sh, dict):
                if merged is None:
                    merged = dict(sh)
                else:
                    for k, vv in sh.items():
                        if k not in merged:
                            merged[k] = vv
            else:
                merged = sh
                break
        return [merged, f"<n={len(v)}>"]
    if isinstance(v, bool):
        return "bool"
    if isinstance(v, int):
        return "int"
    if isinstance(v, float):
        return "float"
    if isinstance(v, str):
        return "str"
    if v is None:
        return "null"
    return type(v).__name__


def hit(base_url: str, ep: dict, token: str, auth_header: str, ua: str) -> tuple[int, bytes]:
    method = ep.get("method", "GET").upper()
    path = expand(ep["path"])
    url = base_url.rstrip("/") + path
    headers = {
        auth_header: token,
        "Content-Type": "application/json",
        "User-Agent": ua,
    }
    data = None
    body = ep.get("body_json")
    if body:
        data = expand(body).encode("utf-8")
    req = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def diff_shapes(a: object, b: object, path: str = "") -> list[str]:
    """Return human-readable list of differences between two shapes."""
    if type(a) is not type(b):
        return [f"{path or '.'}: type {type(a).__name__} -> {type(b).__name__}"]
    if isinstance(a, dict):
        out = []
        a_keys, b_keys = set(a.keys()), set(b.keys())
        for k in sorted(a_keys - b_keys):
            out.append(f"{path}/{k}: removed")
        for k in sorted(b_keys - a_keys):
            out.append(f"{path}/{k}: added")
        for k in sorted(a_keys & b_keys):
            out.extend(diff_shapes(a[k], b[k], f"{path}/{k}"))
        return out
    if isinstance(a, list):
        if not a or not b:
            return []
        return diff_shapes(a[0], b[0], f"{path}[]")
    if a != b:
        return [f"{path}: {a!r} -> {b!r}"]
    return []


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--manifest", required=True, type=Path)
    ap.add_argument("--out-dir", required=True, type=Path)
    ap.add_argument("--diff", type=Path, help="prior baselines dir to diff against")
    args = ap.parse_args()

    if not args.manifest.exists():
        print(f"manifest not found: {args.manifest}", file=sys.stderr)
        return 2

    with open(args.manifest, "rb") as f:
        m = tomllib.load(f)

    base_url = m["base_url"]
    auth_header = m["auth_header"]
    auth_env = m["auth_env"]
    ua = m.get("user_agent", "shape-probe/0.0")
    delay = float(m.get("delay_sec", 1.0))
    endpoints = m.get("endpoint", [])

    token = os.environ.get(auth_env)
    if not token:
        print(f"env var {auth_env} not set", file=sys.stderr)
        return 2

    args.out_dir.mkdir(parents=True, exist_ok=True)

    summary = {"endpoints": []}
    drift_count = 0
    for i, ep in enumerate(endpoints):
        if i > 0:
            time.sleep(delay)
        ep_id = ep["id"]
        try:
            code, body = hit(base_url, ep, token, auth_header, ua)
        except Exception as e:
            print(f"  {ep_id}: ERROR {type(e).__name__}: {e}")
            summary["endpoints"].append({"id": ep_id, "error": str(e)})
            continue
        try:
            parsed = json.loads(body)
        except json.JSONDecodeError:
            sig: object = "<not-json>"
            parsed = None
        else:
            sig = shape(parsed)

        sig_path = args.out_dir / f"{ep_id}.shape"
        with open(sig_path, "w") as f:
            json.dump(sig, f, indent=2, sort_keys=True)

        size = len(body)
        report = {"id": ep_id, "status": code, "size": size}

        if args.diff:
            prior = args.diff / f"{ep_id}.shape"
            if prior.exists():
                with open(prior) as f:
                    prior_sig = json.load(f)
                diffs = diff_shapes(prior_sig, sig)
                if diffs:
                    drift_count += 1
                    report["drift"] = diffs
                    print(f"  {ep_id}: HTTP {code}, {size}B, DRIFT ({len(diffs)} changes)")
                    for d in diffs:
                        print(f"    {d}")
                else:
                    print(f"  {ep_id}: HTTP {code}, {size}B, ok")
            else:
                print(f"  {ep_id}: HTTP {code}, {size}B, no prior baseline")
        else:
            print(f"  {ep_id}: HTTP {code}, {size}B")
        summary["endpoints"].append(report)

    summary_path = args.out_dir / "_summary.json"
    with open(summary_path, "w") as f:
        json.dump(summary, f, indent=2)

    return 1 if drift_count else 0


if __name__ == "__main__":
    sys.exit(main())
