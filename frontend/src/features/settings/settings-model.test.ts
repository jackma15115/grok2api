import assert from "node:assert/strict";
import { it } from "node:test";

import { settingsSchema } from "./settings-model.ts";

const webSettings = {
  baseURL: "https://grok.com",
  statsigMode: "disabled",
  statsigManualValue: "",
  statsigManualConfigured: false,
  statsigSignerURL: "",
  statsigMaterialURL: "",
  clearanceMode: "manual",
  flareSolverrURL: "",
  clearanceTimeout: { value: 60, unit: "s" },
  clearanceRefresh: { value: 10, unit: "m" },
  quotaTimeout: { value: 25, unit: "s" },
  chatTimeout: { value: 120, unit: "s" },
  streamIdleTimeout: { value: 60, unit: "s" },
  imageTimeout: { value: 3, unit: "m" },
  videoTimeout: { value: 15, unit: "m" },
  mediaConcurrency: 4,
  allowNSFW: false,
  freeVideoDurationCap: 6,
  recoveryBackoffBase: { value: 30, unit: "s" },
  recoveryBackoffMax: { value: 30, unit: "m" },
};

it("saves disabled signing without signer or material configuration", () => {
  const result = settingsSchema.shape.providerWeb.safeParse(webSettings);
  assert.equal(result.success, true);
});

it("inactive legacy inputs do not block disabling signing", () => {
  const result = settingsSchema.shape.providerWeb.safeParse({
    ...webSettings,
    statsigManualValue: "an old invalid value",
    statsigSignerURL: "unavailable-signer",
    statsigMaterialURL: "unavailable-material",
  });
  assert.equal(result.success, true);
});

it("validates the selected legacy mode", () => {
  const schema = settingsSchema.shape.providerWeb;
  assert.equal(schema.safeParse({ ...webSettings, statsigMode: "url" }).success, false);
  assert.equal(schema.safeParse({ ...webSettings, statsigMode: "url", statsigSignerURL: "http://seed-hex-catch:8789/sign" }).success, true);
  assert.equal(schema.safeParse({ ...webSettings, statsigMode: "local" }).success, true);
  assert.equal(schema.safeParse({ ...webSettings, statsigMode: "manual" }).success, false);
  assert.equal(schema.safeParse({ ...webSettings, statsigMode: "manual", statsigManualConfigured: true }).success, true);
});
