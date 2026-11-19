// Pack once, compare immutable registry versions, publish only absent versions,
// and wait for every version to become visible and verified.
import { execFile } from 'node:child_process';
import { createHash, X509Certificate } from 'node:crypto';
import { mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { setTimeout } from 'node:timers/promises';
import { pathToFileURL } from 'node:url';
import { promisify } from 'node:util';

const registry = 'https://registry.npmjs.org';
const execFileAsync = promisify(execFile);
export function checkMetadata(meta, pkg, bytes) {
  const integrity = `sha512-${createHash('sha512').update(bytes).digest('base64')}`;
  const shasum = createHash('sha1').update(bytes).digest('hex');
  if (meta.name !== pkg.name || meta.version !== pkg.version ||
      meta.dist?.integrity !== integrity || meta.dist?.shasum !== shasum) {
    throw new Error(`${pkg.name}@${pkg.version}: immutable npm tarball differs`);
  }
  if (!meta.dist?.attestations?.url) throw new Error('npm provenance missing');
}

export function checkProvenance(report, pkg, context, bytes) {
  const verified = report.verified?.find(p => p.name === pkg.name && p.version === pkg.version);
  const bundles = verified?.attestationBundles;
  if (report.invalid?.length) throw new Error('npm signature verification failed');
  if (report.missing?.length) throw new NotReady('npm signatures or attestations are not yet available');
  if (!Array.isArray(bundles)) throw new Error('npm provenance verification unavailable');
  const statements = bundles.filter(a => {
    const material = (a.bundle ?? a).verificationMaterial;
    const cert = material?.certificate ?? material?.x509CertificateChain?.certificates?.[0];
    if (!cert?.rawBytes) return false;
    const identity = new X509Certificate(Buffer.from(cert.rawBytes, 'base64')).subjectAltName;
    return identity === `URI:https://github.com/${context.repo}/.github/workflows/release.yml@${context.ref}`;
  }).map(a => {
    const envelope = (a.bundle ?? a).dsseEnvelope;
    return envelope ? JSON.parse(Buffer.from(envelope.payload, 'base64').toString()) : null;
  });
  if (!statements.some(s => matchesStatement(s, context) && s.subject?.some(subject =>
    subject.digest?.sha512 === createHash('sha512').update(bytes).digest('hex')))) throw new Error('npm provenance does not match this workflow, tag, commit and run');
}

export function matchesStatement(s, context) {
  const expected = `git+https://github.com/${context.repo}@${context.ref}`;
  return s?.predicateType === 'https://slsa.dev/provenance/v1' &&
    s.predicate?.buildDefinition?.externalParameters?.workflow?.repository === `https://github.com/${context.repo}` &&
    s.predicate.buildDefinition.externalParameters.workflow.path === '.github/workflows/release.yml' &&
    s.predicate.buildDefinition.externalParameters.workflow.ref === context.ref &&
    s.predicate.buildDefinition.resolvedDependencies?.some(d => d.uri === expected && d.digest?.gitCommit === context.sha) &&
    s.predicate.runDetails?.metadata?.invocationId?.startsWith(`https://github.com/${context.repo}/actions/runs/${context.run}/attempts/`);
}

export async function lookup(name, version, fetcher = fetch) {
  const response = await fetcher(`${registry}/${encodeURIComponent(name)}/${encodeURIComponent(version)}`);
  if (response.status === 404) return null;
  if (!response.ok) throw new Error(`npm lookup failed: HTTP ${response.status}`);
  return response.json();
}

// A condition that resolves by itself once the registry catches up with a
// publish: the version is not yet visible, installable or attested.
export class NotReady extends Error {}

export function notYetInstallable(stderr = '') {
  return /\bcode (E404|ETARGET)\b/.test(stderr);
}

// Retries NotReady with bounded backoff; any other error fails at once.
export async function untilReady(attempt, label, { sleep, now, log,
  budgetMs = 10 * 60_000, firstDelayMs = 5_000, maxDelayMs = 30_000 }) {
  const deadline = now() + budgetMs;
  for (let delay = firstDelayMs; ; delay = Math.min(delay * 2, maxDelayMs)) {
    try {
      return await attempt();
    } catch (err) {
      if (!(err instanceof NotReady)) throw err;
      if (now() + delay > deadline) {
        throw new Error(`${err.message} after ${budgetMs / 1000}s; rerunning this job is safe`);
      }
      log(`${label}: ${err.message}; retrying in ${delay / 1000}s`);
      await sleep(delay);
    }
  }
}

// Publishes a version only when the registry answers 404, then waits until
// the new or existing version is visible and verified.
async function release(dir, { pack, find, publish, verify }, timing) {
  let label = dir;
  try {
    const item = await pack(dir);
    label = `${item.pkg.name}@${item.pkg.version}`;
    if (!await find(item.pkg.name, item.pkg.version)) await publish(item);
    await untilReady(async () => {
      const meta = await find(item.pkg.name, item.pkg.version);
      if (!meta) throw new NotReady('published version is not yet visible');
      await verify(meta, item);
    }, label, timing);
  } catch (err) {
    throw new Error(`${label}: ${err.message}`, { cause: err });
  }
}

// Platform packages run in parallel and all settle before the outcome is
// decided; the launcher depends on them and is published only after every
// platform package verifies.
export async function publishAll(platformDirs, launcherDir, operations, timing) {
  const results = await Promise.allSettled(platformDirs.map(dir => release(dir, operations, timing)));
  const errors = results.filter(r => r.status === 'rejected').map(r => r.reason);
  if (errors.length) {
    throw new AggregateError(errors, `launcher not published: ${errors.map(e => e.message).join('; ')}`);
  }
  await release(launcherDir, operations, timing);
}

async function main() {
  const context = { repo: process.env.GITHUB_REPOSITORY, ref: process.env.GITHUB_REF,
    sha: process.env.GITHUB_SHA, run: process.env.GITHUB_RUN_ID };
  if (Object.values(context).some(v => !v)) throw new Error('missing release context');
  const temp = mkdtempSync(join(tmpdir(), 'release-npm-'));
  const npm = async (args, cwd) =>
    (await execFileAsync('npm', args, { cwd, encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 })).stdout;
  try {
    const platforms = readdirSync('npm/platforms').sort().map(d => `npm/platforms/${d}`);
    await publishAll(platforms, 'npm/cli', {
      async pack(dir) {
        const pkg = JSON.parse(readFileSync(`${dir}/package.json`, 'utf8'));
        if (`refs/tags/v${pkg.version}` !== context.ref) throw new Error('package version differs from tag');
        const [pack] = JSON.parse(await npm(['pack', resolve(dir), '--ignore-scripts', '--json', '--pack-destination', temp]));
        const tarball = join(temp, pack.filename);
        const bytes = readFileSync(tarball);
        return { pkg, tarball, bytes };
      },
      find: lookup,
      async publish({ pkg, tarball }) {
        await npm(['publish', tarball, '--ignore-scripts', '--provenance', '--access', 'public',
          '--registry', registry, '--tag', pkg.version.includes('-') ? 'next' : 'latest']);
      },
      async verify(meta, { pkg, bytes }) {
        checkMetadata(meta, pkg, bytes);
        const audit = mkdtempSync(join(temp, 'audit-'));
        writeFileSync(join(audit, 'package.json'), JSON.stringify({ private: true }));
        try {
          // Force permits auditing other OS/CPU packages; scripts are never run.
          await npm(['install', `${pkg.name}@${pkg.version}`, '--ignore-scripts', '--force', '--omit=optional',
            '--no-audit', '--no-fund', '--registry', registry], audit);
        } catch (err) {
          if (notYetInstallable(err.stderr)) throw new NotReady('published version is not yet installable');
          throw err;
        }
        // The audit exits non-zero when signatures are missing or invalid;
        // its JSON report says which.
        let report;
        try {
          report = await npm(['audit', 'signatures', '--json', '--include-attestations', '--registry', registry], audit);
        } catch (err) {
          if (!err.stdout) throw err;
          report = err.stdout;
        }
        checkProvenance(JSON.parse(report), pkg, context, bytes);
        console.log(`verified ${pkg.name}@${pkg.version}`);
      },
    }, { sleep: setTimeout, now: Date.now, log: console.log });
  } finally { rmSync(temp, { recursive: true, force: true }); }
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) await main();
