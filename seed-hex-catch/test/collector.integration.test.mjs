import assert from "node:assert/strict";
import http from "node:http";
import test from "node:test";

import { SVGMaterialCollector } from "../src/collector.mjs";

const executablePath = process.env.CATCH_TEST_BROWSER;
const seed = Array.from({ length: 48 }, (_, index) => index + 1);
const seedBase64 = Buffer.from(seed).toString("base64").replace(/=+$/g, "");
const hex = "ad36d100100";

const fixtureHTML = `<!doctype html>
<script>
const seed = new Uint8Array(${JSON.stringify(seed)});
const originalFetch = window.fetch.bind(window);
window.fetch = async function(input, init = {}) {
  const url = new URL(input, location.href);
  const method = String(init.method || 'GET').toUpperCase();
  const number = Math.floor(Date.now() / 1000) - 1682924400;
  const digestInput = method + '!' + url.pathname + '!' + number + 'obfiowerehiring' + ${JSON.stringify(hex)};
  const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(digestInput)));
  const key = 37;
  const output = new Uint8Array(70);
  output[0] = key;
  for (let index = 0; index < 48; index += 1) output[index + 1] = seed[index] ^ key;
  output[49] = number ^ key;
  output[50] = (number >>> 8) ^ key;
  output[51] = (number >>> 16) ^ key;
  output[52] = (number >>> 24) ^ key;
  for (let index = 0; index < 16; index += 1) output[index + 53] = digest[index] ^ key;
  output[69] = 3 ^ key;
  let binary = '';
  for (const value of output) binary += String.fromCharCode(value);
  const headers = new Headers(init.headers || {});
  headers.set('x-statsig-id', btoa(binary).replace(/=+$/g, ''));
  return originalFetch(input, { ...init, headers });
};
</script>`;

test("collects material from a browser Statsig probe", { skip: !executablePath }, async () => {
  const server = http.createServer((request, response) => {
    if (request.url === "/v1" && request.method === "POST") {
      response.writeHead(200, { "content-type": "application/json" });
      response.end(JSON.stringify({
        status: "ok",
        solution: {
          userAgent: "Mozilla/5.0 Chrome/148.0.0.0 Safari/537.36",
          cookies: [],
        },
      }));
      return;
    }
    if (request.url === "/") {
      response.writeHead(200, { "content-type": "text/html" });
      response.end(fixtureHTML);
      return;
    }
    response.writeHead(204);
    response.end();
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const targetURL = `http://127.0.0.1:${server.address().port}/`;
  try {
    const collector = new SVGMaterialCollector({
      targetURL,
      flareSolverrURL: targetURL,
      executablePath,
      browserTimeoutMs: 10_000,
      pageSettleMs: 100,
    });
    const material = await collector.refresh();
    assert.equal(collector.status().lastError, null);
    assert.equal(material.seed, seedBase64);
    assert.equal(material.hex, hex);
    assert.equal(material.digestLength, 16);
    assert.equal(material.hasMarker, true);
    assert.equal(material.prefix, "");
    assert.equal(material.capturedMethod, "POST");
    assert.equal(material.capturedPath, "/rest/rate-limits");
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});
