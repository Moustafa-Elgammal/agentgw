"""Verify the gateway's signed calls. Standard library only. Copy this into your backend.

    verifier = Verifier({"dev-1": b"dev-secret"})
    result = verifier.check(method, path_and_query, headers, body)   # raises Rejected
    player = result.player                                          # authenticated

Idempotency: wrap your action with `verifier.once(key_id, idem_key, action)`.
"""
import hashlib
import hmac
import threading
import time

MAX_SKEW = 300  # seconds, both directions
HEADERS = ("x-ag-key-id", "x-ag-timestamp", "x-ag-player", "x-ag-idempotency-key", "x-ag-signature")


class Rejected(Exception):
    """The request is not from the gateway. Answer 401 and say nothing more."""


def signed_string(ts, key_id, method, path, player, idem, body):
    digest = hashlib.sha256(body).hexdigest()
    return "\n".join(["v1", str(ts), key_id, method.upper(), path, player, idem, digest])


def sign(secret, ts, key_id, method, path, player, idem, body):
    s = signed_string(ts, key_id, method, path, player, idem, body)
    return hmac.new(secret, s.encode(), hashlib.sha256).hexdigest()


class Verifier:
    def __init__(self, secrets, now=time.time):
        self.secrets = secrets  # key id -> secret bytes. Keep two during a rotation.
        self.now = now
        self._lock = threading.Lock()
        self._results = {}  # (key id, idempotency key) -> (expires, entry)

    def check(self, method, path, headers, body):
        h = {k.lower(): v for k, v in headers.items()}
        if any(not h.get(name) for name in HEADERS):
            raise Rejected("missing header")
        try:
            ts = int(h["x-ag-timestamp"])
        except ValueError:
            raise Rejected("bad timestamp")
        if abs(self.now() - ts) > MAX_SKEW:
            raise Rejected("timestamp outside the allowed skew")
        secret = self.secrets.get(h["x-ag-key-id"])
        if secret is None:
            raise Rejected("unknown key")
        want = sign(secret, ts, h["x-ag-key-id"], method, path, h["x-ag-player"], h["x-ag-idempotency-key"], body)
        # Constant time: a plain == would leak how many leading characters matched.
        if not hmac.compare_digest(want, h["x-ag-signature"]):
            raise Rejected("signature mismatch")
        return Result(h["x-ag-player"], h["x-ag-key-id"], h["x-ag-idempotency-key"])

    def once(self, key_id, idem, action):
        """Run action() once per idempotency key. A repeat gets the stored answer back.

        Call this only after check() succeeded: the key is trusted only once it is signed.
        """
        k = (key_id, idem)
        with self._lock:
            now = self.now()
            for old in [x for x, (exp, e) in self._results.items() if now > exp and e["done"].is_set()]:
                del self._results[old]
            hit = self._results.get(k)
            if hit is None:
                entry = {"done": threading.Event(), "value": None}
                self._results[k] = (now + 2 * MAX_SKEW, entry)
        if hit is not None:
            entry = hit[1]
            entry["done"].wait()
            return entry["value"], True
        try:
            entry["value"] = action()
        except BaseException:
            with self._lock:
                self._results.pop(k, None)  # it did not finish: let a retry run
            entry["done"].set()
            raise
        entry["done"].set()
        return entry["value"], False


class Result:
    def __init__(self, player, key_id, idem):
        self.player, self.key_id, self.idempotency_key = player, key_id, idem
