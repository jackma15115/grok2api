import assert from "node:assert/strict";
import test from "node:test";

import { createServer } from "../src/app.mjs";

async function listen(server) {
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  return `http://127.0.0.1:${server.address().port}`;
}

test("serves material and protects configured deployments", async () => {
  const material = { seed: "seed", hex: "hex", refreshedAt: "now", expiresAt: "later", pathVersion: "v", pathCount: 4 };
  const collector = { status: () => ({ ready: true, refreshInFlight: false, lastError: null, material }), refresh: async () => material };
  const server = createServer({ collector, apiToken: "secret" });
  const baseURL = await listen(server);
  try {
    assert.equal((await fetch(`${baseURL}/healthz`)).status, 200);
    assert.equal((await fetch(`${baseURL}/material`)).status, 401);
    const response = await fetch(`${baseURL}/material`, { headers: { authorization: "Bearer secret" } });
    assert.deepEqual(await response.json(), material);
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});

test("returns 503 until a complete capture is ready", async () => {
  const collector = { status: () => ({ ready: false, refreshInFlight: true, lastError: null, material: null }), refresh: async () => null };
  const server = createServer({ collector });
  const baseURL = await listen(server);
  try {
    assert.equal((await fetch(`${baseURL}/healthz`)).status, 503);
    assert.equal((await fetch(`${baseURL}/material`)).status, 503);
    assert.equal((await fetch(`${baseURL}/refresh`, { method: "POST" })).status, 403);
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});
