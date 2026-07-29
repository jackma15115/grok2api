import assert from "node:assert/strict";
import test from "node:test";

import { currentMaterialStatus } from "../src/material.mjs";

test("only reports not ready before the first complete material exists", () => {
  assert.equal(currentMaterialStatus(null).ready, false);

  const material = Object.freeze({
    seed: Buffer.alloc(48).toString("base64"),
    hex: "deadbeef",
    refreshedAt: "2000-01-01T00:00:00.000Z",
    pathVersion: "v1",
    pathCount: 4,
  });
  const status = currentMaterialStatus(material, { lastError: "latest refresh failed" });

  assert.equal(status.ready, true);
  assert.equal(status.material, material);
  assert.equal(status.lastError, "latest refresh failed");
});
