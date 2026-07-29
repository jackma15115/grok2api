import http from "node:http";

import { buildStatsig, normalizeMethod, normalizePath } from "./statsig.mjs";

function json(response, status, value) {
  const body = JSON.stringify(value);
  response.writeHead(status, {
    "content-type": "application/json; charset=utf-8",
    "cache-control": "no-store",
    "content-length": Buffer.byteLength(body),
  });
  response.end(body);
}

async function readBody(request, maxBodyBytes) {
  const declared = Number(request.headers["content-length"] ?? 0);
  if (declared > maxBodyBytes) throw new Error("request body is too large");
  const chunks = [];
  let total = 0;
  for await (const chunk of request) {
    total += chunk.length;
    if (total > maxBodyBytes) throw new Error("request body is too large");
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString("utf8");
}

export function createServer({ collector, apiToken = "", maxBodyBytes = 64 * 1024 }) {
  const authorized = (request) => !apiToken || request.headers.authorization === `Bearer ${apiToken}`;
  return http.createServer(async (request, response) => {
    try {
      const url = new URL(request.url ?? "/", `http://${request.headers.host ?? "localhost"}`);
      if (request.method === "OPTIONS") {
        response.writeHead(204, { allow: "GET,POST,OPTIONS", "cache-control": "no-store" });
        return response.end();
      }
      if (request.method === "GET" && url.pathname === "/healthz") {
        const status = collector.status();
        return json(response, status.ready ? 200 : 503, {
          ready: status.ready,
          refreshInFlight: status.refreshInFlight,
          lastAttemptAt: status.lastAttemptAt,
          refreshedAt: status.material?.refreshedAt ?? null,
          pathVersion: status.material?.pathVersion ?? null,
          pathCount: status.material?.pathCount ?? 0,
          lastError: status.lastError,
        });
      }
      if (request.method === "GET" && url.pathname === "/material") {
        if (!authorized(request)) return json(response, 401, { error: "unauthorized" });
        const status = collector.status();
        if (!status.ready) return json(response, 503, { error: "material has not been captured" });
        return json(response, 200, status.material);
      }
      if (request.method === "POST" && url.pathname === "/refresh") {
        if (!apiToken) return json(response, 403, { error: "manual refresh is disabled" });
        if (!authorized(request)) return json(response, 401, { error: "unauthorized" });
        const material = await collector.refresh();
        return json(response, material ? 200 : 503, { refreshed: Boolean(material), status: collector.status() });
      }
      if (request.method === "POST" && url.pathname === "/sign") {
        if (!authorized(request)) return json(response, 401, { error: "unauthorized" });
        let payload;
        try {
          payload = JSON.parse(await readBody(request, maxBodyBytes));
        } catch (error) {
          return json(response, 400, { error: error instanceof Error ? error.message : "invalid JSON" });
        }
        let method;
        let path;
        try {
          method = normalizeMethod(payload?.method);
          path = normalizePath(payload?.path);
        } catch (error) {
          return json(response, 400, { error: error instanceof Error ? error.message : "invalid method or path" });
        }
        const status = collector.status();
        if (!status.ready) return json(response, 503, { error: "material has not been captured" });
        try {
          return json(response, 200, { "x-statsig-id": buildStatsig(status.material, method, path) });
        } catch (error) {
          return json(response, 503, { error: error instanceof Error ? error.message : "Statsig generation failed" });
        }
      }
      return json(response, 404, { error: "not found" });
    } catch (error) {
      return json(response, 500, { error: error instanceof Error ? error.message : "internal error" });
    }
  });
}
