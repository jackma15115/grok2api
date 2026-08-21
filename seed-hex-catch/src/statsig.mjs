import { createHash, randomBytes } from "node:crypto";

import { decodeSeed, validateMaterial } from "./hex.mjs";

export const STATSIG_EPOCH = 1682924400;
export const STATSIG_SALT = "obfiowerehiring";
export const STATSIG_MARK = 0x03;

function decodeBase64(value) {
  const compact = String(value ?? "").trim();
  if (!compact || !/^[A-Za-z0-9+/_-]+={0,2}$/.test(compact)) throw new Error("value is not valid base64");
  const decoded = Buffer.from(compact.replace(/-/g, "+").replace(/_/g, "/"), "base64");
  if (!decoded.length) throw new Error("value decodes to an empty buffer");
  return decoded;
}

function encodeBase64Raw(value) {
  return Buffer.from(value).toString("base64").replace(/=+$/g, "");
}

export function decodeStatsigID(value) {
  const decoded = decodeBase64(value);
  let payloadOffset = -1;
  let digestLength = 0;
  for (let offset = 0; offset <= Math.min(8, decoded.length - 70); offset += 1) {
    const candidateDigestLength = decoded.length - offset - 54;
    if (candidateDigestLength >= 16 && candidateDigestLength <= 32 && (decoded[decoded.length - 1] ^ decoded[offset]) === STATSIG_MARK) {
      payloadOffset = offset;
      digestLength = candidateDigestLength;
      break;
    }
  }
  const hasMarker = payloadOffset >= 0;
  if (!hasMarker) {
    payloadOffset = 0;
    digestLength = decoded.length - 53;
  }
  if (digestLength < 16 || digestLength > 32) {
    throw new Error(`x-statsig-id must contain a 16-32 byte digest prefix, got ${digestLength}`);
  }
  const key = decoded[payloadOffset];
  const seed = Buffer.alloc(48);
  for (let index = 0; index < seed.length; index += 1) seed[index] = decoded[payloadOffset + index + 1] ^ key;
  const number = (decoded[payloadOffset + 49] ^ key)
    | ((decoded[payloadOffset + 50] ^ key) << 8)
    | ((decoded[payloadOffset + 51] ^ key) << 16)
    | ((decoded[payloadOffset + 52] ^ key) << 24);
  const digestPrefix = Buffer.alloc(digestLength);
  for (let index = 0; index < digestPrefix.length; index += 1) digestPrefix[index] = decoded[payloadOffset + index + 53] ^ key;
  return {
    seed,
    number: number >>> 0,
    digestPrefix,
    digestLength,
    hasMarker,
    prefix: encodeBase64Raw(decoded.subarray(0, payloadOffset)),
  };
}

export function extractMaterialFromCapture({ statsigID, method, path, digestInputs, hexCandidates = [] }) {
  const decoded = decodeStatsigID(statsigID);
  const normalizedMethod = normalizeMethod(method);
  const normalizedPath = normalizePath(path);
  const expectedPrefix = `${normalizedMethod}!${normalizedPath}!${decoded.number}${STATSIG_SALT}`;
  let input = Array.isArray(digestInputs)
    ? digestInputs.find((candidate) => typeof candidate === "string" && candidate.startsWith(expectedPrefix))
    : null;
  if (!input && Array.isArray(hexCandidates)) {
    input = hexCandidates
      .filter((candidate) => typeof candidate === "string" && /^[0-9a-f]+$/i.test(candidate))
      .map((candidate) => `${expectedPrefix}${candidate}`)
      .find((candidate) => createHash("sha256").update(candidate).digest().subarray(0, decoded.digestLength).equals(decoded.digestPrefix));
  }
  if (!input) throw new Error("browser did not expose the matching Statsig digest input");
  const hex = input.slice(expectedPrefix.length);
  if (!hex || !/^[0-9a-f]+$/i.test(hex)) throw new Error("browser Statsig digest contains an invalid HEX fingerprint");
  const expectedDigest = createHash("sha256").update(input).digest().subarray(0, decoded.digestLength);
  if (!expectedDigest.equals(decoded.digestPrefix)) throw new Error("browser x-statsig-id does not match its captured SHA input");
  return {
    seed: encodeBase64Raw(decoded.seed),
    hex: hex.toLowerCase(),
    digestLength: decoded.digestLength,
    hasMarker: decoded.hasMarker,
    prefix: decoded.prefix,
    capturedMethod: normalizedMethod,
    capturedPath: normalizedPath,
  };
}

