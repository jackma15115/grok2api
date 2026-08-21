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
    const rememberDigestInput = (value) => {
      if (typeof value === 'string' && value.length <= 4096 && value.includes(salt)) state.digestInputs.push(value);
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
        if (element?.tagName === 'DIV' && element.childElementCount === 0 && element.parentElement === document.body) {
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
      await context.addCookies(clearance.cookies);
      const page = await context.newPage();
      await page.addInitScript({ content: materialCaptureScript() });
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
              probeCapture.resolve({
                statsigID: headers["x-statsig-id"],
                method: request.method(),
                path: new URL(request.url()).pathname,
              });
            }
            await route.abort();
            return;
          }
        } catch (_) {}
        await route.continue();
      });
      const response = await page.goto(this.targetURL, { waitUntil: "domcontentloaded", timeout: this.browserTimeoutMs });
      await page.waitForTimeout(this.pageSettleMs);
      let captured = await page.evaluate(() => structuredClone(globalThis.__seedHexCatch));
      let hexCandidates = captured.styles.flatMap((style) => {
        try { return [computeStyleHEX(style.color, style.transform)]; } catch { return []; }
      });
      let extracted = null;
      const mismatches = [];
      for (const capture of observed.toReversed()) {
        try {
          extracted = extractMaterialFromCapture({ ...capture, digestInputs: captured.digestInputs, hexCandidates });
          break;
        } catch (_) {
          mismatches.push(describeCaptureMismatch({ ...capture, digestInputs: captured.digestInputs, hexCandidates }));
        }
      }
      if (!extracted) {
        const nonce = `${Date.now()}-${Math.random().toString(36).slice(2)}`;
        const probe = page.evaluate(async ({ path, method, nonce }) => {
          const init = {
            method,
            credentials: "include",
            cache: "no-store",
            headers: { "x-grok2api-statsig-probe": nonce },
          };
          if (!["GET", "HEAD"].includes(method)) init.body = "{}";
          try { await fetch(path, init); } catch (_) {}
        }, { path: this.probePath, method: this.probeMethod, nonce });
        const capture = await waitWithTimeout(probeCapture.promise, this.browserTimeoutMs);
        await probe;
        captured = await page.evaluate(() => structuredClone(globalThis.__seedHexCatch));
        hexCandidates = captured.styles.flatMap((style) => {
          try { return [computeStyleHEX(style.color, style.transform)]; } catch { return []; }
        });
        if (capture) extracted = extractMaterialFromCapture({ ...capture, digestInputs: captured.digestInputs, hexCandidates });
      }
      if (!extracted) {
        const status = response?.status();
        const title = await page.title().catch(() => "");
        const detail = [status ? `initial HTTP ${status}` : "", title ? `title ${JSON.stringify(title)}` : ""]
          .filter(Boolean)
          .join(", ");
        const mismatchDetail = mismatches.length ? `; ${mismatches.slice(0, 4).join("; ")}` : "";
        throw new Error(`browser did not produce a matching x-statsig-id request${detail ? ` (${detail})` : ""}${mismatchDetail}`);
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
