import http from "node:http";

function json(response, status, value) {
  const body = JSON.stringify(value);
  response.writeHead(status, {
    "content-type": "application/json; charset=utf-8",
    "cache-control": "no-store",
    "content-length": Buffer.byteLength(body),
  });
  response.end(body);
}

export function createServer({ collector, apiToken = "" }) {
  const authorized = (request) => !apiToken || request.headers.authorization === `Bearer ${apiToken}`;
  return http.createServer(async (request, response) => {
    const url = new URL(request.url ?? "/", `http://${request.headers.host ?? "localhost"}`);
    if (request.method === "GET" && url.pathname === "/healthz") {
      const status = collector.status();
      return json(response, status.ready ? 200 : 503, {
        ready: status.ready,
        refreshInFlight: status.refreshInFlight,
        lastAttemptAt: status.lastAttemptAt,
        refreshedAt: status.material?.refreshedAt ?? null,
        expiresAt: status.material?.expiresAt ?? null,
        pathVersion: status.material?.pathVersion ?? null,
        pathCount: status.material?.pathCount ?? 0,
        lastError: status.lastError,
      });
    }
    if (request.method === "GET" && url.pathname === "/material") {
      if (!authorized(request)) return json(response, 401, { error: "unauthorized" });
      const status = collector.status();
      if (!status.ready) return json(response, 503, { error: "material is not ready" });
      return json(response, 200, status.material);
    }
    if (request.method === "POST" && url.pathname === "/refresh") {
      if (!apiToken) return json(response, 403, { error: "manual refresh is disabled" });
      if (!authorized(request)) return json(response, 401, { error: "unauthorized" });
      const material = await collector.refresh();
      return json(response, material ? 200 : 503, { refreshed: Boolean(material), status: collector.status() });
    }
    return json(response, 404, { error: "not found" });
  });
}
