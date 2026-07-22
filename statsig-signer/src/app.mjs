import http from "node:http";

import { buildStatsig, normalizeMethod, normalizePath, publicMaterialStatus } from "./statsig.mjs";

function json(res, status, value) {
  const body = JSON.stringify(value);
  res.writeHead(status, {
    "content-type": "application/json; charset=utf-8",
    "cache-control": "no-store",
    "content-length": Buffer.byteLength(body),
  });
  res.end(body);
}

async function readBody(req, maxBodyBytes) {
  const declared = Number(req.headers["content-length"] ?? 0);
  if (declared > maxBodyBytes) throw new Error("request body is too large");
  const chunks = [];
  let total = 0;
  for await (const chunk of req) {
    total += chunk.length;
    if (total > maxBodyBytes) throw new Error("request body is too large");
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString("utf8");
}

export function createSignerServer({ calibrator, apiToken = "", maxBodyBytes = 64 * 1024 }) {
  const authorized = (req) => !apiToken || req.headers.authorization === `Bearer ${apiToken}`;
  const materialStatus = () => {
    const state = calibrator.status();
    return publicMaterialStatus(state.material, state);
  };

  return http.createServer(async (req, res) => {
    try {
      const requestURL = new URL(req.url ?? "/", `http://${req.headers.host ?? "localhost"}`);
      if (req.method === "OPTIONS") {
        res.writeHead(204, { allow: "GET,POST,OPTIONS", "cache-control": "no-store" });
        return res.end();
      }
      if (requestURL.pathname === "/healthz" && req.method === "GET") {
        const status = materialStatus();
        return json(res, status.ready ? 200 : 503, status);
      }
      if (requestURL.pathname === "/refresh" && req.method === "POST") {
        if (!authorized(req)) return json(res, 401, { error: "unauthorized" });
        const material = await calibrator.refresh();
        return json(res, material ? 200 : 503, { refreshed: Boolean(material), status: materialStatus() });
      }
      if (requestURL.pathname !== "/sign" || req.method !== "POST") {
        return json(res, 404, { error: "not found" });
      }
      if (!authorized(req)) return json(res, 401, { error: "unauthorized" });
      let payload;
      try {
        payload = JSON.parse(await readBody(req, maxBodyBytes));
      } catch (error) {
        return json(res, 400, { error: error instanceof Error ? error.message : "invalid JSON" });
      }
      let method;
      let path;
      try {
        method = normalizeMethod(payload?.method);
        path = normalizePath(payload?.path);
      } catch (error) {
        return json(res, 400, { error: error instanceof Error ? error.message : "invalid method or path" });
      }
      const state = calibrator.status();
      if (!state.material) {
        return json(res, 503, { error: "Statsig browser calibration is not ready", status: materialStatus() });
      }
      try {
        return json(res, 200, { "x-statsig-id": buildStatsig(state.material, method, path) });
      } catch (error) {
        return json(res, 503, { error: error instanceof Error ? error.message : "Statsig generation failed" });
      }
    } catch (error) {
      return json(res, 500, { error: error instanceof Error ? error.message : "internal error" });
    }
  });
}
