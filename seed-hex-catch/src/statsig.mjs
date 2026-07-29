import { createHash, randomBytes } from "node:crypto";

import { decodeSeed, validateMaterial } from "./hex.mjs";

export const STATSIG_EPOCH = 1682924400;
export const STATSIG_SALT = "obfiowerehiring";
export const STATSIG_MARK = 0x03;

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
  const seed = decodeSeed(material.seed);
  const normalizedMethod = normalizeMethod(method);
  const normalizedPath = normalizePath(path);
  const number = (Math.floor(nowSeconds) - STATSIG_EPOCH) >>> 0;
  const input = `${normalizedMethod}!${normalizedPath}!${number}${STATSIG_SALT}${material.hex}`;
  const digest = createHash("sha256").update(input).digest();
  const output = Buffer.alloc(70);

  output[0] = key;
  for (let index = 0; index < seed.length; index += 1) output[index + 1] = seed[index] ^ key;
  output[49] = number ^ key;
  output[50] = (number >>> 8) ^ key;
  output[51] = (number >>> 16) ^ key;
  output[52] = (number >>> 24) ^ key;
  for (let index = 0; index < 16; index += 1) output[index + 53] = digest[index] ^ key;
  output[69] = STATSIG_MARK ^ key;
  return output.toString("base64").replace(/=+$/, "");
}
