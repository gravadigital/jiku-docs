// Shared helpers of the browser e2e run.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
export const repoRoot = path.resolve(here, '../../..');

// Reads .local/test-users.env (or $TEST_USERS_ENV) without printing anything.
export function loadCreds() {
  const file = process.env.TEST_USERS_ENV || path.join(repoRoot, '.local/test-users.env');
  const env = {};
  for (const line of fs.readFileSync(file, 'utf8').split('\n')) {
    if (!line.includes('=') || line.trim().startsWith('#')) continue;
    const i = line.indexOf('=');
    env[line.slice(0, i).trim()] = line.slice(i + 1).trim().replace(/^["']|["']$/g, '');
  }
  for (const k of ['WEB_USER_LOGIN', 'WEB_USER_PASSWORD', 'WEB_EXTERNAL_LOGIN', 'WEB_EXTERNAL_PASSWORD']) {
    if (!env[k]) throw new Error(`missing ${k} in ${file}`);
  }
  return env;
}

// Hides credentials in any text about to be logged.
export function redact(text, creds) {
  let out = String(text);
  for (const v of Object.values(creds)) if (v && v.length > 3) out = out.split(v).join('<redacted>');
  return out;
}

// Hosted Zitadel login through next-auth. who = 'USER' | 'EXTERNAL'.
export async function login(page, base, who, creds) {
  await page.goto(base + '/login');
  await page.locator('button', { hasText: /iniciar sesi/i }).first().click();
  await page.waitForURL(/id\.grava\.io/, { timeout: 30000 });
  await page.fill('#loginName', creds[`WEB_${who}_LOGIN`]);
  await page.click('button:has-text("siguiente")');
  const pwd = page.locator('input[type=password]');
  try {
    await pwd.waitFor({ timeout: 15000 });
  } catch {
    const body = await page.locator('body').innerText();
    throw new Error(`login ${who}: Zitadel did not ask for a password: ${redact(body, creds).replace(/\s+/g, ' ').slice(0, 200)}`);
  }
  await pwd.fill(creds[`WEB_${who}_PASSWORD`]);
  await page.click('button:has-text("siguiente")');
  for (let i = 0; i < 30; i++) {
    await page.waitForTimeout(1000);
    const u = page.url();
    if (u.startsWith(base) && !u.includes('/login') && !u.includes('/api/auth')) return;
    if (u.includes('id.grava.io')) {
      // Initial password: Zitadel requires changing it. The suite does not change it (it is a real
      // account of the hosted IdP): the owner has to log in once by hand and update the .env.
      if (await page.locator('#change-old-password').count()) {
        throw Object.assign(
          new Error(`login ${who}: Zitadel requires changing the initial password; log in once by hand and update WEB_${who}_PASSWORD`),
          { skip: true }
        );
      }
      const skip = page.locator('button:has-text("saltar")');
      if (await skip.count()) { await skip.click(); continue; }
    }
  }
  const body = await page.locator('body').innerText().catch(() => '');
  throw new Error(`login ${who}: did not come back to ${base} (${page.url().split('?')[0]}): ${redact(body, creds).replace(/\s+/g, ' ').slice(0, 200)}`);
}
