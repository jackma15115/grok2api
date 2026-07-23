import assert from "node:assert/strict";
import test from "node:test";

import { computeStyleHEX, validateMaterial } from "../src/hex.mjs";

test("encodes browser computed style without a digest or signed request", () => {
  const seed = Buffer.alloc(48).toString("base64");
  const hex = computeStyleHEX("rgb(52, 158, 156)", "matrix(0.991005, -0.133826, 0.133826, 0.991005, 0, 0)");
  assert.equal(hex, "349e9c0fd70a3d70a3d702147ae147ae14802147ae147ae1480fd70a3d70a3d700");
  assert.doesNotThrow(() => validateMaterial(seed, hex));
});
