// End-to-end browser run of the jiku-docs integration in web and opus-web, with the
// P-1 model (documentation without folders, contract 1.3.0).
//
//   node run.mjs [--only web|opus|public] [--down] [--headed] [--steps W1,W5] [--keep]
//
// See README.md: variables, prerequisites and the data it expects from the local stack.
import { chromium } from 'playwright';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { loadCreds, login, redact, repoRoot } from './lib.mjs';

const args = process.argv.slice(2);
const only = args.includes('--only') ? args[args.indexOf('--only') + 1] : null;
const withDown = args.includes('--down');
const headed = args.includes('--headed');
const keep = args.includes('--keep');

const WEB = process.env.E2E_WEB_URL || 'http://localhost:3000';
const OPUS = process.env.E2E_OPUS_URL || 'http://localhost:3001';
const PUBLIC = process.env.E2E_PUBLIC_URL || 'http://127.0.0.1:3100';
const DOCS_CONTAINER = process.env.E2E_DOCS_CONTAINER || 'jiku-docs-local';
// Command (with arguments) that runs `jiku` as the machine user of the jiku-docs service (role
// docs, class service: reads and writes everything, `restricted` too), e.g. "/path/as.sh docs".
// E2E_CLI_ADMIN is the former name and is still accepted.
const CLI_DOCS = process.env.E2E_CLI_DOCS || process.env.E2E_CLI_ADMIN || '';
const OUT =
  process.env.E2E_OUT ||
  path.join(repoRoot, '.local/e2e-web', new Date().toISOString().replace(/[:.]/g, '-'));
fs.mkdirSync(OUT, { recursive: true });

// Data of the local stack (from an earlier run over the bus, see README).
const D = {
  project: Number(process.env.E2E_PROJECT_ID || 43),
  projectName: process.env.E2E_PROJECT_NAME || 'EXO - Videoconferencia',
  projectWord: 'Videoconferencia', // appears only in the name of that project
  client: Number(process.env.E2E_CLIENT_ID || 1),
  clientName: process.env.E2E_CLIENT_NAME || 'EXO',
  requirement: Number(process.env.E2E_REQUIREMENT_ID || 54),
  publicRequirement: Number(process.env.E2E_PUBLIC_REQUIREMENT_ID || 15),
  task: Number(process.env.E2E_TASK_ID || 2716),
  arq: process.env.E2E_DOC_PUBLIC || '01M5RAFX6A1R2VD9T277DEY0XP', // public, project/req/task, client 1
  arqTitle: 'Arquitectura de videoconferencia',
  arqTopic: 'arquitectura',
  aduana: '01M5RAYHDTK4B43C52NZ16G5ZM', // public, requirement 15
  aduanaTitle: 'Integración con aduana',
  infra: '01M5RAN06ZMZ07EMMT69E274TY', // in /infra/: the P-1 migration left it internal
  infraTitle: 'Servidor de staging',
  internal: '01M5RAYNGC4J6Z0RE7YQ8DQ944', // internal, task 2697 of the project (indirect)
  internalTitle: 'Reunión semanal',
  arqAttachment: '01M5RB1RRDH3AX4NWRQ10FFCDB', // diagrama.png of arq
  privAttachment: '01M5RB3FJXEEP0WH66VAJTTHH1', // attachment of an internal document
  otherProjectDoc: '01M5RCXWR9WGC9JMRSE3E3FX1R', // of another project (restricted after P-1)
  // Notes imported from HedgeDoc (read by the user role).
  impImage: process.env.E2E_IMPORTED_IMAGE_DOC || '01KPP6J840WSMXAM5CF9NX830Z', // with an image
  impImageTitle: 'Opus / Arquitectura del sistema',
  impImageAtt: '01KPP6J840V6XKJBAKS6FVB49G',
  impLinkSrc: process.env.E2E_IMPORTED_LINK_DOC || '01EFPRY9405SWET0DANRE36EBX', // Wecerty.md
  impLinkText: 'Versión 1',
  impLinkDst: '01G2NH12ZGDXS8EE7P1KC5ZV5F',
  impLinkDstTitle: 'Wecerty - versión 1',
};

// Data of this run: everything the suite creates carries the prefix "e2e-p1 " and the mark TAG (a
// word that exists nowhere else, to search for it). It is deleted at the end (unless --keep).
const TAG = `p1${Date.now().toString(36)}`;
const PREFIX = 'e2e-p1';
const created = []; // ids to delete at the end

const creds = loadCreds();
const results = [];
const log = (...a) => console.log(redact(a.join(' '), creds));

// Jiku bugs (not jiku-docs ones) reported and not fixed yet. A scenario that finds one goes on
// with the rest and ends as "jiku bug" instead of "ok"; it does not count as a failure for the exit
// code unless E2E_STRICT=1. If the bug is fixed, the scenario goes back to "ok" by itself: the
// condition is still checked.
const KNOWN = {
  'JIKU-WEB-1': 'the viewer of the internal web shows the frontmatter (documento.md: "el frontmatter no se muestra", the frontmatter is not shown)',
  'JIKU-WEB-2': '"Crear y editar" (create and edit) does not enter editing: after router.push the page stays on ?editar=1 without requesting the lock',
  'JIKU-WEB-3':
    'Client/Project of creation and editing: GET /api/projects does not return `client`, so with a client chosen the Project selector only offers "Sin proyecto" (no project) and choosing the project does not fill in the client',
  'JIKU-WEB-4':
    'the web compares the lock holder and the author (Zitadel id) with `session.user.id` (Jiku uuid): the person never recognises themselves ("Lo estás editando en otra pestaña", you are editing it in another tab; "vos", you)',
  'JIKU-WEB-5':
    'what a person writes from the web has no name (lock.holderName, updatedByName, authorName null): the api `actor` envelope carries no name or username and jiku-docs does not look it up in core, so the web shows "Otra persona" (another person) and "—" (item 12 still open)',
};
const STRICT = process.env.E2E_STRICT === '1';

// --steps W1,W11: runs only those scenarios (for debugging; the ones that depend on another fail).
const STEPS = args.includes('--steps') ? args[args.indexOf('--steps') + 1].split(',') : null;

async function step(id, name, fn) {
  if (STEPS && !STEPS.includes(id)) return;
  const t0 = Date.now();
  // The same bug found several times in the scenario is reported once, with every detail.
  const found = new Map();
  const bug = (code, detail = '') => found.set(code, [...(found.get(code) || []), detail].filter(Boolean));
  const bugsOf = () => [...found].map(([code, ds]) => `${code} ${KNOWN[code]}${ds.length ? ` (${ds.join('; ')})` : ''}`);
  try {
    let note = (await fn(bug)) || '';
    const bugs = bugsOf();
    if (bugs.length) {
      note = `${bugs.join('; ')}${note ? ' — rest: ' + note : ''}`;
      results.push({ id, name, status: 'jiku bug', note });
      log(`BUG   ${id} ${name} — ${note} (${Date.now() - t0} ms)`);
      return;
    }
    results.push({ id, name, status: 'ok', note });
    log(`ok    ${id} ${name}${note ? ' — ' + note : ''} (${Date.now() - t0} ms)`);
  } catch (e) {
    const status = e.skip ? 'not tested' : 'fail';
    if (status === 'fail' && failPage) await shot(failPage, `FAIL-${id}`);
    const msg = redact(e.message, creds).split('\n')[0];
    const bugs = bugsOf();
    results.push({ id, name, status, note: (bugs.length ? bugs.join('; ') + ' — ' : '') + msg });
    log(`${status === 'fail' ? 'FAIL ' : 'skip '} ${id} ${name} — ${msg}`);
  }
}
// Page captured when a scenario fails (the one of the running suite).
let failPage = null;
const skip = (msg) => Object.assign(new Error(msg), { skip: true });
const assert = (cond, msg) => {
  if (!cond) throw new Error(msg);
};
// Screenshots mask any text with the login of the test users.
let shotN = 0;
const shot = (page, name, opts = {}) =>
  page
    .screenshot({
      path: path.join(OUT, `${String(++shotN).padStart(2, '0')}-${name}.png`),
      fullPage: true,
      mask: [page.getByText(creds.WEB_USER_LOGIN), page.getByText(creds.WEB_EXTERNAL_LOGIN)],
      ...opts,
    })
    .catch(() => {});

