import assert from "node:assert/strict";
import test from "node:test";

import { launchCaptureBrowser } from "../src/browser.mjs";

test("retries a Chromium launch SIGSEGV once with the same options", async () => {
  const options = { executablePath: "/usr/bin/chromium", headless: true };
  const browser = {};
  let calls = 0;
  const result = await launchCaptureBrowser(options, async (actual) => {
    assert.equal(actual, options);
    if (++calls === 1) throw new Error("browserType.launch: closed\nprocess did exit: exitCode=null, signal=SIGSEGV");
    return browser;
  });

  assert.equal(result, browser);
  assert.equal(calls, 2);
});

test("reports a persistent launch failure after the single retry", async () => {
  const failure = new Error("browserType.launch: closed; signal=SIGSEGV");
  let calls = 0;
  await assert.rejects(launchCaptureBrowser({}, async () => {
    calls++;
    throw failure;
  }), (error) => error === failure);
  assert.equal(calls, 2);
});

test("does not retry other launch errors", async () => {
  for (const message of ["Executable doesn't exist", "browserType.launch: Target page, context or browser has been closed"]) {
    const failure = new Error(message);
    let calls = 0;
    await assert.rejects(launchCaptureBrowser({}, async () => {
      calls++;
      throw failure;
    }), (error) => error === failure);
    assert.equal(calls, 1);
  }
});
