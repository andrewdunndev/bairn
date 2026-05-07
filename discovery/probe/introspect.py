#!/usr/bin/env python3
"""GraphQL introspection probe.

Single shot: POST a small introspection query at a GraphQL endpoint
and report whether introspection is enabled. If it is, dump the
schema to the output path. If it isn't, exit 0 with a report.

Usage:
    introspect.py --url https://example.com/graphql \\
                  --auth-header x-vendor-token \\
                  --auth-env VENDOR_TOKEN \\
                  --out path/to/baselines/__schema.json
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.request
import urllib.error
from pathlib import Path

# Minimal probe: types and root operation names. A full schema dump
# uses a much larger query; we start small to limit traffic if
# introspection is off.
MINIMAL_QUERY = (
    "{__schema{queryType{name} mutationType{name} subscriptionType{name} "
    "types{name kind}}}"
)

FULL_QUERY = """
query IntrospectionQuery {
  __schema {
    queryType { name }
    mutationType { name }
    subscriptionType { name }
    types {
      kind
      name
      description
      fields(includeDeprecated: true) {
        name
        description
        args { name type { ...TypeRef } defaultValue }
        type { ...TypeRef }
        isDeprecated
        deprecationReason
      }
      inputFields { name type { ...TypeRef } defaultValue }
      interfaces { ...TypeRef }
      enumValues(includeDeprecated: true) {
        name description isDeprecated deprecationReason
      }
      possibleTypes { ...TypeRef }
    }
    directives {
      name description
      locations
      args { name type { ...TypeRef } defaultValue }
    }
  }
}
fragment TypeRef on __Type {
  kind name
  ofType {
    kind name
    ofType {
      kind name
      ofType {
        kind name
        ofType { kind name }
      }
    }
  }
}
"""


def post_graphql(url: str, query: str, headers: dict) -> tuple[int, dict | str]:
    body = json.dumps({"query": query}).encode("utf-8")
    req = urllib.request.Request(
        url, data=body, method="POST",
        headers={**headers, "Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, json.loads(r.read())
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read())
        except Exception:
            return e.code, "<non-json error body>"


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--url", required=True)
    ap.add_argument("--auth-header", required=True)
    ap.add_argument("--auth-env", required=True)
    ap.add_argument("--user-agent", default="introspect-probe/0.0")
    ap.add_argument("--out", type=Path, required=True)
    args = ap.parse_args()

    token = os.environ.get(args.auth_env)
    if not token:
        print(f"env var {args.auth_env} not set", file=sys.stderr)
        return 2

    headers = {args.auth_header: token, "User-Agent": args.user_agent}

    print("probing minimal introspection query...")
    code, resp = post_graphql(args.url, MINIMAL_QUERY, headers)
    if code != 200 or not isinstance(resp, dict):
        print(f"  HTTP {code}, body: {resp!r}")
        print("  introspection appears disabled or auth rejected.")
        return 0

    if "errors" in resp and resp["errors"]:
        print(f"  HTTP 200 with errors: {resp['errors']}")
        print("  introspection appears disabled at the GraphQL layer.")
        return 0

    schema = resp.get("data", {}).get("__schema")
    if not schema:
        print("  HTTP 200 but no __schema in response. introspection unavailable.")
        return 0

    print(f"  introspection enabled. types reported: {len(schema.get('types', []))}")
    for op in ("queryType", "mutationType", "subscriptionType"):
        t = schema.get(op)
        if t:
            print(f"  {op}: {t.get('name')}")

    print("\nfetching full schema...")
    code, resp = post_graphql(args.url, FULL_QUERY, headers)
    if code != 200:
        print(f"  HTTP {code}, body: {resp!r}")
        return 1

    args.out.parent.mkdir(parents=True, exist_ok=True)
    with open(args.out, "w") as f:
        json.dump(resp, f, indent=2, sort_keys=True)
    print(f"  wrote {args.out} ({args.out.stat().st_size} bytes)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
