import { spawn, spawnSync } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const temp = await mkdtemp(join(tmpdir(), 'drifter-admin-e2e-'));
const binary = join(temp, 'drifter-admin-fixture');
const database = join(temp, 'mappings.sqlite');
let server;

async function stopServer() {
  if (!server || server.exitCode !== null) return;
  server.kill('SIGTERM');
  await Promise.race([
    new Promise((resolveExit) => server.once('exit', resolveExit)),
    delay(5000).then(() => { throw new Error('Drifter server did not stop'); }),
  ]);
}

try {
  const build = spawnSync('mise', ['exec', '--', 'go', 'build', '-o', binary, './e2e/fixture'], { stdio: 'inherit' });
  if (build.status !== 0) throw new Error(`Synthetic admin fixture build failed (${build.status ?? build.signal})`);
  server = spawn(binary, ['--addr', '127.0.0.1:0', '--db', database], {
    stdio: ['ignore', 'pipe', 'inherit'],
    env: process.env,
  });
  let output = '';
  let baseURL;
  server.stdout.setEncoding('utf8');
  server.stdout.on('data', (chunk) => {
    output += chunk;
    for (const line of output.split('\n')) {
      try {
        const record = JSON.parse(line);
        if (record.msg === 'admin server starting') baseURL = `http://${record.addr}`;
      } catch {}
    }
  });
  for (let i = 0; i < 100 && !baseURL; i++) {
    if (server.exitCode !== null) throw new Error(`Drifter exited before startup: ${output}`);
    await delay(50);
  }
  if (!baseURL) throw new Error(`Drifter did not report startup: ${output}`);
  const playwright = resolve('node_modules/@playwright/test/cli.js');
  const testArgs = process.env.E2E_PROBE === '1'
    ? ['test', 'e2e/probe.spec.ts']
    : ['test', 'e2e/admin.spec.ts'];
  const result = spawnSync(process.execPath, [playwright, ...testArgs], {
    stdio: 'inherit', env: { ...process.env, E2E_BASE_URL: baseURL },
  });
  if (result.status !== 0) throw new Error(`Playwright failed (${result.status ?? result.signal})`);
} finally {
  await stopServer();
  await rm(temp, { recursive: true, force: true });
}
