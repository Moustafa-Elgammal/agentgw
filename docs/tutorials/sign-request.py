"""Helper for 03-accept-signed-calls.sh: prints the five signed headers for one request, one
per line as `Name: value`, using the Python verifier example. Docker-only, no host Python.

    python sign-request.py METHOD PATH_AND_QUERY PLAYER IDEMPOTENCY_KEY BODY
"""
import sys
import time

sys.path.insert(0, "/sig")
import sigverify  # noqa: E402

method, path, player, idem, body = sys.argv[1:6]
ts = int(time.time())
sig = sigverify.sign(b"dev-secret", ts, "dev-1", method, path, player, idem, body.encode())
for k, v in [("X-AG-Key-Id", "dev-1"), ("X-AG-Timestamp", str(ts)), ("X-AG-Player", player),
             ("X-AG-Idempotency-Key", idem), ("X-AG-Signature", sig)]:
    print(f"{k}: {v}")
