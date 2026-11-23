#!/usr/bin/env node
// Runs the binary of the platform package npm installed as an optional dependency.
const { spawnSync } = require("node:child_process");

const pkg = `postgres-dba-mcp-${process.platform}-${process.arch}`;
let bin;
try {
  bin = require.resolve(`${pkg}/bin/postgres-dba-mcp${process.platform === "win32" ? ".exe" : ""}`);
} catch {
  console.error(
    `postgres-dba-mcp: no binary for ${process.platform} ${process.arch} (package ${pkg} is not installed).\n` +
      "Other ways to install: https://github.com/a-lafon/postgres-dba-mcp#install",
  );
  process.exit(1);
}
const r = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
if (r.error) throw r.error;
process.exit(r.status ?? 1);
