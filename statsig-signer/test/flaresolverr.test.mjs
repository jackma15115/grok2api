import assert from "node:assert/strict";
import test from "node:test";

import { flareSolverrEndpoint, sanitizeFlareSolverrMessage, solveFlareSolverr } from "../src/flaresolverr.mjs";

test("normalizes a FlareSolverr endpoint", () => {
  assert.equal(flareSolverrEndpoint("http://flaresolverr:8191"), "http://flaresolverr:8191/v1");
  assert.equal(flareSolverrEndpoint("https://solver.example/api/"), "https://solver.example/api/v1");
  assert.throws(() => flareSolverrEndpoint("http://user:pass@solver/v1"), /without credentials/);
});

test("returns cookies and User-Agent from a solved browser session", async () => {
  let request;
  const solution = await solveFlareSolverr({
    baseURL: "http://flaresolverr:8191",
    targetURL: "https://grok.com/",
    proxyURL: "socks5://warp:1080",
    fetchImpl: async (url, init) => {
      request = { url, payload: JSON.parse(init.body) };
      return new Response(JSON.stringify({
        status: "ok",
        solution: {
          userAgent: "Mozilla/5.0 Chrome/146.0.0.0 Safari/537.36",
          cookies: [
            { name: "cf_clearance", value: "clear" },
            { name: "__cf_bm", value: "bm" },
            { name: "sso", value: "must-not-leak" },
          ],
        },
      }));
    },
  });
  assert.equal(request.url, "http://flaresolverr:8191/v1");
  assert.equal(request.payload.proxy.url, "socks5://warp:1080");
  assert.equal(solution.cookieHeader, "cf_clearance=clear; __cf_bm=bm");
  assert.match(solution.userAgent, /Chrome/);
});

test("redacts credentials from FlareSolverr errors", async () => {
  const fetchImpl = async () => new Response(JSON.stringify({
    status: "error",
    message: "proxy socks5://user:secret@resin:2260 failed; token=abc Authorization: Bearer.SECRET",
  }));
  await assert.rejects(
    solveFlareSolverr({ baseURL: "http://flaresolverr:8191", targetURL: "https://grok.com/", fetchImpl }),
    (error) => {
      assert.doesNotMatch(error.message, /secret|abc|Bearer\.SECRET/);
      assert.match(error.message, /\*\*\*:\*\*\*@/);
      return true;
    },
  );
  assert.equal(sanitizeFlareSolverrMessage("password=hunter2"), "password=[redacted]");
});
