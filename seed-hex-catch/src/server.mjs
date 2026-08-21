import { createServer } from "./app.mjs";
import { SVGMaterialCollector } from "./collector.mjs";

const host = process.env.CATCH_HOST ?? "0.0.0.0";
const port = Number(process.env.CATCH_PORT ?? 8789);
const apiToken = String(process.env.CATCH_API_TOKEN ?? "").trim();
const refreshIntervalMs = Number(process.env.CATCH_REFRESH_INTERVAL_MS ?? 600_000);
const retryIntervalMs = Number(process.env.CATCH_RETRY_INTERVAL_MS ?? 15_000);
const maxBodyBytes = Number(process.env.CATCH_MAX_BODY_BYTES ?? 64 * 1024);
const collector = new SVGMaterialCollector();
const server = createServer({ collector, apiToken, maxBodyBytes });
let timer;

async function refreshAndSchedule() {
  const material = await collector.refresh();
  const status = collector.status();
  if (material) {
    console.log(`seed-hex-catch capture ready: path=${material.capturedMethod} ${material.capturedPath}, svgPaths=${material.pathCount}`);
  } else {
    console.error(`seed-hex-catch capture failed: ${status.lastError ?? "unknown error"}`);
  }
  const delay = material ? refreshIntervalMs : retryIntervalMs;
  timer = setTimeout(() => void refreshAndSchedule(), delay);
  timer.unref();
}

server.listen(port, host, () => {
  console.log(`seed-hex-catch listening on ${host}:${port}`);
  void refreshAndSchedule();
});

function shutdown(signal) {
  console.log(`${signal}: shutting down`);
  if (timer) clearTimeout(timer);
  server.close(() => process.exit(0));
}

process.once("SIGINT", () => shutdown("SIGINT"));
process.once("SIGTERM", () => shutdown("SIGTERM"));
