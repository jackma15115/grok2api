import assert from "node:assert/strict";
import test from "node:test";

import {
  buildStatsig,
  decodeStatsigID,
  extractMaterialFromCapture,
  materialFromConfig,
} from "../src/statsig.mjs";

const liveBytes = [
  143, 88, 179, 38, 57, 241, 161, 251, 76, 31, 220, 20, 210, 15, 31, 211,
  40, 89, 225, 96, 223, 196, 229, 180, 219, 104, 246, 71, 119, 71, 49, 110,
  220, 66, 118, 80, 206, 209, 201, 160, 148, 152, 11, 19, 155, 213, 81, 3,
  187, 243, 38, 125, 138, 27, 62, 96, 63, 212, 65, 52, 228, 53, 177, 114,
  125, 99, 165, 182, 110, 140,
];
const capturedID = Buffer.from(liveBytes).toString("base64").replace(/=+$/, "");
const capturedInput = "POST!/rest/app-chat/conversations/new!99789180obfiowerehiringad36d100100";
const capturedSeed = "1zyptn4udMOQU5tdgJBcp9Zu71BLajtU53nI+Mi+4VPN+d9BXkYvGxeEnBRa3ow0";

test("extracts and validates a browser-captured material pair", () => {
  const material = extractMaterialFromCapture({
    statsigID: capturedID,
    method: "post",
    path: "/rest/app-chat/conversations/new",
    digestInputs: [capturedInput],
  });
  assert.equal(material.seed, capturedSeed);
  assert.equal(material.hex, "ad36d100100");
  assert.equal(material.number, 99789180);
  assert.equal(buildStatsig(material, "POST", "/rest/app-chat/conversations/new", 1682924400 + 99789180, 143), capturedID);
});

test("rejects malformed or non-70-byte values", () => {
  assert.throws(() => decodeStatsigID("not-a-statsig"), /70 bytes|base64/);
  assert.throws(() => extractMaterialFromCapture({ statsigID: capturedID, method: "POST", path: "/rest/test", digestInputs: [capturedInput] }), /matching/);
});

test("accepts padded config seed and emits a raw base64 value", () => {
  const seed = Buffer.alloc(48, 7).toString("base64");
  const material = materialFromConfig(seed, "deadbeef");
  const value = buildStatsig(material, "post", "/rest/test", 1682924401, 1);
  assert.equal(value.includes("="), false);
  assert.equal(decodeStatsigID(value).seed.toString("base64"), seed);
});
