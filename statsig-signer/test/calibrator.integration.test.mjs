import assert from "node:assert/strict";
import http from "node:http";
import test from "node:test";

import { BrowserCalibrator } from "../src/calibrator.mjs";

const executablePath = process.env.SIGNER_TEST_BROWSER;
const seed = Array.from({ length: 48 }, (_, index) => index + 1);
const seedBase64 = Buffer.from(seed).toString("base64").replace(/=+$/, "");
const hex = "ad36d100100";
const prefix = [0x02, 0x01];

const fixtureHTML = `<!doctype html>
<script>
const seed = new Uint8Array(${JSON.stringify(seed)});
const hex = ${JSON.stringify(hex)};
const originalFetch = window.fetch.bind(window);
window.fetch = async function(input, init = {}) {
  if (!document.cookie.includes("cf_clearance=clear") || !navigator.userAgent.includes("Chrome/146")) {
    return originalFetch(input, init);
  }
  const url = new URL(input, location.href);
  const method = String(init.method || "GET").toUpperCase();
  const number = Math.floor(Date.now() / 1000) - 1682924400;
  const digestInput = method + "!" + url.pathname + "!" + number + "obfiowerehiring" + hex;
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(digestInput)));
  const key = 37;
  const output = new Uint8Array(72);
  output.set(${JSON.stringify(prefix)});
  const offset = 2;
  output[offset] = key;
  for (let i = 0; i < 48; i += 1) output[offset + i + 1] = seed[i] ^ key;
  output[offset + 49] = number ^ key;
  output[offset + 50] = (number >>> 8) ^ key;
  output[offset + 51] = (number >>> 16) ^ key;
  output[offset + 52] = (number >>> 24) ^ key;
  for (let i = 0; i < 16; i += 1) output[offset + i + 53] = digest[i] ^ key;
  output[71] = 3 ^ key;
  let binary = "";
  for (const value of output) binary += String.fromCharCode(value);
  const headers = new Headers(init.headers || {});
  headers.set("x-statsig-id", btoa(binary).replace(/=+$/, ""));
  return originalFetch(input, { ...init, headers });
};
</script>`;

test("calibrates from a browser request and matching Web Crypto input", { skip: !executablePath }, async () => {
  const server = http.createServer((req, res) => {
    if (req.url === "/v1" && req.method === "POST") {
      res.writeHead(200, { "content-type": "application/json" });
      return res.end(JSON.stringify({
        status: "ok",
        solution: {
          userAgent: "Mozilla/5.0 Chrome/146.0.0.0 Safari/537.36",
          cookies: [{ name: "cf_clearance", value: "clear" }],
        },
      }));
    }
    if (req.url === "/") {
      res.writeHead(200, { "content-type": "text/html" });
      return res.end(fixtureHTML);
    }
    res.writeHead(204);
    res.end();
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const targetURL = `http://127.0.0.1:${server.address().port}/`;
  try {
    const calibrator = new BrowserCalibrator({ targetURL, flareSolverrURL: targetURL, executablePath, timeoutMs: 10_000, settleMs: 100 });
    const material = await calibrator.refresh();
    assert.equal(calibrator.status().lastError, null);
    assert.equal(material.seed, seedBase64);
    assert.equal(material.hex, hex);
    assert.equal(material.prefix, "AgE");
    assert.equal(material.digestLength, 16);
    assert.equal(material.hasMarker, true);
    assert.equal(material.capturedMethod, "POST");
    assert.equal(material.capturedPath, "/rest/rate-limits");
    assert.equal(calibrator.status().clearanceSource, "flaresolverr");
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});

test("calibrates through Grok's Turbopack signer when fetch is not wrapped", { skip: !executablePath }, async () => {
  const runtimeHTML = `<!doctype html><title>Grok</title><script>
globalThis.TURBOPACK = [];
const seed = new Uint8Array(${JSON.stringify(seed)});
globalThis.TURBOPACK.push = function(entry) {
  if (Array.isArray(entry) && typeof entry[1] === 'number' && typeof entry[2] === 'function') {
    entry[2]({ i: () => ({ botoxSign: async (path, method) => {
      const number = Math.floor(Date.now() / 1000) - 1682924400;
      const input = method + '!' + path + '!' + number + 'obfiowerehiring' + ${JSON.stringify(hex)};
      const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(input)));
      const key = 37;
      const output = new Uint8Array(70);
      output[0] = key;
      for (let index = 0; index < 48; index++) output[index + 1] = seed[index] ^ key;
      output[49] = number ^ key;
      output[50] = (number >>> 8) ^ key;
      output[51] = (number >>> 16) ^ key;
      output[52] = (number >>> 24) ^ key;
      for (let index = 0; index < 16; index++) output[index + 53] = digest[index] ^ key;
      output[69] = 3 ^ key;
      let binary = '';
      for (const value of output) binary += String.fromCharCode(value);
      return btoa(binary).replace(/=+$/g, '');
    } })});
  }
};
</script>`;
  const server = http.createServer((req, res) => {
    if (req.url === "/v1" && req.method === "POST") {
      res.writeHead(200, { "content-type": "application/json" });
      return res.end(JSON.stringify({ status: "ok", solution: { userAgent: "Mozilla/5.0 Chrome/146.0.0.0 Safari/537.36", cookies: [{ name: "cf_clearance", value: "clear" }] } }));
    }
    if (req.url === "/") {
      res.writeHead(200, { "content-type": "text/html" });
      return res.end(runtimeHTML);
    }
    res.writeHead(204);
    res.end();
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const targetURL = `http://127.0.0.1:${server.address().port}/`;
  try {
    const calibrator = new BrowserCalibrator({ targetURL, flareSolverrURL: targetURL, executablePath, timeoutMs: 5000, settleMs: 25 });
    const material = await calibrator.refresh();
    assert.equal(calibrator.status().lastError, null);
    assert.equal(material.seed, seedBase64);
    assert.equal(material.hex, hex);
    assert.equal(material.capturedMethod, "POST");
    assert.equal(material.capturedPath, "/rest/rate-limits");
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});
