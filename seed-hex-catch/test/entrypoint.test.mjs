import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const entrypointURL = new URL("../docker-entrypoint.sh", import.meta.url);

test("forces the embedded FlareSolverr topology to its fixed internal port", async () => {
  const entrypoint = await readFile(entrypointURL, "utf8");

  assert.match(entrypoint, /PORT=8191 python3 -u flaresolverr\.py/);
  assert.match(entrypoint, /curl -fsS http:\/\/127\.0\.0\.1:8191\/health/);
  assert.doesNotMatch(entrypoint, /FLARESOLVERR_PORT/);
});
