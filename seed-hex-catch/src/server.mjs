import { createServer } from "./app.mjs";
import { SVGMaterialCollector } from "./collector.mjs";

const host = process.env.CATCH_HOST ?? "0.0.0.0";
const port = Number(process.env.CATCH_PORT ?? 8789);
const apiToken = String(process.env.CATCH_API_TOKEN ?? "").trim();
const refreshIntervalMs = Number(process.env.CATCH_REFRESH_INTERVAL_MS ?? 600_000);
const retryIntervalMs = Number(process.env.CATCH_RETRY_INTERVAL_MS ?? 15_000);
const collector = new SVGMaterialCollector({ refreshIntervalMs });
const server = createServer({ collector, apiToken });
let timer;

async function refreshAndSchedule() {
  const material = await collector.refresh();
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
