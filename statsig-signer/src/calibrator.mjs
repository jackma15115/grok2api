import { chromium } from "playwright";

import { solveFlareSolverr } from "./flaresolverr.mjs";
import { extractMaterialFromCapture, materialFromConfig } from "./statsig.mjs";

const DEFAULT_TARGET_URL = "https://grok.com/";
const DEFAULT_PROBE_PATH = "/rest/rate-limits";
const DEFAULT_USER_AGENT = "";
const PROBE_HEADER = "x-grok2api-statsig-probe";

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function parseProxy(value) {
  const raw = String(value ?? "").trim();
  if (!raw) return undefined;
  const parsed = new URL(raw);
  if (!["http:", "https:", "socks5:", "socks5h:"].includes(parsed.protocol)) {
    throw new Error("SIGNER_PROXY_URL must use http, https, socks5, or socks5h");
  }
  const proxy = { server: `${parsed.protocol === "socks5h:" ? "socks5:" : parsed.protocol}//${parsed.host}` };
  if (parsed.username) proxy.username = decodeURIComponent(parsed.username);
  if (parsed.password) proxy.password = decodeURIComponent(parsed.password);
  return proxy;
}

function cookiesFromHeader(value, targetURL) {
  const header = String(value ?? "").trim();
  if (!header) return [];
  const url = new URL(targetURL);
  return header.split(";").flatMap((part) => {
    const separator = part.indexOf("=");
    if (separator <= 0) return [];
    const name = part.slice(0, separator).trim();
    const cookieValue = part.slice(separator + 1).trim();
    if (!/^[\w!#$%&'*+.^`|~-]+$/.test(name) || !cookieValue) return [];
    return [{ name, value: cookieValue, domain: url.hostname, path: "/" }];
  });
}

function mergeCookieHeaders(...headers) {
  const values = new Map();
  for (const header of headers) {
    for (const part of String(header ?? "").split(";")) {
      const separator = part.indexOf("=");
      if (separator <= 0) continue;
      const name = part.slice(0, separator).trim();
      const value = part.slice(separator + 1).trim();
      if (name && value) values.set(name, value);
    }
  }
  return [...values].map(([name, value]) => `${name}=${value}`).join("; ");
}

function createDeferred() {
  let resolve;
  const promise = new Promise((nextResolve) => { resolve = nextResolve; });
  return { promise, resolve };
}

export class BrowserCalibrator {
  constructor(options = {}) {
    this.targetURL = options.targetURL ?? process.env.SIGNER_TARGET_URL ?? DEFAULT_TARGET_URL;
    this.probePath = options.probePath ?? process.env.SIGNER_PROBE_PATH ?? DEFAULT_PROBE_PATH;
    this.probeMethod = options.probeMethod ?? process.env.SIGNER_PROBE_METHOD ?? "POST";
    this.userAgent = options.userAgent ?? process.env.SIGNER_USER_AGENT ?? DEFAULT_USER_AGENT;
    this.cookieHeader = options.cookieHeader ?? process.env.SIGNER_COOKIE ?? "";
    this.proxyURL = options.proxyURL ?? process.env.SIGNER_PROXY_URL ?? "";
    this.executablePath = options.executablePath ?? process.env.SIGNER_BROWSER_EXECUTABLE_PATH ?? "";
    this.flareSolverrURL = options.flareSolverrURL ?? process.env.SIGNER_FLARESOLVERR_URL ?? "";
    this.flareSolverrTimeoutMs = options.flareSolverrTimeoutMs ?? Number(process.env.SIGNER_FLARESOLVERR_TIMEOUT_MS ?? 60_000);
    this.headless = options.headless ?? process.env.SIGNER_HEADLESS !== "false";
    this.timeoutMs = options.timeoutMs ?? Number(process.env.SIGNER_BROWSER_TIMEOUT_MS ?? 45000);
    this.settleMs = options.settleMs ?? Number(process.env.SIGNER_PAGE_SETTLE_MS ?? 5000);
    this.material = null;
    this.state = { source: null, clearanceSource: this.cookieHeader ? "configured" : null, refreshInFlight: false, lastError: null };
    this.refreshPromise = null;
    this.fallback = null;
    if (process.env.SIGNER_FALLBACK_SEED && process.env.SIGNER_FALLBACK_HEX) {
      this.fallback = materialFromConfig(process.env.SIGNER_FALLBACK_SEED, process.env.SIGNER_FALLBACK_HEX);
      this.material = this.fallback;
      this.state.source = "configured-fallback";
    }
  }

  status() {
    return { ...this.state, material: this.material };
  }

  async start() {
    return this.refresh();
  }

  async refresh() {
    if (this.refreshPromise) return this.refreshPromise;
    this.refreshPromise = this.#refresh().finally(() => {
      this.refreshPromise = null;
    });
    return this.refreshPromise;
  }

  async #refresh() {
    this.state.refreshInFlight = true;
    this.state.lastError = null;
    let browser;
    let context;
    try {
      let cookieHeader = this.cookieHeader;
      let userAgent = this.userAgent;
      if (this.flareSolverrURL) {
        const solution = await solveFlareSolverr({
          baseURL: this.flareSolverrURL,
          targetURL: this.targetURL,
          proxyURL: this.proxyURL,
          timeoutMs: this.flareSolverrTimeoutMs,
        });
        cookieHeader = mergeCookieHeaders(cookieHeader, solution.cookieHeader);
        userAgent = solution.userAgent;
        this.state.clearanceSource = "flaresolverr";
      } else {
        this.state.clearanceSource = cookieHeader ? "configured" : null;
      }
      browser = await chromium.launch({
        headless: this.headless,
        proxy: parseProxy(this.proxyURL),
        ...(this.executablePath ? { executablePath: this.executablePath } : {}),
        args: ["--disable-dev-shm-usage"],
      });
      context = await browser.newContext({
        ...(userAgent ? { userAgent } : {}),
        locale: "en-US",
      });
      const digestInputs = [];
      await context.exposeBinding("__grok2apiReportStatsigDigest", async (_source, value) => {
        if (typeof value === "string" && value.length <= 4096) digestInputs.push(value);
      });
      await context.addInitScript({
        content: `(() => {
          const salt = ${JSON.stringify("obfiowerehiring")};
          const report = globalThis.__grok2apiReportStatsigDigest;
          const subtle = globalThis.crypto && globalThis.crypto.subtle;
          if (!subtle || typeof subtle.digest !== "function" || typeof report !== "function") return;
          const original = subtle.digest.bind(subtle);
          const wrapped = function (algorithm, data) {
            try {
              let bytes = null;
              if (data instanceof ArrayBuffer) bytes = new Uint8Array(data);
              else if (ArrayBuffer.isView(data)) bytes = new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
              if (bytes) {
                const text = new TextDecoder().decode(bytes);
                if (text.includes(salt)) Promise.resolve(report(text)).catch(() => {});
              }
            } catch (_) {}
            return original(algorithm, data);
          };
          let installed = false;
          try {
            Object.defineProperty(subtle, "digest", { value: wrapped, configurable: true });
            installed = subtle.digest === wrapped;
          } catch (_) {}
          if (!installed) {
            try {
              Object.defineProperty(Object.getPrototypeOf(subtle), "digest", { value: wrapped, configurable: true });
            } catch (_) {}
          }
        })();`,
      });
      const cookies = cookiesFromHeader(cookieHeader, this.targetURL);
      if (cookies.length) await context.addCookies(cookies);
      const page = await context.newPage();
      const observed = [];
      const probeCapture = createDeferred();
      page.on("request", async (request) => {
        try {
          const headers = await request.allHeaders();
          const statsigID = headers["x-statsig-id"];
          if (statsigID) observed.push({ statsigID, method: request.method(), path: new URL(request.url()).pathname });
        } catch (_) {}
      });
      await context.route("**/*", async (route) => {
        const request = route.request();
        try {
          const headers = await request.allHeaders();
          if (headers[PROBE_HEADER]) {
            if (headers["x-statsig-id"]) {
              probeCapture.resolve({ statsigID: headers["x-statsig-id"], method: request.method(), path: new URL(request.url()).pathname });
            }
            await route.abort();
            return;
          }
        } catch (_) {}
        await route.continue();
      });
      const response = await page.goto(this.targetURL, { waitUntil: "domcontentloaded", timeout: this.timeoutMs });
      await page.waitForTimeout(this.settleMs);
      let capture = observed.at(-1);
      if (!capture) {
        const nonce = `${Date.now()}-${Math.random().toString(36).slice(2)}`;
        const probe = page.evaluate(async ({ path, method, nonce }) => {
          const init = { method, credentials: "include", cache: "no-store", headers: { "x-grok2api-statsig-probe": nonce } };
          if (!["GET", "HEAD"].includes(method)) init.body = "{}";
          try { await fetch(path, init); } catch (_) {}
        }, { path: this.probePath, method: this.probeMethod, nonce });
        capture = await Promise.race([
          probeCapture.promise,
          delay(this.timeoutMs).then(() => null),
        ]);
        await probe;
      }
      if (!capture) {
        const status = response?.status();
        const title = await page.title().catch(() => "");
        const detail = [status ? `initial HTTP ${status}` : "", title ? `title ${JSON.stringify(title)}` : ""].filter(Boolean).join(", ");
        throw new Error(`browser did not produce an x-statsig-id request; Cloudflare or the page fetch interceptor may be blocking calibration${detail ? ` (${detail})` : ""}`);
      }
      await delay(100);
      const material = extractMaterialFromCapture({ ...capture, digestInputs });
      this.material = material;
      this.state.source = "browser";
      this.state.lastError = null;
      return material;
    } catch (error) {
      this.state.lastError = error instanceof Error ? error.message : String(error);
      if (!this.material) this.state.source = null;
      return null;
    } finally {
      this.state.refreshInFlight = false;
      await context?.close().catch(() => {});
      await browser?.close().catch(() => {});
    }
  }
}
