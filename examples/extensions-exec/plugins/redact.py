#!/usr/bin/env python3
"""A reference AgentGo plugin: mask email addresses, and refuse an answer that
leaks one.

It is an ordinary program. It reads one JSON object per line on stdin, writes
one JSON object per line on stdout, and says anything else on stderr — the
framework forwards those lines to its own logger. Nothing here is Go, and
nothing here knows what a Go interface is.

Run it by hand to see the protocol:

    $ echo '{"id":1,"type":"hello","protocol":1,"name":"redact"}' | python3 redact.py
    {"id": 1, "type": "hello", "protocol": 1, "capabilities": ["after_tool", "lint"]}
"""

import json
import re
import sys

PROTOCOL = 1

# Only what is declared here is ever sent to this process.
CAPABILITIES = ["after_tool", "lint"]

EMAIL = re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}")
MASK = "[email redacted]"


def mask(value):
    """Walk any JSON value and mask every address in every string."""
    if isinstance(value, str):
        return EMAIL.sub(MASK, value)
    if isinstance(value, list):
        return [mask(item) for item in value]
    if isinstance(value, dict):
        return {key: mask(item) for key, item in value.items()}
    return value


def handle(req):
    kind = req.get("type")
    rep = {"id": req.get("id", 0)}

    if kind == "hello":
        rep["type"] = "hello"
        rep["protocol"] = PROTOCOL
        rep["capabilities"] = CAPABILITIES
        return rep

    if kind == "after_tool":
        payload = req.get("result") or {}
        original = payload.get("result")
        masked = mask(original)
        rep["result"] = masked
        rep["replaced"] = masked != original
        if rep["replaced"]:
            print("masked an address in the result of %s" % payload.get("name"),
                  file=sys.stderr)
        return rep

    if kind == "lint":
        text = (req.get("lint") or {}).get("text", "")
        if EMAIL.search(text):
            rep["ok"] = False
            # Written for the model: what is wrong, and what passes.
            rep["reason"] = ("the answer contains an email address; say what the "
                             "user needs without quoting any address")
        else:
            rep["ok"] = True
        return rep

    # A capability we never declared. Saying so is better than staying quiet:
    # an empty reply would look like a verdict.
    rep["error"] = "unsupported request type %r" % kind
    return rep


def main():
    while True:
        line = sys.stdin.readline()
        if not line:
            # stdin closed: the framework is gone, and so are we.
            return
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except json.JSONDecodeError as err:
            print("undecodable request: %s" % err, file=sys.stderr)
            continue
        if req.get("type") == "shutdown":
            return
        sys.stdout.write(json.dumps(handle(req)) + "\n")
        sys.stdout.flush()


if __name__ == "__main__":
    main()
