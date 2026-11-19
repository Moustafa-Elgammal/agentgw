// Verify the gateway's signed calls. Node's standard library only. Copy this into your backend.
//
//   const v = new Verifier({ "dev-1": Buffer.from("dev-secret") });
//   const { player } = v.check(method, pathAndQuery, req.headers, bodyBytes); // throws Rejected
//   const [result, replayed] = await v.once(keyId, idemKey, () => doTheAction());
import { createHash, createHmac, timingSafeEqual } from "node:crypto";

export const MAX_SKEW = 300; // seconds, both directions
const REQUIRED = ["x-ag-key-id", "x-ag-timestamp", "x-ag-player", "x-ag-idempotency-key", "x-ag-signature"];

/** The request is not from the gateway. Answer 401 and say nothing more. */
export class Rejected extends Error {}

export function signedString(ts: number, keyId: string, method: string, path: string, player: string, idem: string, body: Uint8Array): string {
  const digest = createHash("sha256").update(body).digest("hex");
  return ["v1", String(ts), keyId, method.toUpperCase(), path, player, idem, digest].join("\n");
}

export function sign(secret: Uint8Array, ts: number, keyId: string, method: string, path: string, player: string, idem: string, body: Uint8Array): string {
  return createHmac("sha256", secret).update(signedString(ts, keyId, method, path, player, idem, body)).digest("hex");
}

type Headers = Record<string, string | string[] | undefined>;
type Entry = { expires: number; promise: Promise<unknown> };

export class Verifier {
  private results = new Map<string, Entry>();
  private secrets: Record<string, Uint8Array>;
  private now: () => number;

  constructor(secrets: Record<string, Uint8Array>, now: () => number = () => Date.now() / 1000) {
    this.secrets = secrets; // key id -> secret. Keep two during a rotation.
    this.now = now;
  }

  check(method: string, path: string, headers: Headers, body: Uint8Array): { player: string; keyId: string; idempotencyKey: string } {
    const h: Record<string, string> = {};
    for (const [k, v] of Object.entries(headers)) h[k.toLowerCase()] = Array.isArray(v) ? (v[0] ?? "") : (v ?? "");
    if (REQUIRED.some((name) => !h[name])) throw new Rejected("missing header");
    if (!/^\d+$/.test(h["x-ag-timestamp"])) throw new Rejected("bad timestamp");
    const ts = Number(h["x-ag-timestamp"]);
    if (Math.abs(this.now() - ts) > MAX_SKEW) throw new Rejected("timestamp outside the allowed skew");
    const secret = this.secrets[h["x-ag-key-id"]];
    if (!secret) throw new Rejected("unknown key");
    const want = Buffer.from(sign(secret, ts, h["x-ag-key-id"], method, path, h["x-ag-player"], h["x-ag-idempotency-key"], body));
    const got = Buffer.from(h["x-ag-signature"]);
    // Constant time, and only on equal lengths (timingSafeEqual throws otherwise).
    if (want.length !== got.length || !timingSafeEqual(want, got)) throw new Rejected("signature mismatch");
    return { player: h["x-ag-player"], keyId: h["x-ag-key-id"], idempotencyKey: h["x-ag-idempotency-key"] };
  }

  /**
   * Run action once per idempotency key. A repeat gets the stored answer. Call it only after
   * check() succeeded: the key is trusted only once it is signed.
   */
  async once<T>(keyId: string, idem: string, action: () => Promise<T> | T): Promise<[T, boolean]> {
    const k = `${keyId}/${idem}`;
    const now = this.now();
    for (const [key, e] of this.results) if (now > e.expires) this.results.delete(key);
    const hit = this.results.get(k);
    if (hit) return [(await hit.promise) as T, true];
    const promise = Promise.resolve().then(action);
    this.results.set(k, { expires: now + 2 * MAX_SKEW, promise });
    try {
      return [await promise, false];
    } catch (err) {
      this.results.delete(k); // it did not finish: let a retry run
      throw err;
    }
  }
}
