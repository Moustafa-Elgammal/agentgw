import json
import pathlib
import unittest

import sigverify

V = json.loads((pathlib.Path(__file__).resolve().parent.parent / "vector.json").read_text())


def headers(**over):
    h = {
        "X-AG-Key-Id": V["key_id"], "X-AG-Timestamp": str(V["timestamp"]), "X-AG-Player": V["player"],
        "X-AG-Idempotency-Key": V["idempotency_key"], "X-AG-Signature": V["signature"],
    }
    h.update(over)
    return h


def verifier(now=None):
    return sigverify.Verifier({V["key_id"]: V["secret"].encode()}, now=lambda: now if now is not None else V["timestamp"])


class VectorTest(unittest.TestCase):
    def test_signature_matches_the_published_vector(self):
        body = V["body"].encode()
        s = sigverify.signed_string(V["timestamp"], V["key_id"], V["method"], V["path"], V["player"], V["idempotency_key"], body)
        self.assertEqual(s, V["signed_string"])
        sig = sigverify.sign(V["secret"].encode(), V["timestamp"], V["key_id"], V["method"], V["path"], V["player"], V["idempotency_key"], body)
        self.assertEqual(sig, V["signature"])

    def test_accepts_the_vector(self):
        r = verifier().check(V["method"], V["path"], headers(), V["body"].encode())
        self.assertEqual(r.player, "player-7")

    def test_rejections(self):
        body = V["body"].encode()
        cases = {
            "tampered signature": (headers(**{"X-AG-Signature": "0" * 64}), body, V["path"], None),
            "tampered player": (headers(**{"X-AG-Player": "mallory"}), body, V["path"], None),
            "tampered body": (headers(), body.replace(b"2", b"9"), V["path"], None),
            "tampered path": (headers(), body, "/api/v1/pets/feed?src=other", None),
            "unknown key": (headers(**{"X-AG-Key-Id": "nope"}), body, V["path"], None),
            "missing header": (headers(**{"X-AG-Idempotency-Key": ""}), body, V["path"], None),
            "six minutes late": (headers(), body, V["path"], V["timestamp"] + 360),
            "six minutes early": (headers(), body, V["path"], V["timestamp"] - 360),
        }
        for name, (h, b, path, now) in cases.items():
            with self.subTest(name):
                with self.assertRaises(sigverify.Rejected):
                    verifier(now).check(V["method"], path, h, b)

    def test_replayed_idempotency_key_returns_the_stored_result(self):
        v = verifier()
        calls = []

        def action():
            calls.append(1)
            return {"run": len(calls)}

        first, replay1 = v.once(V["key_id"], V["idempotency_key"], action)
        second, replay2 = v.once(V["key_id"], V["idempotency_key"], action)
        self.assertEqual((first, replay1), ({"run": 1}, False))
        self.assertEqual((second, replay2), ({"run": 1}, True))
        self.assertEqual(len(calls), 1)

    def test_failed_action_can_be_retried(self):
        v = verifier()

        def boom():
            raise RuntimeError("backend down")

        with self.assertRaises(RuntimeError):
            v.once("k", "i1", boom)
        value, replay = v.once("k", "i1", lambda: "ok")
        self.assertEqual((value, replay), ("ok", False))


if __name__ == "__main__":
    unittest.main()
