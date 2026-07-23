import { createHash } from "node:crypto";
import { chromium } from "playwright";

import { solveFlareSolverr } from "./flaresolverr.mjs";
import { computeStyleHEX, validateMaterial } from "./hex.mjs";

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
    globalThis.__seedHexCatch = { outputs: [], paths: [], selected: null, styles: [] };
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

    const originalBtoa = globalThis.btoa.bind(globalThis);
    globalThis.btoa = function (value) {
      try {
        if (typeof value === 'string' && value.length === 70) {
          const bytes = Array.from(value, (char) => char.charCodeAt(0));
          if ((bytes[69] ^ bytes[0]) === 3) {
            const key = bytes[0];
            let binary = '';
            for (let index = 1; index <= 48; index += 1) binary += String.fromCharCode(bytes[index] ^ key);
            state.outputs.push({ seed: originalBtoa(binary).replace(/=+$/g, ''), styleIndex: state.styles.length - 1 });
          }
        }
      } catch (_) {}
      return originalBtoa(value);
    };

    const originalComputedStyle = globalThis.getComputedStyle.bind(globalThis);
    globalThis.getComputedStyle = function (element, pseudoElement) {
      const style = originalComputedStyle(element, pseudoElement);
      if (element?.tagName === 'DIV' && element.childElementCount === 0 && element.parentElement === document.body) {
        const animation = element.getAnimations().find((item) => item.effect?.getComputedTiming()?.duration === 4096);
        if (animation) {
          state.styles.push({
            color: style.color,
            transform: style.transform,
            selected: state.selected ? { ...state.selected } : null,
            paths: state.paths.slice(),
          });
        }
      }
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
    this.executablePath = options.executablePath ?? process.env.CATCH_BROWSER_EXECUTABLE_PATH ?? "";
    this.proxyURL = options.proxyURL ?? process.env.CATCH_PROXY_URL ?? "";
    this.headless = options.headless ?? process.env.CATCH_HEADLESS !== "false";
    this.refreshIntervalMs = options.refreshIntervalMs ?? Number(process.env.CATCH_REFRESH_INTERVAL_MS ?? 600_000);
    this.material = null;
    this.refreshPromise = null;
    this.state = { refreshInFlight: false, lastError: null, lastAttemptAt: null };
  }

  status() {
    const now = Date.now();
    return {
      ...this.state,
      ready: Boolean(this.material && Date.parse(this.material.expiresAt) > now),
      material: this.material,
    };
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
      await page.goto(this.targetURL, { waitUntil: "domcontentloaded", timeout: this.browserTimeoutMs });
      await page.waitForFunction(() => globalThis.__seedHexCatch?.outputs?.length > 0 && globalThis.__seedHexCatch?.styles?.length > 0, null, {
        timeout: this.browserTimeoutMs,
      });
      await page.waitForTimeout(this.pageSettleMs);
      const captured = await page.evaluate(() => structuredClone(globalThis.__seedHexCatch));
      const output = captured.outputs.at(-1);
      const style = captured.styles[output?.styleIndex] ?? captured.styles.at(-1);
      if (!output?.seed || !style?.color || !style?.transform) throw new Error("natural Statsig material was not observed");

      const seed = output.seed;
      const hex = computeStyleHEX(style.color, style.transform);
      validateMaterial(seed, hex);
      const selectedPath = style.selected?.path ?? "";
      const completePaths = style.paths.length === 4 && style.paths.every((path) => typeof path === "string" && path.startsWith("M 10,30 C "));
      const pathMaterial = completePaths ? style.paths : selectedPath ? [selectedPath] : [];
      if (!pathMaterial.length) throw new Error("natural Statsig SVG path was not observed");

      const refreshedAt = new Date();
      const expiresAt = new Date(refreshedAt.getTime() + Math.max(this.refreshIntervalMs + 120_000, 180_000));
      const nextMaterial = Object.freeze({
        seed,
        hex,
        refreshedAt: refreshedAt.toISOString(),
        expiresAt: expiresAt.toISOString(),
        pathVersion: createHash("sha256").update(pathMaterial.join("\n")).digest("hex"),
        pathCount: pathMaterial.length,
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
