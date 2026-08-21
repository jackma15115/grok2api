import assert from "node:assert/strict";
import test from "node:test";

import { solveFlareSolverr } from "../src/flaresolverr.mjs";

test("accepts a successful no-challenge solution without cookies", async () => {
  let request;
  const fetchImpl = async (url, init) => {
    request = { url, init };
    return new Response(JSON.stringify({
      status: "ok",
      solution: {
        userAgent: "Mozilla/5.0 Chrome/148.0.0.0 Safari/537.36",
        cookies: [],
      },
    }), { status: 200 });
  };

  const solution = await solveFlareSolverr({
    baseURL: "http://flaresolverr:8191",
    targetURL: "https://grok.com/",
    fetchImpl,
  });

  assert.equal(request.url, "http://flaresolverr:8191/v1");
  assert.deepEqual(solution.cookies, []);
  assert.match(solution.userAgent, /Chrome\/148/);
});
