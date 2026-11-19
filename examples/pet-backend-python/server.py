"""A toy pet shop REST API that accepts the gateway's signed calls. Standard library only.

The same API as examples/pet-backend-go. It uses examples/signing/python/sigverify.py to check
the signature and to make a repeated idempotency key return the stored answer.
"""
import json
import os
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "signing", "python"))
sys.path.insert(0, os.path.dirname(__file__))
import sigverify  # noqa: E402

SECRET = os.environ.get("AGENTGW_SIGNING_SECRET", "")
KEY_ID = os.environ.get("KEY_ID", "dev-1")  # the gateway's --key-id default
if not SECRET:
    sys.exit("set AGENTGW_SIGNING_SECRET")

verifier = sigverify.Verifier({KEY_ID: SECRET.encode()})
lock = threading.Lock()
state = {"coins": 100, "pets": {"rex": {"name": "rex", "species": "dog", "hunger": 80}}}


class Refuse(Exception):
    """An expected 4xx. The gateway shows the text to the agent: a code and a hint."""

    def __init__(self, status, text):
        super().__init__(text)
        self.status, self.text = status, text


def shop(_body, _name):
    return 200, {"coins": state["coins"], "pets": list(state["pets"])}


def get_pet(_body, name):
    pet = state["pets"].get(name)
    if pet is None:
        raise Refuse(404, "no_such_pet: you do not own that pet")
    return 200, pet


def feed(body, _name):
    try:
        data = json.loads(body)
        pet_name, food, amount = data["pet"], data["food"], int(data["amount"])
    except (ValueError, KeyError, TypeError):
        raise Refuse(400, "bad_request: send pet, food and amount")
    price = {"kibble": 1, "fish": 2}.get(food)
    if price is None or amount < 1:
        raise Refuse(400, "bad_request: food must be kibble or fish")
    pet = state["pets"].get(pet_name)
    if pet is None:
        raise Refuse(404, "no_such_pet: you do not own that pet")
    cost = price * amount
    if state["coins"] < cost:
        raise Refuse(422, f"not_enough_coins: need {cost}, have {state['coins']}")
    state["coins"] -= cost
    pet["hunger"] = max(0, pet["hunger"] - 10 * amount)
    return 200, {"pet": pet["name"], "hunger": pet["hunger"], "coins": state["coins"]}


def adopt(body, _name):
    try:
        data = json.loads(body)
        name, species = data["name"], data["species"]
    except (ValueError, KeyError, TypeError):
        raise Refuse(400, "bad_request: send name and species")
    if not name or not species:
        raise Refuse(400, "bad_request: send name and species")
    if name in state["pets"]:
        raise Refuse(409, "name_taken: pick another name")
    if state["coins"] < 50:
        raise Refuse(422, f"not_enough_coins: need 50, have {state['coins']}")
    state["coins"] -= 50
    state["pets"][name] = {"name": name, "species": species, "hunger": 50}
    return 201, {"adopted": name, "coins": state["coins"]}


ROUTES = {
    ("GET", "/api/v1/shop"): shop,
    ("POST", "/api/v1/pets/feed"): feed,
    ("POST", "/api/v1/pets/adopt"): adopt,
}


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.serve()

    def do_POST(self):
        self.serve()

    def send(self, status, payload, replay=False):
        text = isinstance(payload, (bytes, str))  # refusals are short plain text, results are JSON
        data = payload.encode() if isinstance(payload, str) else payload if isinstance(payload, bytes) else json.dumps(payload, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "text/plain; charset=utf-8" if text else "application/json")
        if replay:
            self.send_header("X-Idempotent-Replay", "true")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def serve(self):
        length = min(int(self.headers.get("Content-Length") or 0), 1 << 20)
        body = self.rfile.read(length)
        try:
            # 1. signature and timestamp, over the path exactly as sent (query included)
            caller = verifier.check(self.command, self.path, dict(self.headers.items()), body)
        except sigverify.Rejected:
            return self.send(401, b'unauthorized')  # say nothing about why

        path = urlsplit(self.path).path
        name = None
        handler = ROUTES.get((self.command, path))
        if handler is None and self.command == "GET" and path.startswith("/api/v1/pets/"):
            handler, name = get_pet, path.rsplit("/", 1)[1]
        if handler is None:
            return self.send(404, b'not_found')

        # 2. run the action once per idempotency key; a repeat gets the stored answer
        def action():
            with lock:
                try:
                    return handler(body, name)
                except Refuse as r:
                    return r.status, r.text

        (status, payload), replayed = verifier.once(caller.key_id, caller.idempotency_key, action)
        self.send(status, payload, replayed)

    def log_message(self, fmt, *args):
        sys.stderr.write("pet shop: " + fmt % args + "\n")


if __name__ == "__main__":
    host, _, port = os.environ.get("BACKEND_ADDR", "127.0.0.1:9090").rpartition(":")
    print("pet shop listening on", host, port, flush=True)
    ThreadingHTTPServer((host, int(port)), Handler).serve_forever()
