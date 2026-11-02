// Only invoked by release.yml. Never replace an artifact or release asset.
import { execFileSync } from 'node:child_process';
import { appendFileSync, readFileSync, readdirSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

export function compareAssets(local, remote) {
  for (const [name, bytes] of remote) {
    if (!local.has(name) || !bytes.equals(local.get(name))) {
      throw new Error(`GitHub asset mismatch: ${name}; refusing to overwrite`);
    }
  }
  return [...local.keys()].filter(name => !remote.has(name));
}

async function main(mode) {
  const { GITHUB_REPOSITORY: repo, GITHUB_RUN_ID: run, GITHUB_SHA: sha,
    GITHUB_REF_NAME: tag, GH_TOKEN: token, GITHUB_OUTPUT: output } = process.env;
  if (!repo || !run || !sha || !tag || !token) throw new Error('missing release context');
  const gh = (...args) => execFileSync('gh', args, { encoding: 'utf8' });
  const pages = path => JSON.parse(gh('api', '--paginate', '--slurp', path));
  const releases = pages(`repos/${repo}/releases?per_page=100`).flat();
  let release = releases.find(r => r.tag_name === tag);
  const marker = `<!-- release-run:${repo}/${run} commit:${sha} -->`;
  if (release && !release.body?.includes(marker)) throw new Error('existing release belongs to a different run/commit');
  if (mode === 'restore') {
    const artifacts = pages(`repos/${repo}/actions/runs/${run}/artifacts?per_page=100`)
      .flatMap(p => p.artifacts).filter(a => a.name === 'release-assets');
    if (artifacts.length > 1) throw new Error('ambiguous release artifact');
    if (artifacts.length && !artifacts[0].expired) {
      gh('run', 'download', run, '--repo', repo, '--name', 'release-assets', '--dir', 'assets');
      appendFileSync(output, 'restored=true\nupload=false\n');
    } else if (release) {
      // A complete release is a second copy of the original bytes. Verification
      // below must succeed; incomplete drafts are never repaired by rebuilding.
      gh('release', 'download', tag, '--repo', repo, '--dir', 'assets');
      appendFileSync(output, 'restored=true\nupload=true\n');
    } else if (artifacts.length) {
      throw new Error('original assets unavailable; refusing to rebuild on a retry');
    } else {
      appendFileSync(output, 'restored=false\nupload=true\n');
    }
    return;
  }
  const local = new Map(readdirSync('assets').map(n => [n, readFileSync(`assets/${n}`)]));
  const remote = new Map();
  if (release) {
    const assets = pages(`repos/${repo}/releases/${release.id}/assets?per_page=100`).flat();
    for (const asset of assets) {
      const bytes = execFileSync('gh', ['api', `repos/${repo}/releases/assets/${asset.id}`,
        '-H', 'Accept: application/octet-stream'], { maxBuffer: 128 * 1024 * 1024 });
      if (remote.has(asset.name)) throw new Error('duplicate remote asset');
      remote.set(asset.name, bytes);
    }
  }
  const missing = compareAssets(local, remote);
  if (mode === 'draft') {
    if (!release) {
      gh('release', 'create', tag, '--repo', repo, '--draft', '--verify-tag', '--title', tag,
        '--generate-notes', '--notes', marker, ...(tag.includes('-') ? ['--prerelease'] : []));
      release = { draft: true };
    }
    if (missing.length && !release.draft) throw new Error('public release is missing assets');
    for (const name of missing) gh('release', 'upload', tag, `assets/${name}`, '--repo', repo);
  } else if (mode === 'finalize') {
    if (!release || missing.length) throw new Error('release assets are incomplete');
    if (release.draft) gh('release', 'edit', tag, '--repo', repo, '--draft=false',
      `--latest=${!tag.includes('-')}`);
  } else throw new Error(`unknown mode ${mode}`);
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) await main(process.argv[2]);
