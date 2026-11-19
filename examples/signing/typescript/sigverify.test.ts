import { readFileSync } from "node:fs";
import { test } from "node:test";
import assert from "node:assert/strict";
import { Rejected, Verifier, sign, signedString } from "./sigverify.ts";

const V = JSON.parse(readFileSync(new URL("../vector.json", import.meta.url), "utf8"));
const secret = Buffer.from(V.secret);
const body = Buffer.from(V.body);

const headers = (over: Record<string, string> = {}) => ({
  "X-AG-Key-Id": V.key_id, "X-AG-Timestamp": String(V.timestamp), "X-AG-Player": V.player,
  "X-AG-Idempotency-Key": V.idempotency_key, "X-AG-Signature": V.signature, ...over,
});
const verifier = (now: number = V.timestamp) => new Verifier({ [V.key_id]: secret }, () => now);

test("matches the published vector", () => {
  assert.equal(signedString(V.timestamp, V.key_id, V.method, V.path, V.player, V.idempotency_key, body), V.signed_string);
  assert.equal(sign(secret, V.timestamp, V.key_id, V.method, V.path, V.player, V.idempotency_key, body), V.signature);
});

test("accepts the vector", () => {
  assert.equal(verifier().check(V.method, V.path, headers(), body).player, "player-7");
});

const cases: [string, Record<string, string>, Buffer, string, number][] = [
  ["tampered signature", { "X-AG-Signature": "0".repeat(64) }, body, V.path, V.timestamp],
  ["short signature", { "X-AG-Signature": "abcd" }, body, V.path, V.timestamp],
  ["tampered player", { "X-AG-Player": "mallory" }, body, V.path, V.timestamp],
  ["tampered body", {}, Buffer.from(V.body.replace("2", "9")), V.path, V.timestamp],
  ["tampered path", {}, body, "/api/v1/pets/feed?src=other", V.timestamp],
  ["unknown key", { "X-AG-Key-Id": "nope" }, body, V.path, V.timestamp],
  ["missing header", { "X-AG-Idempotency-Key": "" }, body, V.path, V.timestamp],
  ["six minutes late", {}, body, V.path, V.timestamp + 360],
  ["six minutes early", {}, body, V.path, V.timestamp - 360],
];
for (const [name, over, b, path, now] of cases) {
  test(`rejects ${name}`, () => {
    assert.throws(() => verifier(now).check(V.method, path, headers(over), b), Rejected);
  });
}

test("a replayed idempotency key returns the stored result", async () => {
  const v = verifier();
  let calls = 0;
  const action = () => ({ run: ++calls });
  const first = await v.once(V.key_id, V.idempotency_key, action);
  const second = await v.once(V.key_id, V.idempotency_key, action);
  assert.deepEqual(first, [{ run: 1 }, false]);
  assert.deepEqual(second, [{ run: 1 }, true]);
  assert.equal(calls, 1);
});

test("a failed action can be retried", async () => {
  const v = verifier();
  await assert.rejects(v.once("k", "i1", () => { throw new Error("backend down"); }));
  assert.deepEqual(await v.once("k", "i1", () => "ok"), ["ok", false]);
});
