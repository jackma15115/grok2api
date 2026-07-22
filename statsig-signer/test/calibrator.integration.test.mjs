import assert from "node:assert/strict";
import http from "node:http";
import test from "node:test";

import { BrowserCalibrator } from "../src/calibrator.mjs";

const executablePath = process.env.SIGNER_TEST_BROWSER;
const seed = Array.from({ length: 48 }, (_, index) => index + 1);
const seedBase64 = Buffer.from(seed).toString("base64").replace(/=+$/, "");
const hex = "ad36d100100";

const fixtureHTML = `<!doctype html>
<script>
const seed = new Uint8Array(${JSON.stringify(seed)});
const hex = ${JSON.stringify(hex)};
const originalFetch = window.fetch.bind(window);
window.fetch = async function(input, init = {}) {
  const url = new URL(input, location.href);
  const method = String(init.method || "GET").toUpperCase();
  const number = Math.floor(Date.now() / 1000) - 1682924400;
  const digestInput = method + "!" + url.pathname + "!" + number + "obfiowerehiring" + hex;
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(digestInput)));
  const key = 37;
  const output = new Uint8Array(70);
  output[0] = key;
  for (let i = 0; i < 48; i += 1) output[i + 1] = seed[i] ^ key;
  output[49] = number ^ key;
  output[50] = (number >>> 8) ^ key;
  output[51] = (number >>> 16) ^ key;
  output[52] = (number >>> 24) ^ key;
  for (let i = 0; i < 16; i += 1) output[i + 53] = digest[i] ^ key;
  output[69] = 3 ^ key;
  let binary = "";
  for (const value of output) binary += String.fromCharCode(value);
  const headers = new Headers(init.headers || {});
  headers.set("x-statsig-id", btoa(binary).replace(/=+$/, ""));
  return originalFetch(input, { ...init, headers });
};
</script>`;

test("calibrates from a browser request and matching Web Crypto input", { skip: !executablePath }, async () => {
  const server = http.createServer((req, res) => {
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
    const calibrator = new BrowserCalibrator({ targetURL, executablePath, timeoutMs: 10_000, settleMs: 100 });
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
