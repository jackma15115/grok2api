import { createHash, randomBytes } from "node:crypto";

export const STATSIG_EPOCH = 1682924400;
export const STATSIG_SALT = "obfiowerehiring";
export const STATSIG_MARK = 0x03;

function decodeBase64(value) {
  if (typeof value !== "string") {
    throw new Error("value must be a base64 string");
  }
  const compact = value.trim();
  if (!compact || !/^[A-Za-z0-9+/_-]+={0,2}$/.test(compact)) {
    throw new Error("value is not valid base64");
  }
  const normalized = compact.replace(/-/g, "+").replace(/_/g, "/");
  const decoded = Buffer.from(normalized, "base64");
  if (decoded.length === 0) {
    throw new Error("value decodes to an empty buffer");
  }
  return decoded;
}

export function encodeBase64Raw(value) {
  return Buffer.from(value).toString("base64").replace(/=+$/, "");
}

export function decodeStatsigID(value) {
  const decoded = decodeBase64(value);
  if (decoded.length !== 70) {
    throw new Error(`x-statsig-id must decode to 70 bytes, got ${decoded.length}`);
  }
  const key = decoded[0];
  const seed = Buffer.alloc(48);
  for (let index = 0; index < seed.length; index += 1) {
    seed[index] = decoded[index + 1] ^ key;
  }
  const number =
    (decoded[49] ^ key)
    | ((decoded[50] ^ key) << 8)
    | ((decoded[51] ^ key) << 16)
    | ((decoded[52] ^ key) << 24);
  const digestPrefix = Buffer.alloc(16);
  for (let index = 0; index < digestPrefix.length; index += 1) {
    digestPrefix[index] = decoded[index + 53] ^ key;
  }
  if ((decoded[69] ^ key) !== STATSIG_MARK) {
    throw new Error("x-statsig-id has an invalid marker");
  }
  return { decoded, key, seed, number: number >>> 0, digestPrefix };
}

export function normalizePath(value) {
  if (typeof value !== "string") {
    throw new Error("path must be a string");
  }
  const path = value.trim();
  if (!path || !path.startsWith("/") || path.length > 2048 || path.includes("\0")) {
    throw new Error("path must be an absolute request pathname");
  }
  return path;
}

export function normalizeMethod(value) {
  if (typeof value !== "string") {
    throw new Error("method must be a string");
  }
  const method = value.trim().toUpperCase();
  if (!/^[A-Z][A-Z0-9-]{0,15}$/.test(method)) {
    throw new Error("method is invalid");
  }
  return method;
}

function digestInput(method, path, number, hex) {
  return `${method}!${path}!${number}${STATSIG_SALT}${hex}`;
}

export function extractMaterialFromCapture({ statsigID, method, path, digestInputs }) {
  const decoded = decodeStatsigID(statsigID);
  const normalizedMethod = normalizeMethod(method);
  const normalizedPath = normalizePath(path);
  if (!Array.isArray(digestInputs)) {
    throw new Error("digestInputs must be an array");
  }
  const expectedPrefix = `${normalizedMethod}!${normalizedPath}!${decoded.number}${STATSIG_SALT}`;
  const input = digestInputs.find((candidate) => typeof candidate === "string" && candidate.startsWith(expectedPrefix));
  if (!input) {
    throw new Error("browser did not expose the matching Statsig digest input");
  }
  const hex = input.slice(expectedPrefix.length);
  if (!hex || !/^[0-9a-f]+$/i.test(hex)) {
    throw new Error("browser Statsig digest contains an invalid HEX fingerprint");
  }
  const expectedDigest = createHash("sha256").update(input).digest().subarray(0, 16);
  if (!expectedDigest.equals(decoded.digestPrefix)) {
    throw new Error("browser x-statsig-id does not match its captured SHA input");
  }
  return {
    seed: encodeBase64Raw(decoded.seed),
    seedBytes: decoded.seed,
    hex: hex.toLowerCase(),
    number: decoded.number,
    capturedStatsigID: statsigID,
    capturedMethod: normalizedMethod,
    capturedPath: normalizedPath,
    capturedAt: new Date().toISOString(),
  };
}

export function materialFromConfig(seedValue, hexValue) {
  const seed = decodeBase64(seedValue);
  if (seed.length !== 48) {
    throw new Error(`seed must decode to 48 bytes, got ${seed.length}`);
  }
  const hex = String(hexValue ?? "").trim().toLowerCase();
  if (!hex || !/^[0-9a-f]+$/.test(hex)) {
    throw new Error("hex must be a non-empty hexadecimal string");
  }
  return { seed: encodeBase64Raw(seed), seedBytes: seed, hex, capturedAt: new Date().toISOString() };
}

export function buildStatsig(material, method, path, nowSeconds = Math.floor(Date.now() / 1000), key = randomBytes(1)[0]) {
  const normalizedMethod = normalizeMethod(method);
  const normalizedPath = normalizePath(path);
  if (!material?.seedBytes || material.seedBytes.length !== 48 || !material.hex) {
    throw new Error("Statsig material is incomplete");
  }
  const number = (Math.floor(nowSeconds) - STATSIG_EPOCH) >>> 0;
  const input = digestInput(normalizedMethod, normalizedPath, number, material.hex);
  const digest = createHash("sha256").update(input).digest();
  const output = Buffer.alloc(70);
  output[0] = key;
  for (let index = 0; index < 48; index += 1) {
    output[index + 1] = material.seedBytes[index] ^ key;
  }
  output[49] = number ^ key;
  output[50] = (number >>> 8) ^ key;
  output[51] = (number >>> 16) ^ key;
  output[52] = (number >>> 24) ^ key;
  for (let index = 0; index < 16; index += 1) {
    output[index + 53] = digest[index] ^ key;
  }
  output[69] = STATSIG_MARK ^ key;
  return encodeBase64Raw(output);
}

export function publicMaterialStatus(material, state = {}) {
  return {
    ready: Boolean(material),
    source: state.source ?? (material ? "browser" : null),
    capturedAt: material?.capturedAt ?? null,
    capturedMethod: material?.capturedMethod ?? null,
    capturedPath: material?.capturedPath ?? null,
    clearanceSource: state.clearanceSource ?? null,
    refreshInFlight: Boolean(state.refreshInFlight),
    lastError: state.lastError ?? null,
  };
}
