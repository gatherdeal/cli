import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { execFileSync, spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { compareAssets } from './release-github.mjs';
import { checkMetadata, checkProvenance, matchesStatement, lookup, publishInOrder } from './release-npm.mjs';

const pkg = { name: '@gatherdeal/cli', version: '1.0.0' };
const bytes = Buffer.from('packed package');
const meta = { ...pkg, dist: {
  integrity: `sha512-${createHash('sha512').update(bytes).digest('base64')}`,
  shasum: createHash('sha1').update(bytes).digest('hex'), attestations: { url: 'https://registry.npmjs.org/attestation' },
} };
test('existing npm version must match both hashes and have provenance', () => {
  checkMetadata(meta, pkg, bytes);
  for (const bad of [{ ...meta, version: '2.0.0' }, { ...meta, name: 'other' },
    { ...meta, dist: { ...meta.dist, integrity: 'bad' } },
    { ...meta, dist: { ...meta.dist, shasum: 'bad' } },
    { ...meta, dist: { ...meta.dist, attestations: undefined } }]) {
    assert.throws(() => checkMetadata(bad, pkg, bytes));
  }
  assert.throws(() => checkMetadata(meta, pkg, Buffer.from('different')));
});
test('only HTTP 404 authorizes publishing', async () => {
  assert.equal(await lookup(pkg.name, pkg.version, async () => ({ status: 404 })), null);
  for (const status of [401, 403, 429, 500, 503]) {
    await assert.rejects(lookup(pkg.name, pkg.version, async () => ({ status, ok: false })));
  }
  await assert.rejects(lookup(pkg.name, pkg.version, async () => { throw Error('network'); }));
  assert.deepEqual(await lookup(pkg.name, pkg.version, async () => ({ ok: true, json: async () => meta })), meta);
});
const context = { repo: 'gatherdeal/cli', ref: 'refs/tags/v1.0.0', sha: 'abc', run: '123' };
const statement = { subject: [{ digest: { sha512: createHash('sha512').update(bytes).digest('hex') } }], predicateType: 'https://slsa.dev/provenance/v1', predicate: {
  buildDefinition: { externalParameters: { workflow: { repository: 'https://github.com/gatherdeal/cli',
    path: '.github/workflows/release.yml', ref: context.ref } },
  resolvedDependencies: [{ uri: `git+https://github.com/gatherdeal/cli@${context.ref}`, digest: { gitCommit: 'abc' } }] },
  runDetails: { metadata: { invocationId: 'https://github.com/gatherdeal/cli/actions/runs/123/attempts/1' } },
} };
test('provenance allows earlier attempts, rejects other run/tag/commit/repository', () => {
  assert.equal(matchesStatement(statement, context), true);
  for (const key of Object.keys(context)) assert.equal(matchesStatement(statement, { ...context, [key]: 'different' }), false);
  const wrong = structuredClone(statement);
  wrong.predicate.buildDefinition.externalParameters.workflow.path = '.github/workflows/other.yml';
  assert.equal(matchesStatement(wrong, context), false);
  assert.throws(() => checkProvenance({ invalid: [], missing: [] }, pkg, context, bytes));
});
test('provenance checks the certificate identity in cryptographically verified output', () => {
  const dir = mkdtempSync(join(tmpdir(), 'release-cert-test-'));
  try {
    const cert = join(dir, 'cert.pem');
    execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', join(dir, 'key.pem'),
      '-out', cert, '-days', '1', '-subj', '/CN=test', '-addext',
      `subjectAltName=URI:https://github.com/${context.repo}/.github/workflows/release.yml@${context.ref}`], { stdio: 'ignore' });
    const rawBytes = execFileSync('openssl', ['x509', '-in', cert, '-outform', 'DER']).toString('base64');
    const report = { invalid: [], missing: [], verified: [{ ...pkg, attestationBundles: [{ bundle: {
      verificationMaterial: { certificate: { rawBytes } },
      dsseEnvelope: { payload: Buffer.from(JSON.stringify(statement)).toString('base64') },
    } }] }] };
    checkProvenance(report, pkg, context, bytes);
    assert.throws(() => checkProvenance(report, pkg, { ...context, repo: 'attacker/cli' }, bytes));
    assert.throws(() => checkProvenance({ ...report, invalid: ['invalid signature'] }, pkg, context, bytes));
    assert.throws(() => checkProvenance({ ...report, missing: ['missing signature'] }, pkg, context, bytes));
  } finally { rmSync(dir, { recursive: true, force: true }); }
});
test('GitHub reconciliation rejects altered and unexpected assets', () => {
  const local = new Map([['a', bytes], ['b', bytes]]);
  assert.deepEqual(compareAssets(local, new Map([['a', bytes]])), ['b']);
  assert.deepEqual(compareAssets(local, local), []);
  assert.throws(() => compareAssets(local, new Map([['a', Buffer.from('changed')]])));
  assert.throws(() => compareAssets(local, new Map([['unexpected', bytes]])));
});

