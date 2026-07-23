const MAX_RESPONSE_BYTES = 4 * 1024 * 1024;

function endpoint(value) {
  const parsed = new URL(String(value ?? "").trim());
  if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password || parsed.search || parsed.hash) {
    throw new Error("CATCH_FLARESOLVERR_URL must be a credential-free HTTP(S) URL");
  }
  const path = parsed.pathname.replace(/\/+$/, "");
  parsed.pathname = !path ? "/v1" : path === "/v1" ? path : `${path}/v1`;
  return parsed.toString();
}

export async function solveFlareSolverr({ baseURL, targetURL, proxyURL = "", timeoutMs = 90_000, fetchImpl = fetch }) {
  const payload = { cmd: "request.get", url: targetURL, maxTimeout: timeoutMs };
  if (proxyURL) payload.proxy = { url: proxyURL };
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs + 15_000);
  let response;
  try {
    response = await fetchImpl(endpoint(baseURL), {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(payload),
      signal: controller.signal,
    });
  } finally {
    clearTimeout(timer);
  }
  const body = await response.text();
  if (Buffer.byteLength(body) > MAX_RESPONSE_BYTES) throw new Error("FlareSolverr response is too large");
  if (!response.ok) throw new Error(`FlareSolverr returned HTTP ${response.status}`);
  let result;
  try { result = JSON.parse(body); } catch { throw new Error("FlareSolverr returned invalid JSON"); }
  if (result?.status !== "ok") throw new Error("FlareSolverr did not solve the challenge");
  const userAgent = String(result.solution?.userAgent ?? "").trim();
  if (!userAgent || userAgent.length > 512 || /[\x00-\x1f\x7f]/.test(userAgent)) throw new Error("FlareSolverr returned an invalid User-Agent");
  const cookies = Array.isArray(result.solution?.cookies) ? result.solution.cookies.flatMap((cookie) => {
    const name = String(cookie?.name ?? "").trim();
    const value = String(cookie?.value ?? "").trim();
    if (!name || !value || value.length > 16 * 1024 || /[\x00-\x1f\x7f]/.test(value)) return [];
    return [{ name, value, domain: new URL(targetURL).hostname, path: "/" }];
  }) : [];
  if (!cookies.length) throw new Error("FlareSolverr returned no cookies");
  return { userAgent, cookies };
}
