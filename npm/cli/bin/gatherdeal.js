#!/usr/bin/env node
// Launcher for the gatherdeal binary shipped in the platform package that
// npm installed as an optional dependency. It has no install scripts and
// runs no code besides starting the binary.
"use strict";

const { spawnSync } = require("node:child_process");

const platform = `${process.platform}-${process.arch}`;
const pkg = `@gatherdeal/cli-${platform}`;
const exe = process.platform === "win32" ? "gatherdeal.exe" : "gatherdeal";

let binary;
try {
  binary = require.resolve(`${pkg}/bin/${exe}`);
} catch {
  process.stderr.write(
    `gatherdeal: no binary for ${platform}. The package ${pkg} was not installed;\n` +
      "reinstall without --omit=optional, or download a release from https://github.com/gatherdeal/cli/releases\n",
  );
  process.exit(1);
}

// GATHERDEAL_LAUNCHER tells `gatherdeal update` that npm installed this
// binary; it only confirms what the binary's path already shows.
const result = spawnSync(binary, process.argv.slice(2), {
  stdio: "inherit",
  windowsHide: true,
  env: { ...process.env, GATHERDEAL_LAUNCHER: "npm" },
});
if (result.error) {
  process.stderr.write(`gatherdeal: cannot start ${binary}: ${result.error.message}\n`);
  process.exit(1);
}
if (result.signal) {
  process.kill(process.pid, result.signal);
}
process.exit(result.status ?? 1);
