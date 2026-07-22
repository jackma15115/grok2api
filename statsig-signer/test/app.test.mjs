import assert from "node:assert/strict";
import test from "node:test";

import { createSignerServer } from "../src/app.mjs";
import { decodeStatsigID, materialFromConfig } from "../src/statsig.mjs";

async function listen(server) {
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  return `http://127.0.0.1:${server.address().port}`;
}

async function close(server) {
  await new Promise((resolve) => server.close(resolve));
}

test("serves the compatible sign API and protects authenticated deployments", async () => {
  const material = materialFromConfig(Buffer.alloc(48, 9).toString("base64"), "deadbeef");
  const calibrator = {
    status: () => ({ material, source: "test", refreshInFlight: false, lastError: null }),
    refresh: async () => material,
  };
  const server = createSignerServer({ calibrator, apiToken: "secret" });
  const baseURL = await listen(server);
  try {
    const health = await fetch(`${baseURL}/healthz`);
    assert.equal(health.status, 200);
    assert.equal((await health.json()).source, "test");

    const unauthorized = await fetch(`${baseURL}/sign`, { method: "POST", body: "{}" });
    assert.equal(unauthorized.status, 401);

    const signed = await fetch(`${baseURL}/sign`, {
      method: "POST",
      headers: { authorization: "Bearer secret", "content-type": "application/json" },
      body: JSON.stringify({ method: "POST", path: "/rest/rate-limits", environment: { metaContent: "compatible" } }),
    });
    assert.equal(signed.status, 200);
    const value = (await signed.json())["x-statsig-id"];
    assert.equal(decodeStatsigID(value).decoded.length, 70);
  } finally {
    await close(server);
  }
});

test("reports not-ready calibration without returning a fake header", async () => {
  const calibrator = {
    status: () => ({ material: null, source: null, refreshInFlight: false, lastError: "blocked" }),
    refresh: async () => null,
  };
  const server = createSignerServer({ calibrator });
  const baseURL = await listen(server);
  try {
    const response = await fetch(`${baseURL}/sign`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ method: "POST", path: "/rest/test" }),
    });
    assert.equal(response.status, 503);
    assert.equal((await response.json()).status.lastError, "blocked");
  } finally {
    await close(server);
  }
});
