import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import test from "node:test";

import { buildStatsig, STATSIG_EPOCH, STATSIG_MARK, STATSIG_SALT } from "../src/statsig.mjs";

test("builds the grok.wodf.de-compatible 70-byte Statsig value", () => {
  const seed = Buffer.from(Array.from({ length: 48 }, (_, index) => index));
  const material = { seed: seed.toString("base64"), hex: "deadbeef" };
  const number = 0x12345678;
  const key = 0x5a;
  const value = buildStatsig(material, "post", "/rest/rate-limits", STATSIG_EPOCH + number, key);
  const decoded = Buffer.from(value, "base64");

  assert.equal(decoded.length, 70);
  assert.equal(decoded[0], key);
  assert.deepEqual(Buffer.from(decoded.subarray(1, 49).map((item) => item ^ key)), seed);
  assert.equal((decoded[49] ^ key) | ((decoded[50] ^ key) << 8) | ((decoded[51] ^ key) << 16) | ((decoded[52] ^ key) << 24), number);

  const input = `POST!/rest/rate-limits!${number}${STATSIG_SALT}${material.hex}`;
  const digest = createHash("sha256").update(input).digest().subarray(0, 16);
  assert.deepEqual(Buffer.from(decoded.subarray(53, 69).map((item) => item ^ key)), digest);
  assert.equal(decoded[69] ^ key, STATSIG_MARK);
});

test("validates method, path, and captured material", () => {
  const material = { seed: Buffer.alloc(48).toString("base64"), hex: "deadbeef" };
  assert.throws(() => buildStatsig(material, "POST", "https://grok.com/rest/test"), /absolute request pathname/);
  assert.throws(() => buildStatsig(material, "POST /", "/rest/test"), /method is invalid/);
  assert.throws(() => buildStatsig({ ...material, seed: "invalid" }, "POST", "/rest/test"), /seed/);
});
