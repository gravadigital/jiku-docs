// Obtains the device-flow token of a test person for the CLI, unattended: runs
// `jiku login --no-browser` in its own JIKU_CONFIG_DIR and completes the approval in Zitadel with
// Playwright, with the user and password of .local/test-users.env.
//
//   node device-login.mjs USER     ../../../.local/cli/human-user
//   node device-login.mjs EXTERNAL ../../../.local/cli/human-external
//
// It prints no credentials, nor the device-flow URL or code, nor tokens. The token ends up in
// <config_dir>/tokens-<instance>.json (0600) and the CLI refreshes it by itself (~20 h lifetime,
// with a refresh token). If Zitadel asks for something that needs a person (initial password
// change, second factor), it stops and says so.
//
// Variables: JIKU_BIN, JIKU_REPO (sentinel creds), JIKU_SERVERS, JIKU_INSTANCE, JIKU_ISSUER,
// JIKU_CLIENT_ID and JIKU_PROJECT_ID; the last two default, read only, to zitadel.client_id and
// zitadel.project_id of ~/.config/jiku/config.<instance>.yaml.
import { chromium } from 'playwright';
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { loadCreds, redact, repoRoot } from './lib.mjs';

const [who, cfgArg] = process.argv.slice(2);
if (!['USER', 'EXTERNAL'].includes(who) || !cfgArg) {
  console.error('usage: node device-login.mjs <USER|EXTERNAL> <config_dir>');
  process.exit(64);
}
const cfgDir = path.resolve(cfgArg);
const creds = loadCreds();
const R = (t) => redact(t, creds).replace(/\s+/g, ' ').slice(0, 300);

const instance = process.env.JIKU_INSTANCE || 'dev';
const yamlFile = path.join(os.homedir(), '.config/jiku', `config.${instance}.yaml`);
const yaml = fs.existsSync(yamlFile) ? fs.readFileSync(yamlFile, 'utf8') : '';
const fromYaml = (k) => yaml.match(new RegExp(`^\\s*${k}:\\s*"?([^"\\n#]+)"?`, 'm'))?.[1]?.trim();
const clientId = process.env.JIKU_CLIENT_ID || fromYaml('client_id');
const projectId = process.env.JIKU_PROJECT_ID || fromYaml('project_id');
if (!clientId || !projectId) {
  console.error(`missing JIKU_CLIENT_ID or JIKU_PROJECT_ID (neither in the environment nor in ${yamlFile})`);
  process.exit(2);
}
const jikuRepo = process.env.JIKU_REPO || path.resolve(repoRoot, '../../jiku');
const bin = process.env.JIKU_BIN || path.resolve(repoRoot, '../../jiku-go/bin/jiku');

fs.mkdirSync(cfgDir, { recursive: true, mode: 0o700 });
const env = {
  ...process.env,
  JIKU_SERVERS: process.env.JIKU_SERVERS || 'nats://localhost:4222',
  JIKU_INSTANCE: instance,
  JIKU_CREDS: process.env.JIKU_CREDS || path.join(jikuRepo, 'deploy/nats/creds/sentinel-client.creds'),
  JIKU_ISSUER: process.env.JIKU_ISSUER || 'https://id.grava.io',
  JIKU_CLIENT_ID: clientId,
  JIKU_PROJECT_ID: projectId,
  JIKU_CONFIG_DIR: cfgDir,
};
delete env.JIKU_KEY_FILE; // with a key the CLI would be a machine user

const child = spawn(bin, ['login', '--no-browser'], { env });
let stderr = '';
const exited = new Promise((res) => child.on('exit', (c) => res(c)));
const url = await new Promise((res, rej) => {
  child.stderr.on('data', (d) => {
    stderr += d;
    const m = stderr.match(/Open:\s+(\S+)/);
    if (m && /Code:/.test(stderr)) res(m[1]);
  });
  child.stdout.resume();
  exited.then((c) => rej(new Error(`jiku login exited with ${c} before giving the URL`)));
  setTimeout(() => rej(new Error('jiku login did not give the URL in 30 s')), 30000);
}).catch((e) => {
  console.error(e.message);
  child.kill();
  process.exit(1);
});
const code = stderr.match(/Code:\s+(\S+)/)?.[1];

const browser = await chromium.launch();
const page = await browser.newPage();
let approved = false;
try {
  await page.goto(url);
  const submit = 'button[type=submit], button:has-text("siguiente"), button:has-text("next")';
  for (let i = 0; i < 25 && !approved; i++) {
    await page.waitForTimeout(1500);
    const visible = async (sel) => (await page.locator(sel).count()) > 0 && page.locator(sel).first().isVisible();
    if (/\/device\/allowed/.test(page.url()) || /Approved/.test(await page.locator('body').innerText().catch(() => ''))) {
      approved = true;
    } else if (await page.locator('#change-old-password').count()) {
      throw new Error(`Zitadel requires changing the initial password of WEB_${who}: log in once by hand and update the .env`);
    } else if (await visible('#loginName')) {
      await page.fill('#loginName', creds[`WEB_${who}_LOGIN`]);
      await page.click(submit);
    } else if (await visible('input[type=password]')) {
      await page.locator('input[type=password]').first().fill(creds[`WEB_${who}_PASSWORD`]);
      await page.click(submit);
    } else if (await visible('input[name=user_code], input#user_code, input[name=code]')) {
      const input = page.locator('input[name=user_code], input#user_code, input[name=code]').first();
      if (!(await input.inputValue()) && code) await input.fill(code);
      await page.click('button[type=submit]');
    } else {
      const allow = page.locator(
        'button:has-text("permitir"), button:has-text("allow"), button:has-text("autorizar"), button:has-text("aprobar"), button:has-text("approve")'
      );
      const skip = page.locator('button:has-text("saltar"), button:has-text("skip")');
      if (await allow.count()) await allow.first().click();
      else if (await skip.count()) await skip.first().click();
    }
  }
  if (!approved) {
    const body = await page.locator('body').innerText().catch(() => '');
    throw new Error(`the code was never approved (${page.url().split('?')[0]}): ${R(body)}`);
  }
} catch (e) {
  console.error(R(e.message));
  child.kill();
  await browser.close();
  process.exit(1);
}
await browser.close();
const timer = setTimeout(() => child.kill(), 30000);
const rc = await exited;
clearTimeout(timer);
console.log(rc === 0 ? `login of ${who} done: ${cfgDir}/tokens-${instance}.json` : `jiku login exited with ${rc}`);
process.exit(rc === 0 ? 0 : 1);
