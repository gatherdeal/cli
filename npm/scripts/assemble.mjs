// Copies GoReleaser binaries from dist/ into the npm platform packages and
// stamps one version into every package.json. It does not publish.
//
//   node npm/scripts/assemble.mjs <version>
import { copyFileSync, chmodSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const version = process.argv[2];
if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(version ?? "")) {
  console.error("usage: node npm/scripts/assemble.mjs <semver>");
  process.exit(2);
}

const goos = { linux: "linux", darwin: "darwin", win32: "windows" };
const goarch = { x64: "amd64", arm64: "arm64" };

function stamp(path, mutate) {
  const pkg = JSON.parse(readFileSync(path, "utf8"));
  mutate(pkg);
  writeFileSync(path, JSON.stringify(pkg, null, 2) + "\n");
}

// Every published package carries the repository's LICENSE and NOTICE.
function copyLegal(pkgDir) {
  for (const file of ["LICENSE", "NOTICE"]) copyFileSync(file, join(pkgDir, file));
}

for (const dir of readdirSync("npm/platforms")) {
  const [os, cpu] = dir.split("-");
  const exe = os === "win32" ? "gatherdeal.exe" : "gatherdeal";
  const distDir = readdirSync("dist").find((d) => d.startsWith(`gatherdeal_${goos[os]}_${goarch[cpu]}`));
  if (!distDir) throw new Error(`dist/ has no build for ${dir}`);
  const binDir = join("npm/platforms", dir, "bin");
  mkdirSync(binDir, { recursive: true });
  copyFileSync(join("dist", distDir, exe), join(binDir, exe));
  if (os !== "win32") chmodSync(join(binDir, exe), 0o755);
  stamp(join("npm/platforms", dir, "package.json"), (p) => { p.version = version; });
  copyLegal(join("npm/platforms", dir));
}

copyLegal("npm/cli");

stamp("npm/cli/package.json", (p) => {
  p.version = version;
  for (const dep of Object.keys(p.optionalDependencies)) p.optionalDependencies[dep] = version;
});
console.log(`assembled npm packages for ${version}`);