export function describeCaptureMismatch({ statsigID, method, path, digestInputs, hexCandidates = [] }) {
  let decoded;
  try {
    decoded = decodeStatsigID(statsigID);
  } catch (error) {
    return `request=${String(method)} ${String(path)}, decode=${error instanceof Error ? error.message : String(error)}`;
  }
  const candidates = Array.isArray(digestInputs) ? digestInputs.flatMap((candidate) => {
    if (typeof candidate !== "string") return [];
    const saltIndex = candidate.indexOf(STATSIG_SALT);
    if (saltIndex < 0) return [];
    return [`${candidate.slice(0, saltIndex)}<salt>hexLen=${candidate.length - saltIndex - STATSIG_SALT.length}`];
  }) : [];
  return `request=${String(method)} ${String(path)}, number=${decoded.number}, digestLength=${decoded.digestLength}, hasMarker=${decoded.hasMarker}, digestInputs=${candidates.join("|") || "none"}, hexCandidates=${Array.isArray(hexCandidates) ? hexCandidates.length : 0}`;
}

export function normalizeMethod(value) {
  if (typeof value !== "string") throw new Error("method must be a string");
  const method = value.trim().toUpperCase();
  if (!/^[A-Z][A-Z0-9-]{0,15}$/.test(method)) throw new Error("method is invalid");
  return method;
}

export function normalizePath(value) {
  if (typeof value !== "string") throw new Error("path must be a string");
  const path = value.trim();
  if (!path || !path.startsWith("/") || path.length > 2048 || path.includes("\0")) {
    throw new Error("path must be an absolute request pathname");
  }
  return path;
}

export function buildStatsig(material, method, path, nowSeconds = Math.floor(Date.now() / 1000), key = randomBytes(1)[0]) {
  validateMaterial(material?.seed, material?.hex);
  const digestLength = material?.digestLength ?? 16;
  const hasMarker = material?.hasMarker ?? true;
  if (!Number.isInteger(digestLength) || digestLength < 16 || digestLength > 32) {
    throw new Error("Statsig digest length must be an integer from 16 to 32");
  }
  const seed = decodeSeed(material.seed);
  const prefix = material?.prefix ? decodeBase64(material.prefix) : Buffer.alloc(0);
  if (prefix.length > 8) throw new Error("Statsig prefix is too large");
  const normalizedMethod = normalizeMethod(method);
  const normalizedPath = normalizePath(path);
  const number = (Math.floor(nowSeconds) - STATSIG_EPOCH) >>> 0;
  const input = `${normalizedMethod}!${normalizedPath}!${number}${STATSIG_SALT}${material.hex}`;
  const digest = createHash("sha256").update(input).digest();
  const output = Buffer.alloc(prefix.length + 53 + digestLength + (hasMarker ? 1 : 0));
  prefix.copy(output);
  const offset = prefix.length;

  output[offset] = key;
  for (let index = 0; index < seed.length; index += 1) output[offset + index + 1] = seed[index] ^ key;
  output[offset + 49] = number ^ key;
  output[offset + 50] = (number >>> 8) ^ key;
  output[offset + 51] = (number >>> 16) ^ key;
  output[offset + 52] = (number >>> 24) ^ key;
  for (let index = 0; index < digestLength; index += 1) output[offset + index + 53] = digest[index] ^ key;
  if (hasMarker) output[output.length - 1] = STATSIG_MARK ^ key;
  return output.toString("base64").replace(/=+$/, "");
}
