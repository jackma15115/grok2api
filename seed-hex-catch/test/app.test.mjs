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
  const seed = Buffer.alloc(48, 9);
  const material = { seed: seed.toString("base64"), hex: "deadbeef", refreshedAt: "now", pathVersion: "v", pathCount: 4 };
  const collector = { status: () => ({ ready: true, refreshInFlight: false, lastError: null, material }), refresh: async () => material };
  const server = createServer({ collector, apiToken: "secret" });
  const baseURL = await listen(server);
  try {
    assert.equal((await fetch(`${baseURL}/healthz`)).status, 200);
    assert.equal((await fetch(`${baseURL}/material`)).status, 401);
    assert.equal((await fetch(`${baseURL}/sign`, { method: "POST", body: "{}" })).status, 401);
    const response = await fetch(`${baseURL}/material`, { headers: { authorization: "Bearer secret" } });
    assert.deepEqual(await response.json(), material);

    const signed = await fetch(`${baseURL}/sign`, {
      method: "POST",
      headers: { authorization: "Bearer secret", "content-type": "application/json" },
      body: JSON.stringify({ method: "POST", path: "/rest/rate-limits", environment: { metaContent: "compatible" } }),
    });
    assert.equal(signed.status, 200);
    const decoded = Buffer.from((await signed.json())["x-statsig-id"], "base64");
    assert.equal(decoded.length, 70);
    assert.deepEqual(Buffer.from(decoded.subarray(1, 49).map((value) => value ^ decoded[0])), seed);
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
    const signed = await fetch(`${baseURL}/sign`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ method: "POST", path: "/rest/test" }),
    });
    assert.equal(signed.status, 503);
    assert.equal((await fetch(`${baseURL}/refresh`, { method: "POST" })).status, 403);
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});

test("continues signing with the current material after a refresh error", async () => {
  const material = {
    seed: Buffer.alloc(48, 7).toString("base64"),
    hex: "deadbeef",
    refreshedAt: "2000-01-01T00:00:00.000Z",
    pathVersion: "v1",
    pathCount: 4,
  };
  const collector = {
    status: () => ({ ready: true, refreshInFlight: false, lastError: "latest refresh failed", material }),
    refresh: async () => null,
  };
  const server = createServer({ collector });
  const baseURL = await listen(server);
  try {
    assert.equal((await fetch(`${baseURL}/healthz`)).status, 200);
    const signed = await fetch(`${baseURL}/sign`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ method: "POST", path: "/rest/test" }),
    });
    assert.equal(signed.status, 200);
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});

test("rejects malformed sign requests before reading material", async () => {
  const collector = { status: () => { throw new Error("status should not be read"); }, refresh: async () => null };
  const server = createServer({ collector, maxBodyBytes: 64 });
  const baseURL = await listen(server);
  try {
    const invalidJSON = await fetch(`${baseURL}/sign`, { method: "POST", body: "{" });
    assert.equal(invalidJSON.status, 400);

    const invalidPath = await fetch(`${baseURL}/sign`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ method: "POST", path: "https://grok.com/rest/test" }),
    });
    assert.equal(invalidPath.status, 400);

    const oversized = await fetch(`${baseURL}/sign`, { method: "POST", body: "x".repeat(65) });
    assert.equal(oversized.status, 400);
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});

test("serves only complete material snapshots while a refresh is in flight", async () => {
  const oldMaterial = { seed: "old-seed", hex: "old-hex", refreshedAt: "old", pathVersion: "old-v", pathCount: 4 };
  const newMaterial = { seed: "new-seed", hex: "new-hex", refreshedAt: "new", pathVersion: "new-v", pathCount: 4 };
  let material = oldMaterial;
  let refreshInFlight = false;
  let finishRefresh;
  const refreshGate = new Promise((resolve) => { finishRefresh = resolve; });
  const collector = {
    status: () => ({ ready: true, refreshInFlight, lastError: null, material }),
    refresh: async () => {
      refreshInFlight = true;
      await refreshGate;
      material = newMaterial;
      refreshInFlight = false;
      return material;
    },
  };
  const server = createServer({ collector, apiToken: "secret" });
  const baseURL = await listen(server);
  try {
    const refresh = fetch(`${baseURL}/refresh`, { method: "POST", headers: { authorization: "Bearer secret" } });
    await new Promise((resolve) => setImmediate(resolve));
    assert.deepEqual(await (await fetch(`${baseURL}/material`, { headers: { authorization: "Bearer secret" } })).json(), oldMaterial);

    finishRefresh();
    assert.equal((await refresh).status, 200);
    assert.deepEqual(await (await fetch(`${baseURL}/material`, { headers: { authorization: "Bearer secret" } })).json(), newMaterial);
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});
