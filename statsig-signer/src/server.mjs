import { createSignerServer } from "./app.mjs";
import { BrowserCalibrator } from "./calibrator.mjs";

const host = process.env.SIGNER_HOST ?? "0.0.0.0";
const port = Number(process.env.SIGNER_PORT ?? 8787);
const apiToken = String(process.env.SIGNER_API_TOKEN ?? "").trim();
const refreshIntervalMs = Number(process.env.SIGNER_REFRESH_INTERVAL_MS ?? 30 * 60 * 1000);
const maxBodyBytes = Number(process.env.SIGNER_MAX_BODY_BYTES ?? 64 * 1024);
const calibrator = new BrowserCalibrator();
const server = createSignerServer({ calibrator, apiToken, maxBodyBytes });

server.listen(port, host, () => {
  console.log(`statsig signer listening on ${host}:${port}`);
  void calibrator.start();
});

if (Number.isFinite(refreshIntervalMs) && refreshIntervalMs >= 60_000) {
  const timer = setInterval(() => void calibrator.refresh(), refreshIntervalMs);
  timer.unref();
}

async function shutdown(signal) {
  console.log(`${signal}: shutting down`);
  server.close(() => process.exit(0));
}
process.once("SIGINT", () => void shutdown("SIGINT"));
process.once("SIGTERM", () => void shutdown("SIGTERM"));