function cli(...cmdArgs) {
  if (!CLI_DOCS) throw skip('E2E_CLI_DOCS not set');
  const [bin, ...pre] = CLI_DOCS.split(' ');
  try {
    return execFileSync(bin, [...pre, ...cmdArgs], { encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe'] });
  } catch (e) {
    // The first line of the error is what reaches the table: make it say what jiku answered.
    const why = String(e.stderr || '').split('\n').filter((l) => /error|code|:/.test(l)).slice(0, 2).join(' ').trim();
    throw Object.assign(new Error(`jiku ${cmdArgs.slice(0, 2).join(' ')}: ${why || e.message}`), { stderr: e.stderr });
  }
}
const cliJSON = (...a) => JSON.parse(cli(...a, '-o', 'json'));
const getDoc = (id, ...include) => cliJSON('docs', 'query', 'documents.get', '--id', id, ...include.flatMap((i) => ['--include', i]));
const lockOf = (id) => getDoc(id, 'lock').lock || null;
function totalOf(...filters) {
  if (!CLI_DOCS) throw skip('E2E_CLI_DOCS not set');
  const [bin, ...pre] = CLI_DOCS.split(' ');
  const out = execFileSync(
    'sh',
    ['-c', '"$@" 2>&1 >/dev/null', 'sh', bin, ...pre, 'docs', 'query', 'documents.list', ...filters.flatMap((f) => ['--filter', f]), '--count', '--limit', '1', '-o', 'json'],
    { encoding: 'utf8' }
  );
  const m = /(\d+) total/.exec(out);
  return m ? Number(m[1]) : 0;
}

// Creates a document through the service CLI, without a folder (the server generates the location).
function newDoc(name, { title, visibility = 'internal', topics = [], clients = [], projects = [], requirements = [], tasks = [], body = '' }) {
  const jiku = [
    clients.length ? `  clients: [${clients.join(', ')}]` : null,
    projects.length ? `  projects: [${projects.join(', ')}]` : null,
    requirements.length ? `  requirements: [${requirements.join(', ')}]` : null,
    tasks.length ? `  tasks: [${tasks.join(', ')}]` : null,
  ].filter(Boolean);
  const md = [
    '---',
    `title: ${title}`,
    `visibility: ${visibility}`,
    ...(topics.length ? [`topics: [${topics.join(', ')}]`] : []),
    ...(jiku.length ? ['jiku:', ...jiku] : []),
    '---',
    '',
    `# ${title}`,
    '',
    body,
    '',
  ].join('\n');
  const f = path.join(OUT, `${name}.md`);
  fs.writeFileSync(f, md);
  const doc = cliJSON('docs', 'new', f);
  created.push(doc.id);
  return { ...doc, title };
}

// Watches dialogs and JS errors: an XSS that runs triggers them.
function watch(page) {
  const w = { dialogs: [], errors: [] };
  page.on('dialog', async (d) => {
    w.dialogs.push(d.message());
    await d.dismiss().catch(() => {});
  });
  page.on('pageerror', (e) => w.errors.push(e.message));
  return w;
}

// page.goto may give net::ERR_ABORTED if a Next router navigation (router.replace from an
// effect, a Link) starts at the same time: it retries once if the page did not end up on the requested URL.
async function go(page, url) {
  try {
    return await page.goto(url);
  } catch (e) {
    if (!/ERR_ABORTED/.test(e.message)) throw e;
    await page.waitForTimeout(1500);
    if (page.url() === url) return null;
    return page.goto(url);
  }
}

function docker(...a) {
  return execFileSync('docker', a, { encoding: 'utf8' }).trim();
}
// SQL against the local Jiku database (only to remove and give back test permissions).
const JIKU_DB = process.env.E2E_JIKU_DB_CONTAINER || 'jiku-local-database';
function jikuSQL(sql) {
  return docker('exec', JIKU_DB, 'sh', '-c', `psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc "${sql.replace(/"/g, '\\"')}"`).split('\n')[0];
}
async function waitModule(ctx, base, apiPath, want, timeoutMs = 150000) {
  const t0 = Date.now();
  while (Date.now() - t0 < timeoutMs) {
    const r = await ctx.request.get(base + apiPath).catch(() => null);
    if (r && r.ok()) {
      const mods = await r.json().catch(() => []);
      const docs = Array.isArray(mods) ? mods.find((m) => m.module === 'docs') : null;
      if (docs?.status === want) return Date.now() - t0;
    }
    await new Promise((res) => setTimeout(res, 3000));
  }
  throw new Error(`the module did not become ${want} in ${timeoutMs / 1000} s`);
}

// ------------------------------------------------------------------------------------------
// Data of the run (through the service CLI)
// ------------------------------------------------------------------------------------------
// R: restricted, related to the project, to requirements 54 (internal) and 15 (public) and to the
// task; with its own topic and word and an attachment. Nobody but admin and services sees it.
// SA and SB: internal without a project; SA names the project in its title ("Asignar sugerido", assign suggested), SB does not.
const F = { R: null, RTitle: `${PREFIX} restringido ${TAG}`, RTopic: `secreto${TAG}`, RWord: `zarzamora${TAG}`, RAtt: null };
function setupData() {
  if (!CLI_DOCS) return 'E2E_CLI_DOCS not set: no data for the run';
  const r = newDoc('restricted', {
    title: F.RTitle,
    visibility: 'restricted',
    topics: [F.RTopic],
    clients: [D.client],
    projects: [D.project],
    requirements: [D.requirement, D.publicRequirement],
    tasks: [D.task],
    body: `Contenido que solo ve la conducción: ${F.RWord}.`,
  });
  F.R = r.id;
  const png = path.join(OUT, 'restricted.png');
  fs.writeFileSync(png, Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAEklEQVR4nGP4z8CAFWEXHbQSACj/P8Fu7N9hAAAAAElFTkSuQmCC', 'base64'));
  F.RAtt = cliJSON('docs', 'attach', r.id, png).id;
  F.SA = newDoc('no-project-suggested', {
    title: `${PREFIX} ${D.projectWord} ${TAG}`,
    body: `Notas sueltas de la ${D.projectWord}, todavía sin proyecto.`,
  });
  F.SB = newDoc('no-project', { title: `${PREFIX} huerfano ${TAG}`, body: 'Texto sin pistas de ningún proyecto.' });
  return `R=${F.R} (${r.path}), SA=${F.SA.id}, SB=${F.SB.id}, tag ${TAG}`;
}

function cleanup() {
  if (keep || !CLI_DOCS) return;
  for (const id of created) {
    try {
      const d = getDoc(id);
      cli('docs', 'rm', id, '--base-version', String(d.version));
    } catch (e) {
      log(`cleanup: could not delete ${id}: ${e.message.split('\n')[0]}`);
    }
  }
}

// ------------------------------------------------------------------------------------------
// opus-web, external user
// ------------------------------------------------------------------------------------------
async function opusSuite(browser) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();
  failPage = page;
  const w = watch(page);
  let logged = false;

  await step('O1', 'opus-web: login of the external person', async () => {
    await login(page, OPUS, 'EXTERNAL', creds);
    logged = true;
    const s = await (await ctx.request.get(OPUS + '/api/auth/session')).json();
    assert(s?.user?.roles?.includes('external-user'), `roles of the session: ${JSON.stringify(s?.user?.roles)}`);
    const m = await (await ctx.request.get(OPUS + '/api/opus/modules')).json();
    const docs = m.find((x) => x.module === 'docs');
    assert(docs?.status === 'available', `GET /opus/modules: ${JSON.stringify(m)}`);
    return `docs=${docs.status}, sharing=${docs.features?.sharing}`;
  });
  if (!logged) return ctx.close();

  await step('O2', 'opus-web: project "Documentación (n)" with public ones only (no internal or restricted)', async () => {
    const docsResp = page.waitForResponse((r) => r.url().includes('/api/opus/docs/documents?'), { timeout: 20000 });
    await page.goto(`${OPUS}/projects/${D.project}/requirements`);
    const r = await docsResp;
    assert(r.status() === 200, `GET /api/opus/docs/documents → ${r.status()} ${(await r.text()).slice(0, 200)}`);
    const body = await r.json().catch(() => ({}));
    const section = page.locator('section[aria-label="Documentación"]');
    await section.locator('a').first().waitFor({ timeout: 15000 });
    const text = await section.innerText();
    await shot(page, 'opus-board');
    const titles = await section.locator('a').allInnerTexts();
    const m = /Documentación \((\d+)\+?\)/.exec(text);
    assert(m && Number(m[1]) === titles.length, `heading "${text.split('\n')[0]}" vs ${titles.length} rows`);
    assert(titles.includes(D.arqTitle) && titles.includes(D.aduanaTitle), `public ones missing: ${titles}`);
    assert(!titles.includes(D.infraTitle), `"${D.infraTitle}" shows up (internal)`);
    assert(!titles.includes(D.internalTitle), `an internal document shows up`);
    assert(!titles.includes(F.RTitle), `the restricted document shows up`);
    const raw = JSON.stringify(body);
    assert(!F.R || !raw.includes(F.R), 'the api response carries the id of the restricted one');
    const vis = [...new Set((body.items || []).map((i) => i.visibility))];
    return `${text.split('\n')[0]}: ${titles.join(' | ')}; visibilities in the response: ${vis.join(',') || '—'}`;
  });

  await step('O3', 'opus-web: section in the detail of the public requirement (without the related restricted one)', async () => {
    const reqTitle = 'Revisión de grabaciones para despliegue productivo';
    await page.getByText(reqTitle, { exact: false }).first().click();
    const resp = await page.waitForResponse(
      (r) => r.url().includes(`/api/opus/docs/documents?`) && r.url().includes(`requirementId=${D.publicRequirement}`),
      { timeout: 20000 }
    );
    assert(resp.status() === 200, `GET requirementId → ${resp.status()}`);
    // The detail is not a role=dialog: it is the second "Documentación" section of the page.
    const section = page.locator('section[aria-label="Documentación"]').nth(1);
    await section.locator('a').first().waitFor({ timeout: 15000 });
    const titles = await section.locator('a').allInnerTexts();
    await shot(page, 'opus-requirement-detail');
    assert(titles.length === 1 && titles[0] === D.aduanaTitle, `documents of the requirement: ${titles}`);
    await page.keyboard.press('Escape');
    // Requirement 54 is internal: it must not be on the external person's board.
    const board = await page.locator('main').innerText();
    assert(!board.includes('Salida a producción'), 'the internal requirement 54 shows up in the portal');
    return `requirement ${D.publicRequirement}: ${titles.join(', ')} (the related restricted one does not show up); requirement ${D.requirement} (internal) hidden`;
  });

  await step('O4', 'opus-web: viewer of the public document (safe markdown, image, no editing)', async () => {
    const imgResp = page.waitForResponse((r) => r.url().includes(`/attachments/${D.arqAttachment}`), { timeout: 20000 });
    const docResp = page.waitForResponse((r) => r.url().endsWith(`/api/opus/docs/documents/${D.arq}`), { timeout: 20000 });
    await page.goto(`${OPUS}/projects/${D.project}/requirements`);
    await page.locator('section[aria-label="Documentación"] a', { hasText: D.arqTitle }).click();
    await page.waitForURL(new RegExp(`/projects/${D.project}/docs/${D.arq}`));
    await page.getByRole('heading', { name: D.arqTitle }).first().waitFor();
    const r = await imgResp;
    assert(r.status() === 302, `image: ${r.status()} (expected 302 to the presigned URL)`);
    await page.waitForTimeout(1500);
    const main = page.locator('main');
    const img = await main.locator(`img[src*="${D.arqAttachment}"]`).evaluate((i) => i.naturalWidth);
    assert(img > 0, 'the attachment image did not load');
    const html = await main.innerHTML();
    assert(!/<script/i.test(html) && !/onerror=/i.test(html), 'the raw HTML of the markdown reached the DOM');
    assert(w.dialogs.length === 0, `JS of the document ran: ${w.dialogs}`);
    const text = await main.innerText();
    assert(!/^---$/m.test(text) && !text.includes('requirements: ['), 'the frontmatter is shown');
    for (const label of ['Editar', 'Compartir', 'Guardar']) {
      assert((await page.getByRole('button', { name: label }).count()) === 0, `there is a "${label}" button`);
    }
    // D-11: the document arrives without frontmatter (network response, not only what is painted).
    const dr = await docResp;
    const doc = await dr.json();
    assert(dr.status() === 200 && typeof doc.content === 'string', `GET document: ${dr.status()}`);
    assert(!doc.content.trimStart().startsWith('---') && !/requirements:|projects:/.test(doc.content), 'the response carries the frontmatter');
    // P-1: an external person receives no clients, requirements or tasks (internal ids).
    const leaked = ['clients', 'requirements', 'tasks'].filter((k) => Array.isArray(doc[k]) && doc[k].length > 0);
    assert(leaked.length === 0, `the response carries ${leaked.join(', ')} to the external person`);
    // S-090: a link to another document (`/docs/{id}` of the internal web) shows as text.
    const hrefs = await main.locator('a').evaluateAll((as) => as.map((a) => a.getAttribute('href') || ''));
    const toDocs = hrefs.filter((h) => /\/docs\/[0-9A-Z]{26}([?#]|$)/.test(h));
    assert(toDocs.length === 0, `links to documents as <a>: ${toDocs.join(', ')}`);
    const linkText = /\[([^\]]+)\]\([^)]*\/docs\/[0-9A-Z]{26}\)/.exec(doc.content)?.[1];
    if (linkText) {
      assert(text.includes(linkText), `the link text "${linkText}" does not appear`);
      assert((await main.locator('a', { hasText: linkText }).count()) === 0, `"${linkText}" is still an <a>`);
    }
    await shot(page, 'opus-document-1440');
    return (
      `image 302 → ${r.headers().location?.split('?')[0]}; no JS; no editing; response without frontmatter (${doc.content.length} B)` +
      (linkText ? `; link "${linkText}" as text` : '; NOTE: the document has no links to other documents')
    );
  });

  await step('O5', 'opus-web: attachment download through /opus/docs/documents/{doc}/attachments/{id}', async () => {
    const link = page.locator('a', { hasText: 'diagrama.png' });
    const href = await link.getAttribute('href');
    assert(href === `/api/docs/documents/${D.arq}/attachments/${D.arqAttachment}`, `href: ${href}`);
    const [popup] = await Promise.all([ctx.waitForEvent('page'), link.click()]);
    await popup.waitForLoadState().catch(() => {});
    const popupUrl = popup.url();
    await popup.close();
    const r302 = await ctx.request.get(OPUS + href, { maxRedirects: 0 });
    assert(r302.status() === 302, `proxy: ${r302.status()}`);
    const loc = r302.headers().location;
    const file = await ctx.request.get(loc);
    assert(file.status() === 200, `storage: ${file.status()}`);
    const body = await file.body();
    assert(body.slice(1, 4).toString() === 'PNG', 'the file is not the PNG');
    // Attachment of another document hung from a public document, of an internal one and of a restricted one.
    const cross = await ctx.request.get(`${OPUS}/api/docs/documents/${D.aduana}/attachments/${D.arqAttachment}`, { maxRedirects: 0 });
    const priv = await ctx.request.get(`${OPUS}/api/docs/documents/${D.arq}/attachments/${D.privAttachment}`, { maxRedirects: 0 });
    assert(cross.status() === 404, `attachment of another document: ${cross.status()}`);
    assert(priv.status() === 404, `attachment of an internal document: ${priv.status()}`);
    let restr = 'no restricted';
    if (F.R) {
      const rr = await ctx.request.get(`${OPUS}/api/docs/documents/${F.R}/attachments/${F.RAtt}`, { maxRedirects: 0 });
      assert(rr.status() === 404, `attachment of the restricted one: ${rr.status()}`);
      restr = 'restricted 404';
    }
    return `302 → storage 200 (${body.length} B; the new tab ended up on ${popupUrl.split('?')[0]}); cross 404; internal 404; ${restr}`;
  });

  await step('O6', 'opus-web: internal, restricted and other-project ones do not open by direct URL', async () => {
    const out = [];
    const cases = [
      [D.internal, 'internal'],
      [D.infra, `${D.infraTitle} (internal)`],
      ...(F.R ? [[F.R, 'restricted of the project']] : []),
    ];
    for (const [id, label] of cases) {
      const api = await ctx.request.get(`${OPUS}/api/opus/docs/documents/${id}`);
      assert(api.status() === 404, `${label}: api ${api.status()}`);
      await page.goto(`${OPUS}/projects/${D.project}/docs/${id}`);
      await page.getByText('Este documento no existe o no está compartido con vos.').waitFor({ timeout: 15000 });
      out.push(`${label} → 404 + message`);
    }
    await shot(page, 'opus-document-not-visible');
    const other = await ctx.request.get(`${OPUS}/api/opus/docs/documents/${D.otherProjectDoc}`);
    out.push(`of another project → ${other.status()}`);
    assert(other.status() === 404, out.join('; '));
    return out.join('; ');
  });

  await step('O7', 'opus-web: responsive document (390 px)', async () => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(`${OPUS}/projects/${D.project}/docs/${D.arq}`);
    await page.getByRole('heading', { name: D.arqTitle }).first().waitFor();
    await page.waitForTimeout(1000);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    await shot(page, 'opus-document-390');
    await page.goto(`${OPUS}/projects/${D.project}/requirements`);
    await page.locator('section[aria-label="Documentación"] a').first().waitFor({ timeout: 15000 });
    const overflow2 = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    await shot(page, 'opus-board-390');
    await page.setViewportSize({ width: 1440, height: 900 });
    assert(overflow <= 0, `horizontal scroll of ${overflow}px in the document`);
    return `no horizontal scroll (document ${overflow}, board ${overflow2})`;
  });

  if (withDown) {
    await step('O8', 'opus-web: module down → notice; comes back when brought up', async () => {
      docker('stop', DOCS_CONTAINER);
      try {
        const ms = await waitModule(ctx, OPUS, '/api/opus/modules', 'unavailable');
        await page.goto(`${OPUS}/projects/${D.project}/requirements`);
        await page.getByText('Los documentos no están disponibles').first().waitFor({ timeout: 20000 });
        await shot(page, 'opus-module-down');
        await page.goto(`${OPUS}/projects/${D.project}/docs/${D.arq}`);
        await page.getByText(/no están disponibles/).first().waitFor({ timeout: 20000 });
        await shot(page, 'opus-document-module-down');
        return `unavailable after ${Math.round(ms / 1000)} s; notice on board and viewer`;
      } finally {
        docker('start', DOCS_CONTAINER);
        await waitModule(ctx, OPUS, '/api/opus/modules', 'available');
        await page.goto(`${OPUS}/projects/${D.project}/requirements`);
        await page.locator('section[aria-label="Documentación"] a').first().waitFor({ timeout: 30000 });
      }
    });
  }

  await step('O10', 'opus-web: without project-permissions → "sin acceso" (no access) notice (not "no disponible", unavailable)', async () => {
    const ext = await (await ctx.request.get(OPUS + '/api/auth/session')).json();
    const uid = process.env.E2E_EXTERNAL_USER_ID || ext?.user?.zitadelId || ext?.user?.sub || ext?.user?.id;
    if (!uid) throw skip(`the session does not expose the external person's id (set E2E_EXTERNAL_USER_ID): ${Object.keys(ext?.user || {})}`);
    const before = jikuSQL(`SELECT count(*) FROM user_project_permissions WHERE user_id='${uid}' AND project_id=${D.project}`);
    assert(before === '1', `the external person has no permission on ${D.project} (${before})`);
    jikuSQL(`DELETE FROM user_project_permissions WHERE user_id='${uid}' AND project_id=${D.project}`);
    try {
      const respP = page.waitForResponse((r) => r.url().includes('/api/opus/docs/documents?'), { timeout: 20000 });
      await page.goto(`${OPUS}/projects/${D.project}/requirements`);
      const resp = await respP.catch(() => null);
      const forbidden = page.getByText('No tenés acceso a la documentación de este proyecto.');
      await forbidden.first().waitFor({ timeout: 20000 });
      const unavailable = await page.getByText('Los documentos no están disponibles').count();
      await shot(page, 'opus-no-access');
      assert(unavailable === 0, 'the "no disponible" (unavailable) notice shows up too');
      return `GET documents → ${resp ? resp.status() : 'no request'}; no-access notice`;
    } finally {
      jikuSQL(`INSERT INTO user_project_permissions (user_id, project_id) VALUES ('${uid}', ${D.project}) ON CONFLICT DO NOTHING`);
      await page.goto(`${OPUS}/projects/${D.project}/requirements`);
      await page.locator('section[aria-label="Documentación"] a').first().waitFor({ timeout: 30000 });
    }
  });

  await step('O9', 'web: the external person does not get into the internal web', async () => {
    const c2 = await browser.newContext();
    const p2 = await c2.newPage();
    await login(p2, WEB, 'EXTERNAL', creds).catch(() => {});
    await p2.waitForTimeout(1500);
    const u = p2.url();
    const att = await c2.request.get(`${WEB}/docs/attachments/${D.arqAttachment}`, { maxRedirects: 0 });
    const doc = await c2.request.get(`${WEB}/docs/${D.arq}`, { maxRedirects: 0 });
    const anon = await browser.newContext();
    const anonAtt = await anon.request.get(`${WEB}/docs/attachments/${D.arqAttachment}`, { maxRedirects: 0 });
    await anon.close();
    await c2.close();
    assert(u.includes('/unauthorized'), `ended up on ${u}`);
    assert(att.status() === 403, `attachment through the web with an external session: ${att.status()}`);
    assert(anonAtt.status() >= 300 && anonAtt.status() < 400, `attachment without a session: ${anonAtt.status()}`);
    return `${u}; /docs/{id} → ${doc.status()} ${doc.headers().location || ''}; attachment with an external session → 403; without a session → ${anonAtt.status()} ${anonAtt.headers().location || ''}`;
  });

  await ctx.close();
}

// ------------------------------------------------------------------------------------------
// Public share link (created by the service CLI) opened in a browser without a session
// ------------------------------------------------------------------------------------------
async function publicSuite(browser) {
  await step('P1', 'public share link: page without a session, headers, no JS, attachment, revoke → 404', async () => {
    const share = cliJSON('docs', 'share', D.arq, '--expires', '1d', '--note', 'e2e browser');
    const url = share.url.replace(/^https?:\/\/[^/]+/, PUBLIC);
    const ctx = await browser.newContext();
    const page = await ctx.newPage();
    const w = watch(page);
    const att = [];
    let cspBlocked = '';
    page.on('requestfailed', (r) => {
      if (r.failure()?.errorText === 'csp') cspBlocked = new URL(r.url()).origin;
    });
    try {
      const r = await page.goto(url);
      assert(r.status() === 200, `GET /p/docs/{token} → ${r.status()}`);
      const h = r.headers();
      await page.waitForTimeout(1500);
      const html = await page.content();
      const text = await page.locator('body').innerText();
      await shot(page, 'public-link');
      assert(w.dialogs.length === 0, `JS ran: ${w.dialogs}`);
      assert(!/<script>alert/i.test(html), 'the <script> of the markdown reached the HTML');
      assert(!text.includes('requirements:'), 'the frontmatter is shown');
      const links = await page.locator('a[href*="/docs/"]').count();
      const hdr = ['content-security-policy', 'x-robots-tag', 'referrer-policy', 'cache-control'].map((k) => `${k}=${h[k] ? 'yes' : 'NO'}`);
      assert(h['content-security-policy'] && /noindex/.test(h['x-robots-tag'] || ''), hdr.join(' '));
      const img = await page.locator('img').first().evaluate((i) => i.naturalWidth).catch(() => 0);
      // The image points to /p/docs/{token}/attachments/{id}, which answers 302 to the presigned URL.
      const src = await page.locator('img').first().getAttribute('src').catch(() => null);
      if (src) {
        assert(/^\/p\/docs\/jds_[^/]+\/attachments\/[0-9A-Z]{26}$/.test(src), `src of the image: ${src.replace(/jds_[^/]+/, 'jds_…')}`);
        const a302 = await ctx.request.get(new URL(src, url).href, { maxRedirects: 0 });
        assert(a302.status() === 302, `attachment of the share link → ${a302.status()}`);
        att.push(302);
      }
      cli('docs', 'revoke', share.id);
      const r2 = await page.goto(url);
      assert(r2.status() === 404, `after revoking → ${r2.status()}`);
      return `200; ${hdr.join(', ')}; no JS; image ${img > 0 ? 'ok' : cspBlocked ? `302 but the CSP (img-src 'self' https:) blocks the storage ${cspBlocked} (http locally)` : 'does NOT load'} (${att.join(',') || 'no <img>'}); links to documents as <a>: ${links}; revoked → 404`;
    } finally {
      await ctx.close();
    }
  });

  await step('P2', 'public share link: a restricted one does not allow share links (sharing_blocked)', async () => {
    if (!F.R) throw skip('without the restricted document of the run');
    let err = '';
    try {
      cli('docs', 'share', F.R, '--expires', '1d');
    } catch (e) {
      err = String(e.stderr || e.message);
    }
    assert(/sharing_blocked/.test(err), `share of a restricted one: ${err.slice(0, 120) || 'IT CREATED THE SHARE LINK'}`);
    return 'sharing_blocked';
  });
}

// ------------------------------------------------------------------------------------------
// web, internal user (user role)
// ------------------------------------------------------------------------------------------
async function webSuite(browser) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();
  failPage = page;
  const w = watch(page);
  // The web talks to the api from Next Server Actions (POST to the page itself with the
  // `next-action` header), not with browser fetch: what can be observed is the body of the POST
  // (the serialized arguments) and the RSC response.
  const actions = [];
  page.on('request', (r) => {
    if (r.method() === 'POST' && r.headers()['next-action']) actions.push({ url: r.url(), body: r.postData() || '', req: r });
  });
  let logged = false;
  let me = { id: null, zitadelId: null, name: null };
  const all = ['W2', 'W3', 'W4', 'W5', 'W6', 'W7', 'W8', 'W9', 'W10', 'W11', 'W12', 'W13', 'W14', 'W15', 'W16'];

  // --- helpers of the /docs screen ----------------------------------------------------------
  const searchBox = () => page.getByRole('combobox', { name: 'Buscar en la documentación' });
  const activeTokens = () => page.getByRole('list', { name: 'Filtros activos' }).locator('li').allInnerTexts();
  const suggestions = () => page.getByRole('listbox', { name: 'Sugerencias' });
  // Waits for the results table to settle and returns the titles.
  const resultTitles = async (region = 'Documentos') => {
    const card = page.getByRole('region', { name: region, exact: true });
    let prev = null;
    for (let i = 0; i < 25; i++) {
      await page.waitForTimeout(600);
      const empty = await page.getByText(/Ningún documento cumple|Todos los documentos tienen proyecto|Todavía no hay documentos/).count();
      if (empty) return [];
      if (!(await card.count())) continue;
      const cur = (await card.locator('tbody tr a[href^="/docs/"]').allInnerTexts()).join('|');
      if (cur && cur === prev) break;
      prev = cur;
    }
    return card.locator('tbody tr a[href^="/docs/"]').allInnerTexts();
  };
  // Types in the search box and picks a suggestion (from a group, or the first that matches).
  const addFilter = async (text, optionName, group) => {
    await searchBox().fill(text);
    const scope = group ? suggestions().getByRole('group', { name: group }) : suggestions();
    const opt = scope.getByRole('option', { name: optionName }).first();
    await opt.waitFor({ timeout: 15000 });
    const before = page.url();
    await opt.click();
    await page.waitForURL((u) => u.href !== before, { timeout: 15000 });
  };
  // Opens a DS `Select` (role=combobox with a search box) and picks an option.
  const pick = async (scope, label, search, option) => {
    await scope.getByRole('combobox', { name: label, exact: true }).click();
    if (search) await scope.getByRole('textbox', { name: `Buscar en ${label}` }).fill(search);
    await scope.getByRole('option', { name: option }).first().click();
  };
  const optionsOf = async (scope, label) => {
    await scope.getByRole('combobox', { name: label, exact: true }).click();
    await page.waitForTimeout(400);
    const opts = await scope.getByRole('option').allInnerTexts();
    // Escape would also close the "Nuevo documento" dialog: it is closed with another click.
    await scope.getByRole('combobox', { name: label, exact: true }).click();
    return opts;
  };
  // Storage paths: the current English locations and the Spanish ones of data created before 1.0.1.
  const noPath = (text, where) => {
    const m = /\/(clients|clientes|general|infra|internal|interno|imported|importado)\/[^\s]*|[\w-]+\.md\b|Ruta de almacenamiento/.exec(text);
    assert(!m, `${where} shows the path: "${m?.[0]}"`);
  };

  await step('W1', 'web: login of the internal person, "Documentación" entry and /docs home without folders', async () => {
    await login(page, WEB, 'USER', creds);
    logged = true;
    const s = await (await ctx.request.get(WEB + '/api/auth/session')).json();
    me = { id: s?.user?.id, zitadelId: s?.user?.zitadelId, name: s?.user?.name || null };
    await page.getByRole('link', { name: 'Documentación' }).first().waitFor({ timeout: 30000 });
    await go(page, `${WEB}/docs`);
    await searchBox().waitFor({ timeout: 30000 });
    assert((await page.getByText(/no está disponible/).count()) === 0, 'module unavailable notice');
    assert((await page.locator('[aria-label="Contenido de la carpeta"]').count()) === 0, 'the folder table is still there');
    const groups = await page.locator('main').getByText(/^(En mis proyectos activos|Editados recientemente)$/).allInnerTexts();
    const titles = await resultTitles('Editados recientemente');
    const main = await page.locator('main').innerText();
    noPath(main, 'the home');
    for (const v of ['Vistas', 'Plantilla']) assert(!new RegExp(`\\b${v}\\b`).test(main), `"${v}" shows up (decisions 6 and 7)`);
    assert(!titles.includes(F.RTitle), 'the home shows the restricted one');
    await shot(page, 'web-docs-home');
    return `groups: ${groups.join(', ') || '—'}; ${titles.length} recent; no path, no views or templates`;
  });
  if (!logged) {
    const why = results.find((r) => r.id === 'W1')?.note || '';
    for (const id of all) results.push({ id, name: 'web (internal)', status: 'not tested', note: `no login of the internal user (W1: ${why})` });
    return ctx.close();
  }

  await step('W2', 'web: search box with filters as tokens (project, client, topic, text with Enter, "Sin proyecto"), no path, and pagination', async () => {
    const out = [];
    await go(page, `${WEB}/docs`);
    // Project
    await addFilter(D.projectWord, new RegExp(`^${D.projectName}`), 'Proyectos');
    assert(new URL(page.url()).searchParams.get('proyecto') === String(D.project), `URL: ${page.url()}`);
    let t = await resultTitles();
    let tokens = await activeTokens();
    assert(tokens.includes(`Proyecto ${D.projectName}`), `tokens: ${tokens}`);
    assert(t.includes(D.arqTitle) && !t.includes(F.RTitle), `project: ${t}`);
    const headProj = await page.getByRole('region', { name: 'Documentos', exact: true }).locator('thead').innerText();
    noPath(await page.locator('main').innerText(), 'the listing by project');
    await shot(page, 'web-search-project');
    out.push(`project → ${t.length} (${headProj.replace(/\s+/g, ' ').trim()})`);
    // Cliente
    await addFilter(D.clientName, new RegExp(`^${D.clientName}$`), 'Clientes');
    assert(new URL(page.url()).searchParams.get('cliente') === String(D.client), `URL: ${page.url()}`);
    t = await resultTitles();
    tokens = await activeTokens();
    assert(tokens.includes(`Cliente ${D.clientName}`), `tokens: ${tokens}`);
    assert(t.includes(D.arqTitle), `project+client: ${t}`);
    out.push(`+client → ${t.join(' | ')}`);
    // Tema
    await addFilter(D.arqTopic, new RegExp(`^${D.arqTopic}`, 'i'), 'Temas');
    t = await resultTitles();
    tokens = await activeTokens();
    assert(tokens.some((x) => /^Tema /i.test(x)), `tokens: ${tokens}`);
    assert(t.length >= 1 && t.includes(D.arqTitle), `+topic: ${t}`);
    await shot(page, 'web-search-three-tokens');
    out.push(`+topic → ${t.length}`);
    // Backspace with the field empty removes the last token.
    await searchBox().click();
    await searchBox().press('Backspace');
    await page.waitForTimeout(800);
    tokens = await activeTokens();
    assert(!tokens.some((x) => /^Tema /i.test(x)), `Backspace did not remove the topic: ${tokens}`);
    // Remove all
    await page.getByRole('button', { name: 'Quitar todos los filtros' }).click();
    await page.waitForURL((u) => !u.search.includes('proyecto='));
    // Text with Enter
    await searchBox().fill(TAG);
    await searchBox().press('Enter');
    await page.waitForURL((u) => u.searchParams.get('q') === TAG, { timeout: 15000 });
    t = await resultTitles();
    tokens = await activeTokens();
    assert(tokens.includes(`“${TAG}”`), `text token: ${tokens}`);
    assert(!F.SA || t.includes(F.SA.title), `text: ${t}`);
    assert(!t.includes(F.RTitle), 'the search returns the restricted one');
    const textMain = await page.locator('main').innerText();
    noPath(textMain, 'the text results');
    assert(!textMain.includes(F.RWord), 'the word of the restricted one shows up');
    await shot(page, 'web-search-text');
    out.push(`text "${TAG}" (Enter) → ${t.length}`);
    // "Sin proyecto" (no project; suggestion of the initial group "Sin …")
    await addFilter('sin', /^Sin proyecto$/);
    assert(new URL(page.url()).searchParams.get('sinProyecto') === '1', `URL: ${page.url()}`);
    await page.getByRole('heading', { name: 'Sin proyecto' }).first().waitFor();
    t = await resultTitles();
    assert(t.some((x) => x.includes(TAG)), `no project + text: ${t}`);
    await shot(page, 'web-search-no-project-text');
    out.push(`+Sin proyecto → ${t.length}`);
    // Pagination: "Sin proyecto" alone has more than 20 documents.
    await go(page, `${WEB}/docs?sinProyecto=1`);
    t = await resultTitles();
    const head = await page.getByRole('region', { name: 'Documentos', exact: true }).locator('thead th').first().innerText();
    const m = /(\d+)\s+de\s+(\d+\+?)/i.exec(head);
    assert(m && Number(m[1]) === t.length, `heading "${head}" with ${t.length} rows`);
    const nav = page.getByRole('navigation', { name: 'Paginación' });
    assert(await nav.count(), 'no pagination');
    noPath(await page.locator('main').innerText(), '"Sin proyecto"');
    await nav.getByRole('button', { name: 'Página siguiente' }).click();
    await page.waitForURL((u) => u.searchParams.get('pagina') === '2', { timeout: 15000 });
    const t2 = await resultTitles();
    assert(t2.length > 0 && t2.join('|') !== t.join('|'), 'page 2 is the same as page 1');
    await shot(page, 'web-search-page-2');
    out.push(`pagination: "${head.trim()}" → page 2 with ${t2.length}`);
    return out.join('; ');
  });

  // Document the person creates from "Nuevo documento" (new document).
  let newId = null;
  const newTitle = `${PREFIX} alta ${TAG}`;
  await step('W3', 'web: "Nuevo documento" without folder or template (client/project, free topics, visibility) and "Crear y editar" without reloading', async (bug) => {
    const out = [];
    await go(page, `${WEB}/docs`);
    await page.getByRole('button', { name: 'Nuevo documento' }).click();
    const dlg = page.getByRole('dialog', { name: 'Nuevo documento' });
    await dlg.waitFor();
    const text = await dlg.innerText();
    for (const v of ['Carpeta', 'Plantilla', 'Ruta', 'Ubicación']) assert(!new RegExp(`\\b${v}\\b`).test(text), `the creation form shows "${v}"`);
    const radios = await dlg.getByRole('radio').allInnerTexts();
    const checked = await dlg.getByRole('radio', { checked: true }).allInnerTexts();
    assert(checked.join() === 'Interno', `default visibility: ${checked}`);
    assert(!radios.includes('Restringido'), `"Restringido" (restricted) shows up for a user: ${radios}`);
    out.push(`visibility [${radios.join(' · ')}] (default ${checked})`);
    await dlg.getByLabel('Título').fill(newTitle);
    // Client and project: with the client chosen, the projects are filtered by that client.
    await pick(dlg, 'Cliente', D.clientName, new RegExp(`^${D.clientName}$`));
    const projOpts = await optionsOf(dlg, 'Proyecto');
    const filtered = projOpts.filter((o) => o !== 'Sin proyecto');
    if (filtered.length === 0) {
      await shot(page, 'web-create-project-empty');
      bug('JIKU-WEB-3', `client ${D.clientName} → Project options: ${projOpts.join(', ')}`);
      // The project is chosen without a client (so the creation still carries a project).
      await pick(dlg, 'Cliente', null, /^Sin cliente$/);
    } else {
      assert(filtered.some((o) => o.startsWith(D.projectName)), `projects of ${D.clientName}: ${filtered.slice(0, 5)}`);
    }
    await pick(dlg, 'Proyecto', D.projectWord, new RegExp(`^${D.projectName}`));
    const combos = await dlg.getByRole('combobox').allInnerTexts();
    out.push(`client/project: ${combos.filter(Boolean).join(' / ')}`);
    if (!combos.includes(D.clientName) && filtered.length === 0) out.push('the project did not fill in the client');
    // Free topics: a new one with Enter and an existing one from the suggestion.
    const topics = dlg.getByRole('combobox', { name: 'Temas' });
    await topics.fill(`${PREFIX}-tema`);
    await topics.press('Enter');
    await topics.fill(D.arqTopic.slice(0, 5));
    const sug = dlg.getByRole('listbox', { name: 'Temas existentes' }).getByRole('option', { name: new RegExp(`^${D.arqTopic}`, 'i') }).first();
    await sug.waitFor({ timeout: 10000 });
    await sug.click();
    const chips = await dlg.getByRole('list', { name: 'Temas elegidos' }).innerText();
    assert(/e2e-p1-tema/i.test(chips) && new RegExp(D.arqTopic, 'i').test(chips), `chosen topics: ${chips}`);
    await shot(page, 'web-create-form');
    // "Crear y editar" enters editing without reloading the page (JIKU-WEB-2).
    await page.evaluate(() => (window.__e2eSinRecarga = 1));
    await dlg.getByRole('button', { name: 'Crear y editar' }).click();
    await page.waitForURL(/\/docs\/[0-9A-Z]{26}/, { timeout: 20000 });
    newId = page.url().match(/\/docs\/([0-9A-Z]{26})/)[1];
    created.push(newId);
    const editing = page.getByText('Estás editando.', { exact: false });
    const ok = await editing.waitFor({ timeout: 20000 }).then(() => true, () => false);
    const sameDoc = await page.evaluate(() => window.__e2eSinRecarga === 1);
    if (!ok) {
      await shot(page, 'web-create-no-editing');
      bug('JIKU-WEB-2', `${page.url().replace(WEB, '')}, lock ${lockOf(newId) ? 'taken' : 'not requested'}`);
      await go(page, `${WEB}/docs/${newId}?editar=1`);
      await editing.waitFor({ timeout: 20000 });
    } else {
      assert(sameDoc, 'it entered editing but with a full reload');
      await page.waitForFunction(() => !location.search.includes('editar=1'), null, { timeout: 10000 }).catch(() => {});
    }
    await shot(page, 'web-create-editing');
    const got = getDoc(newId, 'content', 'topics');
    assert(got.visibility === 'internal', `created visibility: ${got.visibility}`);
    assert((got.projects || []).includes(D.project), `projects: ${JSON.stringify(got.projects)}`);
    assert((got.topics || []).length >= 2, `topics: ${JSON.stringify(got.topics)}`);
    assert(lockOf(newId)?.sessionId, 'the document was not locked on entering editing');
    out.push(`id ${newId} → ${got.path} (generated location); topics ${JSON.stringify(got.topics)}; clients ${JSON.stringify(got.clients)}; editing without a reload, URL ${page.url().replace(WEB, '')}`);
    return out.join('; ');
  });

  await step('W4', 'web: editing by fields, "Mensaje de la versión" (version message) and own name on the lock', async (bug) => {
    assert(newId, 'no document from W3');
    const out = [];
    const editor = page.locator('main');
    const labels = ['Título', 'Markdown', 'Mensaje de la versión (opcional)'];
    for (const l of labels) assert(await editor.getByLabel(l, { exact: true }).count(), `the "${l}" field is missing`);
    assert(await editor.getByRole('combobox', { name: 'Temas' }).count(), 'the Temas field is missing');
    assert(await editor.getByRole('combobox', { name: 'Cliente', exact: true }).count(), 'Cliente is missing');
    assert(await editor.getByRole('combobox', { name: 'Proyecto', exact: true }).count(), 'Proyecto is missing');
    const radios = await editor.getByRole('radiogroup', { name: 'Visibilidad' }).getByRole('radio').allInnerTexts();
    assert(!radios.includes('Restringido'), `"Restringido" in a user's editing: ${radios}`);
    const ta = editor.getByLabel('Markdown', { exact: true });
    assert(!/^---/.test(await ta.inputValue()), 'the editor shows the frontmatter in the body');
    // The lock with the person's own name: the service sees it with holderName; in another tab the web says so.
    const lock = lockOf(newId);
    assert(lock?.sessionId, 'no lock while editing');
    if (!lock.holderName) bug('JIKU-WEB-5', `lock.holderName ${JSON.stringify(lock.holderName)}`);
    const tab = await ctx.newPage();
    await tab.goto(`${WEB}/docs/${newId}`);
    await tab.getByRole('heading', { name: newTitle }).first().waitFor({ timeout: 20000 });
    const notice = tab.getByText(/lo está editando|Lo estás editando/).first();
    await notice.waitFor({ timeout: 15000 });
    const noticeText = await notice.innerText();
    await shot(tab, 'web-lock-other-tab');
    // The listing row announces who is editing it (screen-reader text). Only the listing
    // mode carries the lock (the text search does not).
    await tab.goto(`${WEB}/docs?proyecto=${D.project}`);
    await tab.getByRole('region', { name: 'Documentos', exact: true }).waitFor({ timeout: 20000 });
    await tab.waitForTimeout(1500);
    const rowSr = await tab.getByText(/lo está editando$/).allInnerTexts();
    await tab.close();
    if (/otra persona/i.test(noticeText) || rowSr.some((x) => /otra persona/i.test(x))) {
      bug('JIKU-WEB-5', `other tab: "${noticeText.slice(0, 50)}…"; row: "${rowSr.join(' / ')}"`);
    }
    if (!/Lo estás editando en otra pestaña/.test(noticeText)) bug('JIKU-WEB-4', `other tab: "${noticeText.slice(0, 60)}…"`);
    out.push(`lock: holderName "${lock.holderName}"; other tab: "${noticeText.slice(0, 70)}"; row: ${rowSr.join(' / ') || '—'}`);
    // Edit fields and body, and save with a message.
    await editor.getByRole('combobox', { name: 'Temas' }).fill(`${PREFIX}-editado`);
    await editor.getByRole('combobox', { name: 'Temas' }).press('Enter');
    await ta.fill((await ta.inputValue()) + `\nPrimera edición desde el navegador ${TAG}.\n`);
    await editor.getByLabel('Mensaje de la versión (opcional)').fill(`${PREFIX} versión con mensaje`);
    await shot(page, 'web-editing');
    const n0 = actions.length;
    await page.getByRole('button', { name: 'Guardar', exact: true }).click();
    const toast = page.getByText(/Guardaste la versión \d+/).first();
    await toast.waitFor({ timeout: 20000 });
    const toastText = await toast.innerText();
    await page.getByText('Estás editando.', { exact: false }).waitFor({ state: 'detached', timeout: 20000 });
    const save = actions.slice(n0).find((a) => a.body.includes(newId) && a.body.includes('baseVersion'));
    assert(save && /"baseVersion":1\b/.test(save.body) && save.body.includes(lock.sessionId), 'the save does not carry baseVersion=1 and the sessionId of the lock');
    await page.waitForTimeout(1000);
    const got = getDoc(newId, 'lock', 'content');
    assert(!got.lock, `the lock is still taken: ${JSON.stringify(got.lock)}`);
    assert(got.version === 2 && got.content.includes(`Primera edición desde el navegador ${TAG}.`), `version ${got.version}`);
    const topicsNow = getDoc(newId, 'topics').topics || [];
    assert(topicsNow.some((x) => /editado/.test(x)), `topics: ${topicsNow}`);
    const hist = cliJSON('docs', 'history', newId);
    const v2 = (hist.items || hist).find((v) => v.version === 2);
    assert(v2?.message === `${PREFIX} versión con mensaje`, `message of version 2: ${JSON.stringify(v2?.message)}`);
    const meta = await page.getByText(/^Versión \d+ · actualizado/).first().innerText();
    if (!/por vos$/.test(meta)) bug('JIKU-WEB-4', `header: "${meta}"`);
    await shot(page, 'web-saved');
    if (!got.updatedByName) bug('JIKU-WEB-5', `updatedByName ${JSON.stringify(got.updatedByName)}`);
    out.push(`"${toastText}"; version 2 with message and topics ${JSON.stringify(topicsNow)}; lock released; header "${meta}"`);
    return out.join('; ');
  });

  await step('W5', 'web: reading without frontmatter, breadcrumb, Propiedades (properties) without path, history ("Ver" view and "Restaurar" restore)', async (bug) => {
    assert(newId, 'no document from W3');
    const out = [];
    await go(page, `${WEB}/docs/${newId}`);
    await page.getByRole('heading', { name: newTitle }).first().waitFor({ timeout: 20000 });
    const main = page.locator('main');
    const text = await main.innerText();
    if (/^---$/m.test(text) || /^(title|visibility|topics|jiku):/m.test(text)) bug('JIKU-WEB-1', `document ${newId}`);
    noPath(text, 'the document');
    const crumb = page.getByRole('navigation', { name: 'Ruta' }).first();
    assert(await crumb.getByRole('link', { name: 'Documentación' }).count(), 'no breadcrumb to Documentación');
    const props = await main.getByRole('heading', { name: 'Propiedades' }).locator('xpath=ancestor::*[.//dl][1]').innerText();
    assert(/Visibilidad/.test(props) && /Versión/.test(props) && !/Ruta/.test(props), `Propiedades: ${props.replace(/\s+/g, ' ')}`);
    const identity = await main.getByText(new RegExp(`${D.projectName}`)).first().innerText().catch(() => '');
    out.push(`breadcrumb "${(await crumb.innerText()).replace(/\s+/g, ' ')}"; identity "${identity}"; Propiedades: ${props.replace(/\s+/g, ' ')}`);
    await shot(page, 'web-reading');
    // History: two versions, version 2 with a message and protected.
    await page.getByRole('button', { name: 'Historial de versiones' }).click();
    const panel = page.getByRole('dialog', { name: 'Historial de versiones' });
    await panel.getByText('Versión 1', { exact: true }).waitFor({ timeout: 15000 });
    const entries = await panel.locator('li').allInnerTexts();
    assert(entries.length >= 2, `versions: ${entries.length}`);
    const v2 = entries.find((e) => e.startsWith('Versión 2')) || '';
    assert(/Actual/i.test(v2) && /Protegida/i.test(v2) && v2.includes(`${PREFIX} versión con mensaje`), `version 2: ${v2.replace(/\s+/g, ' ')}`);
    assert(!entries.some((e) => /Otra persona/.test(e)), `author "Otra persona" in the history: ${entries.map((e) => e.replace(/\s+/g, ' ')).join(' / ')}`);
    if (entries.some((e) => /^—/m.test(e))) bug('JIKU-WEB-5', 'author "—" in the history');
    await shot(page, 'web-history');
    out.push(`history: ${entries.map((e) => e.split('\n').slice(0, 3).join(' ')).join(' / ')}`);
    // View version 1
    const v1 = panel.locator('li', { hasText: /^Versión 1/ });
    await v1.getByRole('button', { name: 'Ver' }).click();
    await page.waitForURL(/\?version=1/, { timeout: 15000 });
    await page.getByText(/Estás viendo la versión 1/).waitFor({ timeout: 15000 });
    const v1Text = await main.innerText();
    assert(!v1Text.includes(`Primera edición desde el navegador ${TAG}`), 'version 1 shows the text of version 2');
    await shot(page, 'web-history-view-v1');
    // Restore version 1
    await page.getByRole('button', { name: 'Historial de versiones' }).click();
    await panel.locator('li', { hasText: /^Versión 1/ }).getByRole('button', { name: 'Restaurar' }).click();
    const confirm = page.getByRole('dialog', { name: 'Restaurar la versión 1' });
    await confirm.waitFor({ timeout: 10000 });
    await confirm.getByRole('button', { name: 'Restaurar' }).click();
    const toast = page.getByText(/Restauraste la versión 1 como versión \d+/).first();
    await toast.waitFor({ timeout: 20000 });
    const toastText = await toast.innerText();
    await page.waitForTimeout(1000);
    const got = getDoc(newId, 'content');
    assert(got.version === 3 && !got.content.includes(`Primera edición desde el navegador ${TAG}`), `after restoring: version ${got.version}`);
    await shot(page, 'web-restored');
    out.push(`"Ver" v1 with a notice; "${toastText}"`);
    return out.join('; ');
  });

  await step('W6', 'web: sharing (create a share link, open the public page, revoke → 404)', async () => {
    assert(newId, 'no document from W3');
    const anon = await browser.newContext();
    try {
      await go(page, `${WEB}/docs/${newId}`);
      await page.getByRole('heading', { name: newTitle }).first().waitFor();
      const btn = page.getByRole('button', { name: 'Compartir' });
      await btn.click();
      const dlg = page.getByRole('dialog', { name: new RegExp(`^Compartir «${newTitle}»`) });
      await dlg.waitFor();
      const head = await dlg.innerText();
      noPath(head, 'the share dialog');
      await shot(page, 'web-share-dialog');
      await dlg.getByRole('button', { name: 'Crear link' }).click();
      const link = dlg.getByRole('textbox', { name: 'Link', exact: true });
      await link.waitFor({ timeout: 15000 });
      const url = await link.inputValue();
      assert(/\/p\/docs\/jds_/.test(url), `link: ${url.slice(0, 40)}`);
      await shot(page, 'web-share-created', { mask: [link, page.getByText(creds.WEB_USER_LOGIN)] });
      const pub = await anon.newPage();
      const r = await pub.goto(url.replace(/^https?:\/\/[^/]+/, PUBLIC));
      assert(r.status() === 200, `public page: ${r.status()}`);
      const pubText = await pub.locator('body').innerText();
      assert(pubText.includes(newTitle) && !/^visibility:/m.test(pubText), 'the public page does not show the document or shows the frontmatter');
      await shot(pub, 'web-public-link');
      await dlg.getByRole('button', { name: 'Cerrar', exact: true }).click();
      await btn.click();
      const rows = dlg.locator('[aria-label="Links de este documento"] tbody tr');
      await rows.first().waitFor();
      const n = await rows.count();
      assert((await dlg.getByRole('textbox', { name: 'Link', exact: true }).count()) === 0, 'the share link is shown again');
      await shot(page, 'web-share-list');
      await dlg.getByRole('button', { name: 'Revocar' }).first().click();
      await page.getByRole('dialog', { name: 'Revocar link' }).getByRole('button', { name: 'Revocar' }).click();
      await page.waitForTimeout(1500);
      const r2 = await pub.goto(url.replace(/^https?:\/\/[^/]+/, PUBLIC));
      assert(r2.status() === 404, `after revoking: ${r2.status()}`);
      await page.keyboard.press('Escape');
      return `share link of an internal one created by a user → 200; ${n} share link(s) listed; revoked → 404`;
    } finally {
      await anon.close();
    }
  });

  await step('W7', 'web: "Sin proyecto": "Asignar sugerido" and "Asignar proyecto" only add the relation', async () => {
    if (!F.SA) throw skip('without the documents of the run (E2E_CLI_DOCS)');
    const out = [];
    await go(page, `${WEB}/docs?sinProyecto=1&q=${encodeURIComponent(TAG)}`);
    await page.getByRole('heading', { name: 'Sin proyecto' }).first().waitFor();
    const titles = await resultTitles();
    assert(titles.includes(F.SA.title) && titles.includes(F.SB.title), `results: ${titles}`);
    const head = await page.getByRole('region', { name: 'Documentos', exact: true }).locator('thead').innerText();
    assert(/Acción/i.test(head) && !/Ruta/i.test(head), `columns: ${head.replace(/\s+/g, ' ')}`);
    const rowA = page.locator('tbody tr', { hasText: `${PREFIX} ${D.projectWord} ${TAG}` });
    const rowB = page.locator('tbody tr', { hasText: `${PREFIX} huerfano ${TAG}` });
    await rowA.getByText(/^Sugerido: /).waitFor({ timeout: 20000 });
    const sugText = await rowA.getByText(/^Sugerido: /).innerText();
    assert(sugText.includes(D.projectName), `suggestion for SA: ${sugText}`);
    await shot(page, 'web-no-project');
    const pathA = getDoc(F.SA.id).path;
    await rowA.getByRole('button', { name: 'Asignar sugerido' }).click();
    const toastA = page.getByText(/^Asignaste «/).first();
    await toastA.waitFor({ timeout: 20000 });
    out.push(`"${await toastA.innerText()}"`);
    const a = getDoc(F.SA.id);
    assert(a.projects.includes(D.project) && a.path === pathA, `SA after assigning: projects ${a.projects}, path ${a.path} (before ${pathA})`);
    // SB: no suggestion → "Asignar proyecto" opens the selector in the row.
    const pathB = getDoc(F.SB.id).path;
    const assignB = rowB.getByRole('button', { name: 'Asignar proyecto' });
    await assignB.waitFor({ timeout: 20000 });
    await assignB.click();
    const search = rowB.getByRole('textbox', { name: /^Buscar en / });
    await search.waitFor({ timeout: 10000 });
    await search.fill(D.projectWord);
    await rowB.getByRole('option', { name: new RegExp(`^${D.projectName}`) }).first().click();
    const toastB = page.getByText(new RegExp(`^Asignaste «.*» a «${PREFIX} huerfano`)).first();
    await toastB.waitFor({ timeout: 20000 });
    out.push(`"${await toastB.innerText()}"`);
    const b = getDoc(F.SB.id, 'content');
    assert(b.projects.includes(D.project) && b.path === pathB, `SB after assigning: projects ${b.projects}, path ${b.path}`);
    assert(b.content.includes('Texto sin pistas de ningún proyecto.'), 'the body changed');
    const hist = cliJSON('docs', 'history', F.SB.id);
    const last = (hist.items || hist)[0];
    out.push(`version ${b.version} "${last?.message}"; paths unchanged`);
    await page.waitForTimeout(1500);
    const after = await resultTitles();
    await shot(page, 'web-no-project-assigned');
    out.push(`left in the list: ${after.filter((t) => t.includes(TAG)).length}`);
    return out.join('; ');
  });

  await step('W8', 'web: project card "Documentación (N)", preloaded "+", "Ver todos ›" (see all) and "Volver al proyecto" (back to the project); requirement and task cards without path', async (bug) => {
    const out = [];
    const card = page.locator('xpath=//ul[@aria-label="Documentos vinculados"]/ancestor::*[.//h2[starts-with(normalize-space(.),"Documentación")]][1]');
    const cardInfo = async () => {
      await page.locator('[aria-label="Documentos vinculados"]').waitFor({ timeout: 30000 });
      const h2 = (await card.locator('h2').first().innerText()).replace(/\s+/g, ' ').trim();
      const titles = await card.locator('[aria-label="Documentos vinculados"] a').allInnerTexts();
      const text = await card.innerText();
      return { h2, titles, text };
    };
    await go(page, `${WEB}/projects/${D.project}`);
    let c = await cardInfo();
    const n = Number(/(\d+)/.exec(c.h2)?.[1]);
    const expected = CLI_DOCS ? totalOf(`projectId=${D.project}`) - totalOf(`projectId=${D.project}`, 'visibility=restricted') : null;
    assert(expected === null || n === expected, `"${c.h2}": expected ${expected} (without restricted)`);
    assert(!c.titles.includes(F.RTitle), 'the project card shows the restricted one');
    noPath(c.text, 'the project card');
    const indirect = (c.text.match(/vinculado, sin proyecto propio/g) || []).length;
    await shot(page, 'web-project-card');
    out.push(`project: "${c.h2}" (expected ${expected ?? '?'}), ${c.titles.length} rows, ${indirect} "vinculado, sin proyecto propio" (related, no own project)`);
    // "+" with the project already chosen
    await card.getByRole('button', { name: 'Nuevo documento en este proyecto' }).click();
    const dlg = page.getByRole('dialog', { name: 'Nuevo documento' });
    await dlg.waitFor();
    await page.waitForTimeout(1000);
    const combos = (await dlg.getByRole('combobox').allInnerTexts()).filter(Boolean);
    await shot(page, 'web-project-plus');
    if (!combos.some((x) => x.startsWith(D.projectName))) {
      // With the client preloaded, the selector filters the projects by client and finds
      // none (JIKU-WEB-3): "Sin proyecto" shows. It is created anyway to see which relation it stores.
      bug('JIKU-WEB-3', `the project "+" shows ${combos.join(' / ')}`);
      const t = `${PREFIX} desde la card ${TAG}`;
      await dlg.getByLabel('Título').fill(t);
      await dlg.getByRole('button', { name: 'Crear y editar' }).click();
      await page.waitForURL(/\/docs\/[0-9A-Z]{26}/, { timeout: 20000 });
      const id = page.url().match(/\/docs\/([0-9A-Z]{26})/)[1];
      created.push(id);
      await page.getByText('Estás editando.', { exact: false }).waitFor({ timeout: 20000 }).catch(() => {});
      const combosEdit = (await page.locator('main').getByRole('combobox').allInnerTexts()).filter(Boolean);
      await shot(page, 'web-project-plus-created');
      const got = getDoc(id);
      out.push(`"+" → ${combos.join(' / ')}; created with projects ${JSON.stringify(got.projects)} clients ${JSON.stringify(got.clients)}; the editor shows ${combosEdit.join(' / ')}`);
      const cancel = page.getByRole('button', { name: 'Cancelar' });
      if (await cancel.count()) await cancel.click();
      await go(page, `${WEB}/projects/${D.project}`);
      await page.locator('[aria-label="Documentos vinculados"]').waitFor({ timeout: 30000 });
    } else {
      out.push(`"+" → ${combos.join(' / ')}`);
      await dlg.getByRole('button', { name: 'Cancelar' }).click();
    }
    // "Ver todos ›"
    await card.getByRole('link', { name: /Ver todos/ }).click();
    await page.waitForURL(/\/docs\?/, { timeout: 15000 });
    const u = new URL(page.url());
    assert(u.searchParams.get('proyecto') === String(D.project) && u.searchParams.get('origen') === 'proyecto', `URL: ${u.search}`);
    await page.getByRole('heading', { name: `Documentos · ${D.projectName}` }).waitFor({ timeout: 15000 });
    const list = await resultTitles();
    assert(!list.includes(F.RTitle), '"Ver todos" lists the restricted one');
    // The "+" may have created one more: count again.
    const expected2 = CLI_DOCS ? totalOf(`projectId=${D.project}`) - totalOf(`projectId=${D.project}`, 'visibility=restricted') : null;
    assert(expected2 === null || list.length === Math.min(expected2, 20), `"Ver todos": ${list.length} rows (expected ${expected2})`);
    const head = await page.getByRole('region', { name: 'Documentos', exact: true }).locator('thead').innerText();
    await shot(page, 'web-project-see-all');
    await page.getByText('Volver al proyecto', { exact: true }).click();
    await page.waitForURL(new RegExp(`/projects/${D.project}$`), { timeout: 15000 });
    out.push(`"Ver todos" → ${u.pathname}${u.search} (${list.length} rows, columns ${head.replace(/\s+/g, ' ').trim()}); "Volver al proyecto" → /projects/${D.project}`);
    // Requirement and task
    for (const [p, label] of [
      [`/requirements/${D.requirement}`, 'requirement'],
      [`/objectives/${D.task}`, 'task'],
    ]) {
      await go(page, WEB + p);
      c = await cardInfo();
      assert(c.titles.includes(D.arqTitle), `${label}: ${c.titles}`);
      assert(!c.titles.includes(F.RTitle), `the ${label} card shows the restricted one`);
      noPath(c.text, `the ${label} card`);
      await shot(page, `web-${label}-card`);
      out.push(`${label}: "${c.h2}" ${c.titles.join(' | ')}`);
    }
    return out.join('; ');
  });

  await step('W9', 'web: at 1440 px the requirement card does not overflow (item 13)', async () => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await go(page, `${WEB}/requirements/${D.requirement}`);
    await page.locator('[aria-label="Documentos vinculados"]').waitFor({ timeout: 30000 });
    const m = await page.evaluate(() => {
      const ul = document.querySelector('[aria-label="Documentos vinculados"]');
      let card = ul;
      while (card && !card.querySelector('h2')) card = card.parentElement;
      const box = card.getBoundingClientRect();
      const over = [...card.querySelectorAll('*')]
        .map((el) => ({ el, r: el.getBoundingClientRect() }))
        .filter(({ r }) => r.width > 0 && (r.right > box.right + 1 || r.left < box.left - 1))
        .map(({ el, r }) => `${el.tagName.toLowerCase()}.${String(el.className).split(' ')[0]} ${Math.round(r.right - box.right)}px`);
      return { scroll: card.scrollWidth - card.clientWidth, over: over.slice(0, 5), width: Math.round(box.width) };
    });
    await page.locator('[aria-label="Documentos vinculados"]').scrollIntoViewIfNeeded();
    await shot(page, 'web-requirement-1440', { fullPage: false });
    assert(m.scroll <= 0 && m.over.length === 0, `the card (${m.width}px) overflows: scroll ${m.scroll}px; ${m.over.join(', ')}`);
    return `card of ${m.width}px without overflow`;
  });

  await step('W10', 'web: a restricted one does not exist for the user (direct URL, search box, suggestions, cards, attachment)', async () => {
    if (!F.R) throw skip('without the restricted document of the run (E2E_CLI_DOCS)');
    const out = [];
    await go(page, `${WEB}/docs/${F.R}`);
    await page.getByText('Este documento no existe o no tenés permiso para verlo.').waitFor({ timeout: 20000 });
    const body = await page.locator('main').innerText();
    assert(!body.includes(F.RTitle) && !body.includes(F.RWord), 'the page leaks the title or the content');
    await shot(page, 'web-restricted-direct-url');
    out.push('/docs/{id} → "no existe o no tenés permiso" (does not exist or no permission)');
    for (const v of ['?version=1', '?editar=1']) {
      await go(page, `${WEB}/docs/${F.R}${v}`);
      await page.getByText('Este documento no existe o no tenés permiso para verlo.').waitFor({ timeout: 20000 });
    }
    assert(!lockOf(F.R), '?editar=1 took the lock of a restricted one');
    out.push('?version=1 and ?editar=1 the same, no lock');
    // Search box: by its word, its title and its topic.
    for (const q of [F.RWord, TAG]) {
      await go(page, `${WEB}/docs?q=${encodeURIComponent(q)}`);
      const t = await resultTitles();
      assert(!t.includes(F.RTitle), `the search "${q}" returns the restricted one`);
      // The search token repeats the query: only the results table is checked.
      const region = page.getByRole('region', { name: 'Documentos', exact: true });
      const shown = (await region.count()) ? await region.innerText() : '';
      assert(!shown.includes(F.RWord) && !shown.includes(F.RTitle), `the search "${q}" shows its content`);
    }
    await go(page, `${WEB}/docs?tema=${encodeURIComponent(F.RTopic)}`);
    const byTopic = await resultTitles();
    assert(byTopic.length === 0, `the filter by its topic returns: ${byTopic}`);
    await shot(page, 'web-restricted-topic-empty');
    out.push('search by content, title and topic: nothing');
    // Search suggestions: its topic does not appear under "Temas" (topics).
    await go(page, `${WEB}/docs`);
    await searchBox().fill(F.RTopic);
    await suggestions().getByRole('option').first().waitFor({ timeout: 15000 });
    await page.waitForTimeout(1500);
    const sug = await suggestions().innerText();
    await shot(page, 'web-restricted-suggestions');
    assert(!(await suggestions().getByRole('group', { name: 'Temas' }).count()), `suggests its topic: ${sug.replace(/\s+/g, ' ')}`);
    out.push(`suggestions for its topic: "${sug.replace(/\s+/g, ' ').trim()}"`);
    // Attachment
    const att = await ctx.request.get(`${WEB}/docs/attachments/${F.RAtt}`, { maxRedirects: 0 });
    assert(att.status() !== 302 && att.status() < 500, `attachment of the restricted one → ${att.status()} ${att.headers().location?.split('?')[0] || ''}`);
    out.push(`attachment → ${att.status()}`);
    // Cards: covered in W8 (project, "Ver todos", requirement and task do not list it).
    return out.join('; ');
  });

  await step('W11', 'web: lock of the service with its name and renewal/release of the own lock', async () => {
    assert(newId, 'no document from W3');
    // a) The service takes the lock: the person sees its name, not "Otra persona".
    const l = cliJSON('docs', 'lock', newId);
    let svcNotice = '';
    try {
      await go(page, `${WEB}/docs/${newId}`);
      const notice = page.getByText(/lo está editando hasta las/).first();
      await notice.waitFor({ timeout: 20000 });
      svcNotice = await notice.innerText();
      assert(await page.getByRole('button', { name: 'Editar' }).isDisabled(), "Editar enabled with someone else's lock");
      await shot(page, 'web-lock-service');
    } finally {
      cli('docs', 'unlock', newId, '--session', l.sessionId);
    }
    assert(!/otra persona/i.test(svcNotice), `notice: ${svcNotice}`);
    // b) Renewal (RENEW_INTERVAL_MS = 60 s) and release on leaving.
    await go(page, `${WEB}/docs/${newId}`);
    await page.getByRole('button', { name: 'Editar' }).click({ timeout: 70000 });
    await page.getByText('Estás editando.', { exact: false }).waitFor();
    const before = lockOf(newId);
    assert(before, 'no lock after Editar');
    await page.waitForTimeout(65000);
    const after = lockOf(newId);
    assert(after && after.expiresAt > before.expiresAt, `did not renew: ${before.expiresAt} → ${after?.expiresAt}`);
    await go(page, `${WEB}/docs`); // pagehide/unmount → unlock with keepalive
    await page.waitForTimeout(2000);
    const gone = lockOf(newId);
    assert(!gone, `the lock is still taken after leaving: ${JSON.stringify(gone)}`);
    return `service: "${svcNotice}"; renewed ${before.expiresAt} → ${after.expiresAt}; released on leaving`;
  });

  await step('W12', 'web: conflict while the human edits (document_locked and version_conflict)', async () => {
    assert(newId, 'no document from W3');
    const out = [];
    await go(page, `${WEB}/docs/${newId}`);
    await page.getByRole('button', { name: 'Editar' }).click();
    await page.getByText('Estás editando.', { exact: false }).waitFor();
    const cur = getDoc(newId, 'lock', 'content');
    const f = path.join(OUT, 'conflict.md');
    fs.writeFileSync(f, (cur.content || '') + '\nCambio del servicio por CLI.\n');
    let lockedErr = '';
    try {
      cli('docs', 'put', newId, f, '--base-version', String(cur.version));
    } catch (e) {
      lockedErr = String(e.stderr || e.message);
    }
    out.push(`put by the service with someone else's lock: ${/document_locked/.test(lockedErr) ? 'document_locked' : lockedErr.slice(0, 80) || 'IT SAVED'}`);
    const ta = page.getByLabel('Markdown', { exact: true });
    await ta.fill((await ta.inputValue()) + '\nTexto del humano.\n');
    await page.waitForTimeout(1000);
    const humanLock = lockOf(newId);
    assert(humanLock?.sessionId, 'the human does not hold the lock after Editar');
    cli('docs', 'unlock', newId, '--force', '--session', humanLock.sessionId);
    cli('docs', 'put', newId, f, '--base-version', String(cur.version));
    const n0 = actions.length;
    await page.getByRole('button', { name: 'Guardar', exact: true }).click();
    const notice = page.getByText(/guardó la versión|está editando|Se perdió la reserva/).first();
    await notice.waitFor({ timeout: 20000 }).catch(() => {});
    await page.waitForTimeout(1000);
    const save = actions.slice(n0).find((a) => a.body.includes(newId) && a.body.includes('baseVersion'));
    const rsc = save ? await (await save.req.response())?.text().catch(() => '') : '';
    const code = /version_conflict|document_locked/.exec(rsc || '')?.[0];
    await shot(page, 'web-editor-conflict');
    const noticeText = (await notice.count()) ? await notice.innerText() : '';
    out.push(`save by the human → ${code || 'no code'}; notice: ${noticeText.slice(0, 160)}`);
    assert(['version_conflict', 'document_locked'].includes(code), out.join('; '));
    assert(noticeText, out.join('; '));
    await page.getByRole('button', { name: /^Guardar/ }).click();
    await page.waitForTimeout(2500);
    await go(page, `${WEB}/docs`);
    return out.join('; ');
  });

  await step('W13', 'web: upload an attachment from the editor and insert it', async () => {
    assert(newId, 'no document from W3');
    await go(page, `${WEB}/docs/${newId}`);
    await page.getByRole('button', { name: 'Editar' }).click({ timeout: 70000 });
    await page.getByText('Estás editando.', { exact: false }).waitFor();
    const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAEklEQVR4nGP4z8CAFWEXHbQSACj/P8Fu7N9hAAAAAElFTkSuQmCC', 'base64');
    await page.locator('input[type=file]').setInputFiles({ name: 'e2e.png', mimeType: 'image/png', buffer: png });
    const ta = page.getByLabel('Markdown', { exact: true });
    await page.waitForFunction(() => /\/docs\/attachments\//.test(document.querySelector('textarea')?.value || ''), null, { timeout: 30000 });
    const md = await ta.inputValue();
    await page.getByRole('button', { name: 'Guardar', exact: true }).click();
    await page.getByText(/Guardaste la versión/).first().waitFor({ timeout: 20000 });
    await page.waitForTimeout(1500);
    const att = md.match(/\/docs\/attachments\/([0-9A-Z]{26})/)[1];
    const nat = await page.locator(`img[src*="${att}"]`).first().evaluate((i) => i.naturalWidth).catch(() => 0);
    await shot(page, 'web-attachment-inserted');
    assert(nat > 0, 'the inserted image does not load in the viewer');
    return `attachment ${att} inserted and visible`;
  });

  await step('W14', 'web: safe viewer (script, javascript:, onerror), image 302 and links', async () => {
    const md = [
      '<script>window.__xss=1;alert("script")</script>',
      '<img src=x onerror="window.__xss=2;alert(\'onerror\')">',
      '',
      '[enlace js](javascript:window.__xss=3;alert(1))',
      '',
      `![diagrama](/docs/attachments/${D.arqAttachment})`,
      '',
      `Ver [arquitectura](${WEB}/docs/${D.arq}) y [relativo](/docs/${D.arq}).`,
    ].join('\n');
    const xss = newDoc('xss', { title: `${PREFIX} xss ${TAG}`, body: md });
    await go(page, `${WEB}/docs/${xss.id}`);
    await page.getByRole('heading', { name: `${PREFIX} xss ${TAG}` }).first().waitFor({ timeout: 20000 });
    await page.waitForTimeout(2000);
    const jsLink = page.getByRole('link', { name: 'enlace js' });
    if (await jsLink.count()) await jsLink.click().catch(() => {});
    await page.waitForTimeout(500);
    const flag = await page.evaluate(() => window.__xss);
    assert(!flag && w.dialogs.length === 0, `JS ran: __xss=${flag} dialogs=${w.dialogs}`);
    const nat = await page.locator(`img[src*="${D.arqAttachment}"]`).evaluate((i) => i.naturalWidth).catch(() => 0);
    assert(nat > 0, 'the image did not load');
    await shot(page, 'web-visor-xss');
    const relHref = await page.locator('main a', { hasText: 'relativo' }).first().getAttribute('href').catch(() => null);
    assert(!relHref, `the relative link kept href ${relHref}`);
    const [tab] = await Promise.all([ctx.waitForEvent('page', { timeout: 20000 }), page.getByRole('link', { name: 'arquitectura' }).click()]);
    await tab.waitForURL(new RegExp(`/docs/${D.arq}`));
    await tab.getByRole('heading', { name: D.arqTitle }).first().waitFor({ timeout: 30000 });
    await tab.close();
    return `no execution; image ok; absolute link opens another tab; relative one without href (${xss.path})`;
  });

  await step('W15', 'web: imported note (no frontmatter, image 302 and internal link) and responsive viewer (390 px)', async (bug) => {
    const imgResp = page.waitForResponse((r) => r.url().includes(`/docs/attachments/${D.impImageAtt}`), { timeout: 20000 });
    await go(page, `${WEB}/docs/${D.impImage}`);
    await page.getByRole('heading', { name: D.impImageTitle }).first().waitFor({ timeout: 20000 });
    const r = await imgResp;
    assert(r.status() === 302, `imported image → ${r.status()}`);
    await page.waitForTimeout(1500);
    const nat = await page.locator(`img[src*="${D.impImageAtt}"]`).first().evaluate((i) => i.naturalWidth).catch(() => 0);
    assert(nat > 0, 'the imported image did not load');
    const text = await page.locator('main').innerText();
    if (/^---$/m.test(text) || /shortid:|^title:/m.test(text)) bug('JIKU-WEB-1', `note ${D.impImage}`);
    noPath(text, 'the imported note');
    await shot(page, 'web-viewer-imported');
    await go(page, `${WEB}/docs/${D.impLinkSrc}`);
    const link = page.locator('main').getByRole('link', { name: D.impLinkText, exact: true }).first();
    await link.waitFor({ timeout: 20000 });
    const [tab] = await Promise.all([ctx.waitForEvent('page', { timeout: 20000 }), link.click()]);
    await tab.waitForURL(new RegExp(`/docs/${D.impLinkDst}`), { timeout: 20000 });
    await tab.getByRole('heading', { name: D.impLinkDstTitle }).first().waitFor({ timeout: 30000 });
    await tab.close();
    await page.setViewportSize({ width: 390, height: 844 });
    const over = {};
    try {
      for (const [k, u] of [
        ['document', `${WEB}/docs/${D.arq}`],
        ['search', `${WEB}/docs?proyecto=${D.project}`],
      ]) {
        await go(page, u);
        await page.waitForTimeout(3000);
        over[k] = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
        await shot(page, `web-${k}-390`);
      }
    } finally {
      await page.setViewportSize({ width: 1440, height: 900 });
    }
    // The general layout of the web (outside the scope of P-1: sidebar and bar stay as they are) has
    // a minimum width that gives horizontal scroll on any screen at 390 px; it is reported without failing.
    const wide = Object.values(over).some((v) => v > 0);
    return `image 302 (${nat}px); link "${D.impLinkText}" opens "${D.impLinkDstTitle}"; 390 px: ${wide ? `NOTE horizontal scroll of the general layout ${JSON.stringify(over)}` : 'no horizontal scroll'}`;
  });

  if (withDown) {
    await step('W16', 'web: module down → notice and disabled actions; comes back', async () => {
      docker('stop', DOCS_CONTAINER);
      try {
        const t0 = Date.now();
        await go(page, `${WEB}/docs`);
        await page.getByText(/no está disponible/).first().waitFor({ timeout: 150000 });
        const ms = Date.now() - t0;
        const disabled = await searchBox().isDisabled();
        await shot(page, 'web-module-down-docs');
        await go(page, `${WEB}/projects/${D.project}`);
        await page.getByText(/no está disponible/).first().waitFor({ timeout: 30000 });
        await shot(page, 'web-module-down-project');
        assert(disabled, 'the search was not disabled');
        return `notice visible (unavailable after ${Math.round(ms / 1000)} s); search disabled`;
      } finally {
        docker('start', DOCS_CONTAINER);
        await go(page, `${WEB}/docs`);
        await page.getByRole('region', { name: 'Editados recientemente' }).waitFor({ timeout: 150000 });
      }
    });
  }

  await ctx.close();
}

const browser = await chromium.launch({ headless: !headed });
try {
  try {
    log(`data of the run: ${setupData()}`);
  } catch (e) {
    log(`data of the run: FAIL ${redact(e.message, creds).split('\n')[0]}`);
  }
  if (!only || only === 'opus') await opusSuite(browser);
  if (!only || only === 'public') await publicSuite(browser);
  if (!only || only === 'web') await webSuite(browser);
} finally {
  await browser.close();
  // Never leave the module down.
  try {
    const st = docker('inspect', '-f', '{{.State.Running}}', DOCS_CONTAINER);
    if (st !== 'true') docker('start', DOCS_CONTAINER);
  } catch {}
  cleanup();
}

const table = ['| id | scenario | status | detail |', '| --- | --- | --- | --- |', ...results.map((r) => `| ${r.id} | ${r.name} | ${r.status} | ${r.note.replace(/\|/g, '\\|')} |`)].join('\n');
fs.writeFileSync(path.join(OUT, 'results.md'), table + '\n');
log('\n' + table + `\n\nScreenshots and results in ${OUT}`);
// 1: some failure. 2: nothing failed but some scenarios were not tested (not "green").
// "jiku bug" (known Jiku bugs, see KNOWN) does not count unless E2E_STRICT=1.
const failed = results.some((r) => r.status === 'fail' || (STRICT && r.status === 'jiku bug'));
process.exitCode = failed ? 1 : results.some((r) => r.status === 'not tested') ? 2 : 0;
