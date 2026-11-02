// Pack once, compare immutable registry versions, publish only absent versions.
import { execFileSync } from 'node:child_process';
import { createHash, X509Certificate } from 'node:crypto';
import { mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const registry = 'https://registry.npmjs.org';
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
  if (report.invalid?.length || report.missing?.length || !Array.isArray(bundles)) {
    throw new Error('npm signature/provenance verification unavailable or failed');
  }
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

// Keep sequencing explicit: a failed platform check must prevent the launcher.
export async function publishInOrder(dirs, { pack, find, publish, verify }) {
  for (const dir of dirs) {
    const item = await pack(dir);
    let meta = await find(item.pkg.name, item.pkg.version);
    if (!meta) {
      await publish(item);
      meta = await find(item.pkg.name, item.pkg.version);
      if (!meta) throw new Error('published package is not yet visible; rerun this job');
    }
    await verify(meta, item);
  }
}

async function main() {
  const context = { repo: process.env.GITHUB_REPOSITORY, ref: process.env.GITHUB_REF,
    sha: process.env.GITHUB_SHA, run: process.env.GITHUB_RUN_ID };
  if (Object.values(context).some(v => !v)) throw new Error('missing release context');
  const temp = mkdtempSync(join(tmpdir(), 'release-npm-'));
  const npm = (args, cwd) => execFileSync('npm', args, { cwd, encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 });
  try {
    const dirs = readdirSync('npm/platforms').sort().map(d => `npm/platforms/${d}`).concat('npm/cli');
    await publishInOrder(dirs, {
      pack(dir) {
        const pkg = JSON.parse(readFileSync(`${dir}/package.json`, 'utf8'));
        if (`refs/tags/v${pkg.version}` !== context.ref) throw new Error('package version differs from tag');
        const [pack] = JSON.parse(npm(['pack', resolve(dir), '--ignore-scripts', '--json', '--pack-destination', temp]));
        const tarball = join(temp, pack.filename);
        const bytes = readFileSync(tarball);
        return { pkg, tarball, bytes };
      },
      find: lookup,
      publish({ pkg, tarball }) {
        npm(['publish', tarball, '--ignore-scripts', '--provenance', '--access', 'public',
          '--registry', registry, '--tag', pkg.version.includes('-') ? 'next' : 'latest']);
      },
      verify(meta, { pkg, bytes }) {
        checkMetadata(meta, pkg, bytes);
        const audit = mkdtempSync(join(temp, 'audit-'));
        writeFileSync(join(audit, 'package.json'), JSON.stringify({ private: true }));
        // Force permits auditing other OS/CPU packages; scripts are never run.
        npm(['install', `${pkg.name}@${pkg.version}`, '--ignore-scripts', '--force', '--omit=optional',
          '--no-audit', '--no-fund', '--registry', registry], audit);
        const report = JSON.parse(npm(['audit', 'signatures', '--json', '--include-attestations', '--registry', registry], audit));
        checkProvenance(report, pkg, context, bytes);
        console.log(`verified ${pkg.name}@${pkg.version}`);
      },
    });
  } finally { rmSync(temp, { recursive: true, force: true }); }
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) await main();
