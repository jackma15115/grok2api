import { chromium } from "playwright";

export async function launchCaptureBrowser(options, launch = (value) => chromium.launch(value)) {
  try {
    return await launch(options);
  } catch (error) {
    if (!/\bsignal=SIGSEGV\b/.test(String(error?.message))) throw error;
    // A failed launch is cleaned up by Playwright; reuse the solved clearance.
    console.warn("seed-hex-catch Chromium exited with SIGSEGV during launch; retrying once");
    return launch(options);
  }
}