// A fake executable records calls; no real gh or npm command is executed.
function github(mode, scenario) {
  const dir = mkdtempSync(join(tmpdir(), 'release-flow-test-'));
  try {
    mkdirSync(join(dir, 'assets'));
    writeFileSync(join(dir, 'assets/a'), 'original');
    const script = `#!${process.execPath}
const fs = require('node:fs');
const args = process.argv.slice(2);
const scenario = ${JSON.stringify(scenario)};
fs.appendFileSync('calls', JSON.stringify(args) + '\\n');
if (args[0] === 'api') {
 const path = args.find(a => a.startsWith('repos/'));
 if (path.includes('/actions/runs/')) console.log(JSON.stringify([{artifacts: scenario.artifacts || []}]));
 else if (path.includes('/releases/assets/')) process.stdout.write(scenario.bytes || 'original');
 else if (path.includes('/assets?')) console.log(JSON.stringify([scenario.assets || []]));
 else console.log(JSON.stringify([scenario.release ? [scenario.release] : []]));
}
`;
    writeFileSync(join(dir, 'gh'), script, { mode: 0o755 });
    const result = spawnSync(process.execPath, [resolve('scripts/release-github.mjs'), mode], {
      cwd: dir, encoding: 'utf8', env: { ...process.env, PATH: dir,
        GITHUB_REPOSITORY: context.repo, GITHUB_RUN_ID: context.run, GITHUB_SHA: context.sha,
        GITHUB_REF_NAME: 'v1.0.0', GH_TOKEN: 'test', GITHUB_OUTPUT: join(dir, 'output') },
    });
    const calls = readFileSync(join(dir, 'calls'), 'utf8').trim().split('\n').map(JSON.parse);
    return { ...result, writes: calls.filter(a => a[0] === 'release' && ['create', 'upload', 'edit'].includes(a[1])), calls };
  } finally { rmSync(dir, { recursive: true, force: true }); }
}
const release = { id: 1, tag_name: 'v1.0.0', draft: true,
  body: '<!-- release-run:gatherdeal/cli/123 commit:abc -->' };
test('draft creation and partial upload resume', () => {
  const fresh = github('draft', {});
  assert.equal(fresh.status, 0, fresh.stderr);
  assert.deepEqual(fresh.writes.map(a => a[1]), ['create', 'upload']);
  const partial = github('draft', { release });
  assert.equal(partial.status, 0, partial.stderr);
  assert.deepEqual(partial.writes.map(a => a[1]), ['upload']);
  const complete = github('draft', { release, assets: [{ id: 2, name: 'a' }] });
  assert.equal(complete.status, 0, complete.stderr);
  assert.deepEqual(complete.writes, []);
});
test('mismatch, foreign run and incomplete public release fail before writes', () => {
  for (const scenario of [
    { release, assets: [{ id: 2, name: 'a' }], bytes: 'changed' },
    { release: { ...release, body: 'another run' } },
    { release: { ...release, draft: false } },
  ]) {
    const result = github('draft', scenario);
    assert.notEqual(result.status, 0);
    assert.deepEqual(result.writes, []);
  }
});
test('finalization requires complete matching assets and accepts public retry', () => {
  const incomplete = github('finalize', { release });
  assert.notEqual(incomplete.status, 0);
  assert.deepEqual(incomplete.writes, []);
  const complete = github('finalize', { release, assets: [{ id: 2, name: 'a' }] });
  assert.equal(complete.status, 0, complete.stderr);
  assert.deepEqual(complete.writes.map(a => a[1]), ['edit']);
  assert.ok(complete.writes[0].includes('--latest=true'));
  const published = github('finalize', { release: { ...release, draft: false }, assets: [{ id: 2, name: 'a' }] });
  assert.equal(published.status, 0, published.stderr);
  assert.deepEqual(published.writes, []);
});
test('full retry restores original run artifact or release, never writes remotely', () => {
  const artifact = github('restore', { artifacts: [{ name: 'release-assets', expired: false }] });
  assert.equal(artifact.status, 0, artifact.stderr);
  assert.ok(artifact.calls.some(a => a[0] === 'run' && a[1] === 'download'));
  const fallback = github('restore', { release });
  assert.equal(fallback.status, 0, fallback.stderr);
  assert.ok(fallback.calls.some(a => a[0] === 'release' && a[1] === 'download'));
  const lost = github('restore', { artifacts: [{ name: 'release-assets', expired: true }] });
  assert.notEqual(lost.status, 0);
  for (const result of [artifact, fallback, lost]) assert.deepEqual(result.writes, []);
});

test('partial npm failure resumes without republishing and keeps launcher last', async () => {
  const published = new Set();
  const calls = [];
  let fail = true;
  const operations = {
    pack: name => ({ pkg: { name, version: '1.0.0' } }),
    find: async name => published.has(name) ? { name } : null,
    publish: async ({ pkg }) => {
      calls.push(pkg.name);
      if (pkg.name === 'platform-b' && fail) throw Error('registry unavailable');
      published.add(pkg.name);
    },
    verify: async (meta, { pkg }) => assert.equal(meta.name, pkg.name),
  };
  const dirs = ['platform-a', 'platform-b', 'launcher'];
  await assert.rejects(publishInOrder(dirs, operations));
  assert.deepEqual([...published], ['platform-a']);
  fail = false;
  await publishInOrder(dirs, operations);
  assert.deepEqual(calls, ['platform-a', 'platform-b', 'platform-b', 'launcher']);
  calls.length = 0;
  await publishInOrder(dirs, operations);
  assert.deepEqual(calls, []);
  await assert.rejects(publishInOrder(dirs, { ...operations, verify: async () => { throw Error('mismatch'); } }));
  assert.deepEqual(calls, []);
});
