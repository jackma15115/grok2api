import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import test from "node:test";

import { buildStatsig, extractMaterialFromCapture, STATSIG_EPOCH, STATSIG_MARK, STATSIG_SALT } from "../src/statsig.mjs";

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

test("extracts and verifies seed and HEX from a browser capture", () => {
  const seed = Buffer.from(Array.from({ length: 48 }, (_, index) => 255 - index));
  const source = { seed: seed.toString("base64"), hex: "349e9cdeadbeef" };
  const number = 0x10203040;
  const statsigID = buildStatsig(source, "POST", "/rest/rate-limits", STATSIG_EPOCH + number, 0xa5);
  const digestInput = `POST!/rest/rate-limits!${number}${STATSIG_SALT}${source.hex}`;

  assert.deepEqual(extractMaterialFromCapture({
    statsigID,
    method: "POST",
    path: "/rest/rate-limits",
    digestInputs: ["unrelated", digestInput],
  }), {
    seed: seed.toString("base64").replace(/=+$/g, ""),
    hex: source.hex,
    digestLength: 16,
    hasMarker: true,
    prefix: "",
    capturedMethod: "POST",
    capturedPath: "/rest/rate-limits",
  });
  assert.throws(() => extractMaterialFromCapture({
    statsigID,
    method: "POST",
    path: "/rest/rate-limits",
    digestInputs: [`${digestInput}00`],
  }), /does not match/);
});

test("preserves a version-prefixed browser payload", () => {
  const material = {
    seed: Buffer.alloc(48, 0x42).toString("base64"),
    hex: "deadbeef",
    digestLength: 16,
    hasMarker: true,
    prefix: Buffer.from([0x02, 0x01]).toString("base64").replace(/=+$/g, ""),
  };
  const statsigID = buildStatsig(material, "POST", "/rest/chat", STATSIG_EPOCH + 1234, 0x33);
  assert.equal(Buffer.from(statsigID, "base64").length, 72);
  assert.deepEqual(extractMaterialFromCapture({
    statsigID,
    method: "POST",
    path: "/rest/chat",
    digestInputs: [`POST!/rest/chat!1234${STATSIG_SALT}${material.hex}`],
  }), {
    seed: material.seed.replace(/=+$/g, ""),
    hex: material.hex,
    digestLength: 16,
    hasMarker: true,
    prefix: material.prefix,
    capturedMethod: "POST",
    capturedPath: "/rest/chat",
  });
});

test("verifies a computed-style HEX candidate when Web Crypto is not observable", () => {
  const material = {
    seed: Buffer.alloc(48, 0x24).toString("base64"),
    hex: "349e9cdeadbeef",
    digestLength: 19,
    hasMarker: false,
    prefix: "",
  };
  const statsigID = buildStatsig(material, "GET", "/rest/modes", STATSIG_EPOCH + 9876, 0x12);
  const extracted = extractMaterialFromCapture({
    statsigID,
    method: "GET",
    path: "/rest/modes",
    digestInputs: [],
    hexCandidates: ["bad", material.hex],
  });
  assert.equal(extracted.hex, material.hex);
  assert.equal(extracted.digestLength, 19);
  assert.equal(extracted.hasMarker, false);
});
