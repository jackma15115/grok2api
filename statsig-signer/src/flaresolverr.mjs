const MAX_RESPONSE_BYTES = 2 * 1024 * 1024;
const MAX_COOKIE_BYTES = 16 * 1024;
const PROXY_CREDENTIAL_PATTERN = /\b(https?|socks4a?|socks5h?):\/\/[^\s/@:]+:[^\s/@]+@/gi;
const BEARER_CREDENTIAL_PATTERN = /\bbearer\s+[a-z0-9._~+/=-]+/gi;
const NAMED_CREDENTIAL_PATTERN = /\b(token|password|passwd|authorization|cookie)\s*[:=]\s*[^\s,;]+/gi;

export function sanitizeFlareSolverrMessage(value) {
  return String(value ?? "")
    .trim()
    .replace(/[\x00-\x1f\x7f]/g, "")
    .replace(PROXY_CREDENTIAL_PATTERN, (candidate) => `${candidate.slice(0, candidate.indexOf("://") + 3)}***:***@`)
    .replace(BEARER_CREDENTIAL_PATTERN, "Bearer [redacted]")
    .replace(NAMED_CREDENTIAL_PATTERN, "$1=[redacted]")
    .slice(0, 300);
}

export function flareSolverrEndpoint(value) {
  const parsed = new URL(String(value ?? "").trim());
  if (!["http:", "https:"].includes(parsed.protocol) || !parsed.hostname || parsed.username || parsed.password || parsed.search || parsed.hash) {
    throw new Error("SIGNER_FLARESOLVERR_URL must be an HTTP(S) URL without credentials, query, or fragment");
  }
  const path = parsed.pathname.replace(/\/+$/, "");
  parsed.pathname = !path ? "/v1" : path === "/v1" ? path : `${path}/v1`;
  return parsed.toString();
}

function isCloudflareCookie(name) {
  const normalized = name.toLowerCase();
  return normalized === "cf_clearance"
    || normalized === "__cf_bm"
    || normalized === "_cfuvid"
    || normalized.startsWith("cf_chl_");
}

export async function solveFlareSolverr({ baseURL, targetURL, proxyURL = "", timeoutMs = 60_000, fetchImpl = fetch }) {
  const endpoint = flareSolverrEndpoint(baseURL);
  const payload = {
    cmd: "request.get",
    url: targetURL,
    maxTimeout: timeoutMs,
  };
  if (proxyURL) payload.proxy = { url: proxyURL };

  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs + 15_000);
  let response;
  try {
    response = await fetchImpl(endpoint, {
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
  try {
    result = JSON.parse(body);
  } catch {
    throw new Error("FlareSolverr returned invalid JSON");
  }
  if (result?.status !== "ok") {
    const message = sanitizeFlareSolverrMessage(result?.message) || "unknown error";
    throw new Error(`FlareSolverr solve failed: ${message}`);
  }
  const userAgent = String(result?.solution?.userAgent ?? "").trim();
  if (!userAgent || userAgent.length > 512 || /[\x00-\x1f\x7f]/.test(userAgent)) {
    throw new Error("FlareSolverr returned an invalid User-Agent");
  }
  const cookieHeader = Array.isArray(result?.solution?.cookies)
    ? result.solution.cookies.flatMap((cookie) => {
      const name = String(cookie?.name ?? "").trim();
      const value = String(cookie?.value ?? "").trim();
      return name
        && isCloudflareCookie(name)
        && value
        && value.length <= MAX_COOKIE_BYTES
        && !/[\x00-\x1f\x7f]/.test(value)
        && /^[\w!#$%&'*+.^`|~-]+$/.test(name)
        ? [`${name.toLowerCase()}=${value}`]
        : [];
    }).join("; ")
    : "";
  if (!cookieHeader) throw new Error("FlareSolverr returned no usable cookies");
  return { cookieHeader, userAgent };
}
