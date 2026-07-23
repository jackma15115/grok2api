function decodeSeed(value) {
  const normalized = String(value ?? "").trim().replace(/-/g, "+").replace(/_/g, "/");
  if (!normalized || !/^[A-Za-z0-9+/]+={0,2}$/.test(normalized)) throw new Error("seed is not valid base64");
  const seed = Buffer.from(normalized, "base64");
  if (seed.length !== 48) throw new Error(`seed must decode to 48 bytes, got ${seed.length}`);
  return seed;
}

function parseColor(value) {
  const match = String(value ?? "").match(/^rgba?\(\s*(\d+)\s*[, ]\s*(\d+)\s*[, ]\s*(\d+)/i);
  if (!match) throw new Error("browser returned an invalid computed color");
  return match.slice(1, 4).map(Number);
}

function parseMatrix(value) {
  const match = String(value ?? "").match(/^matrix\(\s*([^)]*)\)$/i);
  if (!match) throw new Error("browser returned an invalid computed transform");
  const values = match[1].split(",").map((item) => Number(item.trim()));
  if (values.length !== 6 || values.some((item) => !Number.isFinite(item))) {
    throw new Error("browser returned an invalid computed transform");
  }
  return values;
}

function encodeHEX(values) {
  return values
    .map((value) => Number(value.toFixed(2)).toString(16))
    .join("")
    .replace(/[.-]/g, "");
}

export function computeStyleHEX(color, transform) {
  return encodeHEX([...parseColor(color), ...parseMatrix(transform)]);
}

export function validateMaterial(seed, hex) {
  decodeSeed(seed);
  if (typeof hex !== "string" || !/^[0-9a-f]{8,256}$/.test(hex)) throw new Error("hex is invalid");
}
