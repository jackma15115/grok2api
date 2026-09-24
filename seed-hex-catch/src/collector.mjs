import { createHash } from "node:crypto";
import { chromium } from "playwright";

import { solveFlareSolverr } from "./flaresolverr.mjs";
import { computeStyleHEX, validateMaterial } from "./hex.mjs";
import { currentMaterialStatus } from "./material.mjs";
import { describeCaptureMismatch, extractMaterialFromCapture, STATSIG_SALT } from "./statsig.mjs";

const PROBE_HEADER = "x-grok2api-statsig-probe";

function createDeferred() {
  let resolve;
  const promise = new Promise((nextResolve) => { resolve = nextResolve; });
  return { promise, resolve };
}

async function waitWithTimeout(promise, timeoutMs) {
  let timer;
  try {
    return await Promise.race([
      promise,
      new Promise((resolve) => { timer = setTimeout(() => resolve(null), timeoutMs); }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

function parseProxy(value) {
  const raw = String(value ?? "").trim();
  if (!raw) return undefined;
  const parsed = new URL(raw);
  if (!["http:", "https:", "socks5:", "socks5h:"].includes(parsed.protocol)) throw new Error("unsupported proxy protocol");
  const proxy = { server: `${parsed.protocol === "socks5h:" ? "socks5:" : parsed.protocol}//${parsed.host}` };
  if (parsed.username) proxy.username = decodeURIComponent(parsed.username);
  if (parsed.password) proxy.password = decodeURIComponent(parsed.password);
  return proxy;
}

function cookiesFromSSO(value, targetURL) {
  const sso = String(value ?? "").trim();
  if (!sso) return [];
  if (/[;\x00-\x1f\x7f]/.test(sso)) throw new Error("CATCH_SSO contains invalid characters");
  const domain = new URL(targetURL).hostname;
  return ["sso", "sso-rw"].map((name) => ({ name, value: sso, domain, path: "/" }));
}

function signedProbeScript() {
  return async ({ path, method, nonce }) => {
    let signer;
    try {
      const chunks = globalThis.TURBOPACK;
      if (chunks && typeof chunks.push === "function") {
        const runtimeId = 990000001;
        const source = document.createElement("script");
        chunks.push([source, { otherChunks: [], runtimeModuleIds: [runtimeId] }, (runtime) => {
          try { signer = runtime.i(831076).botoxSign; } catch (_) {}
        }]);
        const deadline = Date.now() + 5000;
        while (typeof signer !== "function" && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 25));
      }
    } catch (_) {}
    const headers = { "x-grok2api-statsig-probe": nonce };
    if (typeof signer === "function") {
      try { headers["x-statsig-id"] = await signer(path, method); } catch (_) {}
    }
    const init = { method, credentials: "include", cache: "no-store", headers };
    if (!["GET", "HEAD"].includes(method)) init.body = "{}";
    let fetchError = "";
    let fetchStatus = null;
    try { fetchStatus = (await fetch(path, init)).status; } catch (error) { fetchError = String(error?.message || error); }
    return { statsigID: headers["x-statsig-id"] || null, fetchError, fetchStatus };
  };
}

function materialCaptureScript() {
  return `(() => {
    globalThis.__seedHexCatch = { digestInputs: [], paths: [], selected: null, styles: [] };
    const state = globalThis.__seedHexCatch;
    const rememberSVGs = (root) => {
      if (!root || root.nodeType !== Node.ELEMENT_NODE) return;
      const svgs = root.matches?.('svg[id^="loading-x-anim-"]') ? [root] : Array.from(root.querySelectorAll?.('svg[id^="loading-x-anim-"]') || []);
      for (const svg of svgs) {
        const index = Number(String(svg.id).slice('loading-x-anim-'.length));
        const path = svg.querySelectorAll('path')[1]?.attributes?.getNamedItem('d')?.value || '';
        if (index >= 0 && index < 4 && path.startsWith('M 10,30 C ')) state.paths[index] = path;
      }
    };
    new MutationObserver((records) => {
      for (const record of records) for (const node of record.addedNodes) rememberSVGs(node);
    }).observe(document, { childList: true, subtree: true });

    const originalGetAttribute = Element.prototype.getAttribute;
    Element.prototype.getAttribute = function (name) {
      const value = originalGetAttribute.call(this, name);
      if (name === 'd' && typeof value === 'string' && value.startsWith('M 10,30 C ')) {
        const id = this.closest('svg')?.id || '';
        const index = Number(id.slice('loading-x-anim-'.length));
        if (id.startsWith('loading-x-anim-') && index >= 0 && index < 4) state.selected = { index, path: value };
      }
      return value;
    };

    const salt = ${JSON.stringify(STATSIG_SALT)};
    const report = globalThis.__seedHexReportDigest;
    const rememberDigestInput = (value) => {
      if (typeof value === 'string' && value.length <= 4096 && value.includes(salt)) {
        state.digestInputs.push(value);
        if (typeof report === 'function') Promise.resolve(report(value)).catch(() => {});
      }
    };
    const textEncoderPrototype = globalThis.TextEncoder?.prototype;
    if (textEncoderPrototype) {
      const originalEncode = textEncoderPrototype.encode;
      if (typeof originalEncode === 'function') textEncoderPrototype.encode = function (value) {
        try { rememberDigestInput(value); } catch (_) {}
        return originalEncode.call(this, value);
      };
      const originalEncodeInto = textEncoderPrototype.encodeInto;
      if (typeof originalEncodeInto === 'function') textEncoderPrototype.encodeInto = function (value, destination) {
        try { rememberDigestInput(value); } catch (_) {}
        return originalEncodeInto.call(this, value, destination);
      };
    }

    const subtle = globalThis.crypto && globalThis.crypto.subtle;
    if (subtle && typeof subtle.digest === 'function') {
      const originalDigest = subtle.digest.bind(subtle);
      const wrappedDigest = function (algorithm, data) {
        try {
          let bytes = null;
          if (data instanceof ArrayBuffer) bytes = new Uint8Array(data);
          else if (ArrayBuffer.isView(data)) bytes = new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
          rememberDigestInput(bytes ? new TextDecoder().decode(bytes) : '');
        } catch (_) {}
        return originalDigest(algorithm, data);
      };
      let installed = false;
      try {
        Object.defineProperty(subtle, 'digest', { value: wrappedDigest, configurable: true });
        installed = subtle.digest === wrappedDigest;
      } catch (_) {}
      if (!installed) try {
        Object.defineProperty(Object.getPrototypeOf(subtle), 'digest', { value: wrappedDigest, configurable: true });
      } catch (_) {}
    }

    const originalComputedStyle = globalThis.getComputedStyle.bind(globalThis);
    globalThis.getComputedStyle = function (element, pseudoElement) {
      const style = originalComputedStyle(element, pseudoElement);
      try {
        if (element?.tagName === 'DIV' && element.childElementCount === 0) {
          const animation = element.getAnimations().find((item) => item.effect?.getComputedTiming()?.duration === 4096);
          if (animation) state.styles.push({ color: style.color, transform: style.transform });
        }
      } catch (_) {}
      return style;
    };
  })();`;
}

export class SVGMaterialCollector {
  constructor(options = {}) {
    this.targetURL = options.targetURL ?? process.env.CATCH_TARGET_URL ?? "https://grok.com/";
    this.flareSolverrURL = options.flareSolverrURL ?? process.env.CATCH_FLARESOLVERR_URL ?? "http://127.0.0.1:8191";
    this.flareSolverrTimeoutMs = options.flareSolverrTimeoutMs ?? Number(process.env.CATCH_FLARESOLVERR_TIMEOUT_MS ?? 90_000);
    this.browserTimeoutMs = options.browserTimeoutMs ?? Number(process.env.CATCH_BROWSER_TIMEOUT_MS ?? 60_000);
    this.pageSettleMs = options.pageSettleMs ?? Number(process.env.CATCH_PAGE_SETTLE_MS ?? 5_000);
    this.probePath = options.probePath ?? process.env.CATCH_PROBE_PATH ?? "/rest/rate-limits";
    this.probeMethod = options.probeMethod ?? process.env.CATCH_PROBE_METHOD ?? "POST";
    this.executablePath = options.executablePath ?? process.env.CATCH_BROWSER_EXECUTABLE_PATH ?? "";
    this.proxyURL = options.proxyURL ?? process.env.CATCH_PROXY_URL ?? "";
    this.sso = options.sso ?? process.env.CATCH_SSO ?? "";
    this.headless = options.headless ?? process.env.CATCH_HEADLESS !== "false";
    this.material = null;
    this.refreshPromise = null;
    this.state = { refreshInFlight: false, lastError: null, lastAttemptAt: null };
  }

  status() {
    return currentMaterialStatus(this.material, this.state);
  }

  refresh() {
    if (this.refreshPromise) return this.refreshPromise;
    this.refreshPromise = this.#refresh().finally(() => { this.refreshPromise = null; });
    return this.refreshPromise;
  }

  async #refresh() {
    this.state.refreshInFlight = true;
    this.state.lastAttemptAt = new Date().toISOString();
    let browser;
    let context;
    try {
      const clearance = await solveFlareSolverr({
        baseURL: this.flareSolverrURL,
        targetURL: this.targetURL,
        proxyURL: this.proxyURL,
        timeoutMs: this.flareSolverrTimeoutMs,
      });
      browser = await chromium.launch({
        headless: this.headless,
        proxy: parseProxy(this.proxyURL),
        ...(this.executablePath ? { executablePath: this.executablePath } : {}),
        args: ["--disable-dev-shm-usage"],
      });
      context = await browser.newContext({ userAgent: clearance.userAgent, locale: "en-US" });
      await context.addCookies([...clearance.cookies, ...cookiesFromSSO(this.sso, this.targetURL)]);
      const page = await context.newPage();
      const digestInputs = [];
      await context.exposeBinding("__seedHexReportDigest", async (_source, value) => {
        if (typeof value === "string" && value.length <= 4096 && value.includes(STATSIG_SALT)) digestInputs.push(value);
      });
      await page.addInitScript({ content: materialCaptureScript() });
      const observed = [];
      const probeCapture = createDeferred();
      let activeProbeNonce = "";
      let probeSummary = "not-run";
      const requestSummary = [];
      const normalizeHeaders = (headers) => Object.fromEntries(Object.entries(headers ?? {}).map(([name, value]) => [name.toLowerCase(), value]));
      const rememberRequest = (request, headers) => {
        const normalized = normalizeHeaders(headers);
        const statsigID = normalized["x-statsig-id"];
        if (!statsigID) return;
        try {
          const capture = { statsigID, method: request.method(), path: new URL(request.url()).pathname };
          observed.push(capture);
          if (activeProbeNonce && normalized[PROBE_HEADER] === activeProbeNonce) probeCapture.resolve(capture);
        } catch (_) {}
      };
      page.on("request", async (request) => {
        try {
          const headers = await request.allHeaders();
          if (requestSummary.length < 40) requestSummary.push(`${request.method()} ${new URL(request.url()).pathname} statsig=${Boolean(normalizeHeaders(headers)["x-statsig-id"])}`);
          rememberRequest(request, headers);
        } catch (_) {}
      });
      page.on("request", (request) => {
        if (requestSummary.length < 40 && !requestSummary.some((value) => value.startsWith(`${request.method()} ${new URL(request.url()).pathname} `))) {
          requestSummary.push(`${request.method()} ${new URL(request.url()).pathname} event`);
        }
      });
      await context.route("**/*", async (route) => {
        const request = route.request();
        try {
          const headers = await request.allHeaders();
          if (normalizeHeaders(headers)[PROBE_HEADER]) {
            rememberRequest(request, headers);
            await route.abort();
            return;
          }
        } catch (_) {}
        await route.continue();
      });
      const response = await page.goto(this.targetURL, { waitUntil: "domcontentloaded", timeout: this.browserTimeoutMs });
      await page.waitForTimeout(this.pageSettleMs);
      let captured = await page.evaluate(() => structuredClone(globalThis.__seedHexCatch));
      let hexCandidates = [];
      let extracted = null;
      const mismatches = new Set();
      const tryExtract = () => {
        hexCandidates = captured.styles.flatMap((style) => {
          try { return [computeStyleHEX(style.color, style.transform)]; } catch { return []; }
        });
        for (const capture of observed.toReversed()) {
          try {
            return extractMaterialFromCapture({ ...capture, digestInputs: [...captured.digestInputs, ...digestInputs], hexCandidates });
          } catch (_) {
            mismatches.add(describeCaptureMismatch({ ...capture, digestInputs: [...captured.digestInputs, ...digestInputs], hexCandidates }));
          }
        }
        return null;
      };
      extracted = tryExtract();
      if (!extracted) {
        const nonce = `${Date.now()}-${Math.random().toString(36).slice(2)}`;
        activeProbeNonce = nonce;
        const probeResult = await page.evaluate(signedProbeScript(), { path: this.probePath, method: this.probeMethod, nonce });
        probeSummary = probeResult?.statsigID ? "signer-returned-id" : `no-id status=${probeResult?.fetchStatus ?? "null"}${probeResult?.fetchError ? ` fetch=${probeResult.fetchError}` : ""}`;
        const observedProbe = await waitWithTimeout(probeCapture.promise, Math.min(this.browserTimeoutMs, 10_000));
        if (observedProbe?.statsigID || probeResult?.statsigID) {
          const capture = observedProbe ?? { statsigID: probeResult.statsigID, method: this.probeMethod, path: new URL(this.probePath, this.targetURL).pathname };
          captured = await page.evaluate(() => structuredClone(globalThis.__seedHexCatch));
          hexCandidates = captured.styles.flatMap((style) => {
            try { return [computeStyleHEX(style.color, style.transform)]; } catch { return []; }
          });
          try {
            extracted = extractMaterialFromCapture({ ...capture, digestInputs: [...captured.digestInputs, ...digestInputs], hexCandidates });
          } catch (_) {
            mismatches.add(describeCaptureMismatch({ ...capture, digestInputs: [...captured.digestInputs, ...digestInputs], hexCandidates }));
          }
        } else if (probeResult?.error) {
          mismatches.add(`probe failed: ${probeResult.error}`);
        }
        const deadline = Date.now() + Math.min(this.browserTimeoutMs, 10_000);
        while (!extracted && Date.now() < deadline) {
          captured = await page.evaluate(() => structuredClone(globalThis.__seedHexCatch));
          extracted = tryExtract();
          if (!extracted) await page.waitForTimeout(100);
        }
      }
      if (!extracted) {
        const status = response?.status();
        const title = await page.title().catch(() => "");
        const detail = [status ? `initial HTTP ${status}` : "", title ? `title ${JSON.stringify(title)}` : ""]
          .filter(Boolean)
          .join(", ");
        const mismatchDetail = mismatches.size ? `; ${[...mismatches].slice(0, 4).join("; ")}` : "";
        throw new Error(`browser did not produce a matching x-statsig-id request${detail ? ` (${detail})` : ""}${mismatchDetail}; probe=${probeSummary}; requests=${requestSummary.join(", ") || "none"}`);
      }
      const { seed, hex } = extracted;
      validateMaterial(seed, hex);
      const paths = Array.isArray(captured.paths) ? captured.paths.filter(Boolean) : [];
      const selectedPath = captured.selected?.path ?? "";
      const pathMaterial = paths.length ? paths : selectedPath ? [selectedPath] : [];

      const nextMaterial = Object.freeze({
        seed,
        hex,
        digestLength: extracted.digestLength,
        hasMarker: extracted.hasMarker,
        prefix: extracted.prefix,
        refreshedAt: new Date().toISOString(),
        pathVersion: createHash("sha256").update(pathMaterial.join("\n")).digest("hex"),
        pathCount: pathMaterial.length,
        capturedMethod: extracted.capturedMethod,
        capturedPath: extracted.capturedPath,
      });
      // Publish the validated seed/HEX pair with one reference replacement.
      this.material = nextMaterial;
      this.state.lastError = null;
      return nextMaterial;
    } catch (error) {
      this.state.lastError = error instanceof Error ? error.message : String(error);
      return null;
    } finally {
      this.state.refreshInFlight = false;
      await context?.close().catch(() => {});
      await browser?.close().catch(() => {});
    }
  }
}
