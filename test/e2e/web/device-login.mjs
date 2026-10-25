// Obtiene el token del device flow de una persona de prueba para el CLI, sin intervención:
// corre `jiku login --no-browser` en un JIKU_CONFIG_DIR propio y completa la aprobación en
// Zitadel con Playwright, con el usuario y la contraseña de .local/test-users.env.
//
//   node device-login.mjs USER     ../../../.local/cli/human-user
//   node device-login.mjs EXTERNAL ../../../.local/cli/human-external
//
// No imprime credenciales, ni la URL ni el código del device flow, ni tokens. El token queda en
// <config_dir>/tokens-<instancia>.json (0600) y el CLI lo refresca solo (~20 h de vida, con
// refresh token). Si Zitadel pide algo que necesita una persona (cambio de contraseña inicial,
// segundo factor), se detiene y lo dice.
//
// Variables: JIKU_BIN, JIKU_REPO (creds del sentinel), JIKU_SERVERS, JIKU_INSTANCE, JIKU_ISSUER,
// JIKU_CLIENT_ID y JIKU_PROJECT_ID; los dos últimos salen por defecto, solo lectura, de
// zitadel.client_id y zitadel.project_id de ~/.config/jiku/config.<instancia>.yaml.
import { chromium } from 'playwright';
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { loadCreds, redact, repoRoot } from './lib.mjs';

const [who, cfgArg] = process.argv.slice(2);
if (!['USER', 'EXTERNAL'].includes(who) || !cfgArg) {
  console.error('uso: node device-login.mjs <USER|EXTERNAL> <config_dir>');
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
  console.error(`falta JIKU_CLIENT_ID o JIKU_PROJECT_ID (ni en el entorno ni en ${yamlFile})`);
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
delete env.JIKU_KEY_FILE; // con una key el CLI sería un machine user

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
  exited.then((c) => rej(new Error(`jiku login salió con ${c} antes de dar la URL`)));
  setTimeout(() => rej(new Error('jiku login no dio la URL en 30 s')), 30000);
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
      throw new Error(`Zitadel exige cambiar la contraseña inicial de WEB_${who}: entrar una vez a mano y actualizar el .env`);
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
    throw new Error(`no se llegó a aprobar el código (${page.url().split('?')[0]}): ${R(body)}`);
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
console.log(rc === 0 ? `login de ${who} listo: ${cfgDir}/tokens-${instance}.json` : `jiku login salió con ${rc}`);
process.exit(rc === 0 ? 0 : 1);
