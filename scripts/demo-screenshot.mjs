// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors
//
// Captures a first-frame PNG of the static demo bundle as a NON-gating CI
// artifact. Dependency-free: uses only Node built-ins plus BUILD-TIME npx
// invocations (never a package.json dependency).
//
// Engine note (load-bearing): the settle flag `--virtual-time-budget` is
// honored by `chrome-headless-shell` (OLD headless) but is UNRELIABLE on
// `google-chrome-stable --headless` (NEW headless), and `--headless=old` was
// removed from modern Chrome. So we run the SAME engine the recipe was proven
// on: $CHROME_BIN if set, else `chrome-headless-shell@stable` fetched via
// `@puppeteer/browsers`.
//
// MIME note (load-bearing): index.html loads
// `<script type="module" crossorigin src="./assets/index-*.js">`; Chrome's
// strict-MIME rule refuses to execute a module script served with a non-JS
// Content-Type (=> blank page), and file:// won't execute a module at all. So
// we serve web/demo-dist over http with correct Content-Type per extension.
//
// Deferred (needs a CDP dependency, out of scope this slice): a dark-theme
// capture. The app follows OS prefers-color-scheme with no toggle, and Chrome
// has no clean CLI flag to emulate dark without double-darkening the
// already-theme-aware app, so a real dark shot needs CDP.

import http from "node:http";
import { createReadStream, existsSync, statSync, mkdirSync } from "node:fs";
import { join, extname, resolve, normalize } from "node:path";
import { tmpdir } from "node:os";
import { spawn, execFileSync } from "node:child_process";

const DIST = resolve("web/demo-dist");
const ARTIFACTS = "demo-artifacts";
const OUT = join(ARTIFACTS, "first-frame.png");
const MIN_PLAUSIBLE_BYTES = 20 * 1024; // blank-frame guard

const MIME = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".woff2": "font/woff2",
  ".json": "application/json; charset=utf-8",
};

function warnExit(msg) {
  console.log(`::warning::${msg}`);
  process.exit(1);
}

if (!existsSync(join(DIST, "index.html"))) {
  warnExit(`web/demo-dist/index.html missing — run 'make demo-build' first.`);
}
mkdirSync(ARTIFACTS, { recursive: true });

// ---- resolve the browser binary --------------------------------------------
function resolveBrowser() {
  if (process.env.CHROME_BIN) {
    console.log(`Using CHROME_BIN=${process.env.CHROME_BIN}`);
    return process.env.CHROME_BIN;
  }
  const cacheDir = join(tmpdir(), "costroid-demo-chrome");
  mkdirSync(cacheDir, { recursive: true });
  console.log("CHROME_BIN unset — installing chrome-headless-shell@stable via @puppeteer/browsers ...");
  const out = execFileSync(
    "npx",
    ["--yes", "@puppeteer/browsers", "install", "chrome-headless-shell@stable", "--path", cacheDir],
    // 3 min: @puppeteer/browsers' downloader sets no request/response timeout
    // and only listens for 'error' (which does NOT fire on a silent mid-transfer
    // stall), so without this the install could block forever. On timeout
    // execFileSync THROWS (ETIMEDOUT) -> the resolveBrowser() try/catch turns it
    // into warnExit() -> ::warning:: + non-zero exit, which the workflow step's
    // continue-on-error tolerates. 3 min is generous for a tens-of-MB download
    // and sits under the 5-min step timeout, so the script self-fails first.
    { encoding: "utf8", stdio: ["ignore", "pipe", "inherit"], timeout: 180000 },
  ).trim();
  // Output form: "chrome-headless-shell@<version> <absolute-binary-path>"
  const bin = out.split(/\s+/).pop();
  if (!bin || !existsSync(bin)) {
    warnExit(`@puppeteer/browsers did not yield a usable binary (parsed: ${bin})`);
  }
  console.log(`Installed browser: ${bin}`);
  return bin;
}

let browserBin;
try {
  browserBin = resolveBrowser();
} catch (err) {
  warnExit(`Could not resolve a browser binary: ${err.message}`);
}

// ---- static server (correct Content-Type per extension) ---------------------
const server = http.createServer((req, res) => {
  try {
    const url = new URL(req.url, "http://127.0.0.1");
    let rel = decodeURIComponent(url.pathname);
    if (rel === "/" || rel === "") rel = "/index.html";
    const filePath = normalize(join(DIST, rel));
    if (!filePath.startsWith(DIST)) {
      res.writeHead(403);
      res.end("forbidden");
      return;
    }
    if (!existsSync(filePath) || !statSync(filePath).isFile()) {
      res.writeHead(404);
      res.end("not found");
      return;
    }
    res.writeHead(200, { "Content-Type": MIME[extname(filePath).toLowerCase()] || "application/octet-stream" });
    createReadStream(filePath).pipe(res);
  } catch (err) {
    res.writeHead(500);
    res.end(String(err));
  }
});

function finish(code) {
  server.close(() => process.exit(code));
  // Fallback so the process never hangs on a stuck socket close.
  setTimeout(() => process.exit(code), 2000).unref();
}

server.listen(0, "127.0.0.1", () => {
  const port = server.address().port;
  const targetUrl = `http://127.0.0.1:${port}/`;
  console.log(`Serving ${DIST} at ${targetUrl}`);

  const args = [
    "--headless",
    "--no-sandbox",
    "--disable-gpu",
    "--disable-dev-shm-usage",
    "--hide-scrollbars",
    "--force-color-profile=srgb",
    "--window-size=1280,900",
    "--virtual-time-budget=4000",
    `--screenshot=${OUT}`,
    targetUrl,
  ];
  console.log(`Launching: ${browserBin} ${args.join(" ")}`);

  const child = spawn(browserBin, args, { stdio: "inherit" });

  const killTimer = setTimeout(() => {
    console.log("::warning::Browser did not exit within 60s — killing.");
    child.kill("SIGKILL");
  }, 60_000);

  child.on("error", (err) => {
    clearTimeout(killTimer);
    console.log(`::warning::Browser launch failed: ${err.message}`);
    finish(1);
  });

  child.on("exit", (code) => {
    clearTimeout(killTimer);
    if (!existsSync(OUT)) {
      console.log("::warning::No screenshot produced.");
      finish(1);
      return;
    }
    const size = statSync(OUT).size;
    if (size < MIN_PLAUSIBLE_BYTES) {
      console.log(`::warning::Screenshot implausibly small (${size} B < ${MIN_PLAUSIBLE_BYTES} B) — likely a blank frame.`);
      finish(1);
      return;
    }
    console.log(`Captured ${OUT} (${size} bytes, browser exit ${code}).`);
    finish(0);
  });
});
