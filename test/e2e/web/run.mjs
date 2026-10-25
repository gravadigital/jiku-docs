// Recorrido end-to-end en navegador de la integración de jiku-docs en web y opus-web.
//
//   node run.mjs [--only web|opus|public] [--down] [--headed] [--steps W1,W11]
//
// Ver README.md: variables, prerrequisitos y datos que espera el stack local.
import { chromium } from 'playwright';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { loadCreds, login, redact, repoRoot } from './lib.mjs';

const args = process.argv.slice(2);
const only = args.includes('--only') ? args[args.indexOf('--only') + 1] : null;
const withDown = args.includes('--down');
const headed = args.includes('--headed');

const WEB = process.env.E2E_WEB_URL || 'http://localhost:3000';
const OPUS = process.env.E2E_OPUS_URL || 'http://localhost:3001';
const PUBLIC = process.env.E2E_PUBLIC_URL || 'http://127.0.0.1:3100';
const DOCS_CONTAINER = process.env.E2E_DOCS_CONTAINER || 'jiku-docs-local';
// Comando (con argumentos) que corre `jiku` como el machine user del servicio jiku-docs (rol
// docs, clase service: lee y escribe todo), p. ej. "/ruta/as.sh docs". E2E_CLI_ADMIN es el nombre
// anterior y se sigue aceptando.
const CLI_DOCS = process.env.E2E_CLI_DOCS || process.env.E2E_CLI_ADMIN || '';
const OUT =
  process.env.E2E_OUT ||
  path.join(repoRoot, '.local/e2e-web', new Date().toISOString().replace(/[:.]/g, '-'));
fs.mkdirSync(OUT, { recursive: true });

// Datos del stack local (prueba por el bus previa, ver README).
const D = {
  project: Number(process.env.E2E_PROJECT_ID || 43),
  requirement: Number(process.env.E2E_REQUIREMENT_ID || 54),
  publicRequirement: Number(process.env.E2E_PUBLIC_REQUIREMENT_ID || 15),
  task: Number(process.env.E2E_TASK_ID || 2716),
  arq: process.env.E2E_DOC_PUBLIC || '01M5RAFX6A1R2VD9T277DEY0XP', // public, proyecto/req/tarea
  arqTitle: 'Arquitectura de videoconferencia',
  aduana: '01M5RAYHDTK4B43C52NZ16G5ZM', // public, requisito 15
  aduanaTitle: 'Integración con aduana',
  infra: '01M5RAN06ZMZ07EMMT69E274TY', // public en /infra/ (externalBlocked)
  infraTitle: 'Servidor de staging',
  internal: '01M5RAYNGC4J6Z0RE7YQ8DQ944', // internal, tarea 2697 del proyecto
  internalTitle: 'Reunión semanal',
  arqAttachment: '01M5RB1RRDH3AX4NWRQ10FFCDB', // diagrama.png de arq
  privAttachment: '01M5RB3FJXEEP0WH66VAJTTHH1', // adjunto de un documento internal
  clientFolder: '01M5RACNQ5QV1KSS9G2Q7FYJGX', // /clientes/exo/videoconferencia/
  // Notas importadas de HedgeDoc (lectura del rol user).
  impImage: process.env.E2E_IMPORTED_IMAGE_DOC || '01KPP6J840WSMXAM5CF9NX830Z', // /interno/proyectos/opus/, con imagen
  impImageTitle: 'Opus / Arquitectura del sistema',
  impImageAtt: '01KPP6J840V6XKJBAKS6FVB49G',
  impLinkSrc: process.env.E2E_IMPORTED_LINK_DOC || '01EFPRY9405SWET0DANRE36EBX', // Wecerty.md
  impLinkText: 'Versión 1',
  impLinkDst: '01G2NH12ZGDXS8EE7P1KC5ZV5F',
  impLinkDstTitle: 'Wecerty - versión 1',
  impFolders: ['clientes', 'wecerty', 'wecerty', 'tecnico'],
  impFolderDoc: 'Wecerty',
  searchPhrase: '"levantar nodos"',
  searchPhraseHit: 'RCA - Levantar nodos caídos',
  searchExclude: '"levantar nodos" -linode',
  searchExcluded: 'Ener2Crowd', // contiene "levantar nodo en Linode"
};

const creds = loadCreds();
const results = [];
const log = (...a) => console.log(redact(a.join(' '), creds));

// Bugs conocidos de Jiku (no de jiku-docs), reportados y sin arreglar. Un escenario que los
// encuentra sigue con lo demás y termina como "bug jiku" en vez de "ok"; no cuenta como falla
// para el código de salida salvo con E2E_STRICT=1. Si el bug se arregla, el escenario vuelve a "ok"
// solo: la condición se sigue verificando.
const KNOWN = {
  'JIKU-WEB-1': 'el visor de la web interna muestra el frontmatter (documento.md: "el frontmatter no se muestra")',
  'JIKU-WEB-2': '"Crear y editar" no entra en edición: tras router.push la página queda en ?editar=1 sin pedir el lock',
};
const STRICT = process.env.E2E_STRICT === '1';

// --steps W1,W11: corre solo esos escenarios (para depurar; los que dependen de otro fallan).
const STEPS = args.includes('--steps') ? args[args.indexOf('--steps') + 1].split(',') : null;

async function step(id, name, fn) {
  if (STEPS && !STEPS.includes(id)) return;
  const t0 = Date.now();
  const bugs = [];
  const bug = (code, detail = '') => bugs.push(`${code} ${KNOWN[code]}${detail ? ` (${detail})` : ''}`);
  try {
    let note = (await fn(bug)) || '';
    if (bugs.length) {
      note = `${bugs.join('; ')}${note ? ' — resto: ' + note : ''}`;
      results.push({ id, name, status: 'bug jiku', note });
      log(`BUG   ${id} ${name} — ${note} (${Date.now() - t0} ms)`);
      return;
    }
    results.push({ id, name, status: 'ok', note });
    log(`ok    ${id} ${name}${note ? ' — ' + note : ''} (${Date.now() - t0} ms)`);
  } catch (e) {
    const status = e.skip ? 'no probado' : 'falla';
    results.push({ id, name, status, note: redact(e.message, creds).split('\n')[0] });
    log(`${status === 'falla' ? 'FALLA' : 'skip '} ${id} ${name} — ${redact(e.message, creds).split('\n')[0]}`);
  }
}
const skip = (msg) => Object.assign(new Error(msg), { skip: true });
const assert = (cond, msg) => {
  if (!cond) throw new Error(msg);
};
// Las capturas tapan cualquier texto con el login de los usuarios de prueba.
const shot = (page, name, opts = {}) =>
  page.screenshot({
    path: path.join(OUT, name + '.png'),
    fullPage: true,
    mask: [page.getByText(creds.WEB_USER_LOGIN), page.getByText(creds.WEB_EXTERNAL_LOGIN)],
    ...opts,
  });

function cli(...cmdArgs) {
  if (!CLI_DOCS) throw skip('E2E_CLI_DOCS no definido');
  const [bin, ...pre] = CLI_DOCS.split(' ');
  try {
    return execFileSync(bin, [...pre, ...cmdArgs], { encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe'] });
  } catch (e) {
    // El primer renglón del error es lo que llega a la tabla: que diga qué contestó jiku.
    const why = String(e.stderr || '').split('\n').filter((l) => /error|code|:/.test(l)).slice(0, 2).join(' ').trim();
    throw Object.assign(new Error(`jiku ${cmdArgs.slice(0, 2).join(' ')}: ${why || e.message}`), { stderr: e.stderr });
  }
}
const cliJSON = (...a) => JSON.parse(cli(...a, '-o', 'json'));

// Vigila diálogos y errores de JS: un XSS que se ejecute los dispara.
function watch(page) {
  const w = { dialogs: [], errors: [], xss: false };
  page.on('dialog', async (d) => {
    w.dialogs.push(d.message());
    await d.dismiss().catch(() => {});
  });
  page.on('pageerror', (e) => w.errors.push(e.message));
  return w;
}

// page.goto puede dar net::ERR_ABORTED si una navegación del router de Next (router.replace de un
// efecto, un Link) arranca a la vez: se reintenta una vez si la página no quedó en la URL pedida.
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

async function waitText(page, locator, re, timeout = 15000) {
  await page.waitForFunction(
    ([sel, src]) => {
      const el = document.querySelector(sel);
      return el && new RegExp(src).test(el.innerText);
    },
    [locator, re.source],
    { timeout }
  );
}

function docker(...a) {
  return execFileSync('docker', a, { encoding: 'utf8' }).trim();
}
// SQL contra la base de Jiku local (solo para quitar y devolver permisos de prueba).
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
  throw new Error(`el módulo no pasó a ${want} en ${timeoutMs / 1000} s`);
}

// ------------------------------------------------------------------------------------------
// opus-web, usuario externo
// ------------------------------------------------------------------------------------------
async function opusSuite(browser) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();
  const w = watch(page);
  let logged = false;

  await step('O1', 'opus-web: login del externo', async () => {
    await login(page, OPUS, 'EXTERNAL', creds);
    logged = true;
    const s = await (await ctx.request.get(OPUS + '/api/auth/session')).json();
    assert(s?.user?.roles?.includes('external-user'), `roles de la sesión: ${JSON.stringify(s?.user?.roles)}`);
    const m = await (await ctx.request.get(OPUS + '/api/opus/modules')).json();
    const docs = m.find((x) => x.module === 'docs');
    assert(docs?.status === 'available', `GET /opus/modules: ${JSON.stringify(m)}`);
    return `docs=${docs.status}, sharing=${docs.features?.sharing}`;
  });
  if (!logged) return ctx.close();

  await step('O2', 'opus-web: "Documentación (n)" del proyecto con solo públicos', async () => {
    const docsResp = page.waitForResponse((r) => r.url().includes('/api/opus/docs/documents?'), { timeout: 20000 });
    await page.goto(`${OPUS}/projects/${D.project}/requirements`);
    const r = await docsResp;
    assert(r.status() === 200, `GET /api/opus/docs/documents → ${r.status()} ${(await r.text()).slice(0, 200)}`);
    const section = page.locator('section[aria-label="Documentación"]');
    await section.locator('a').first().waitFor({ timeout: 15000 });
    const text = await section.innerText();
    await shot(page, 'opus-tablero');
    const titles = await section.locator('a').allInnerTexts();
    const m = /Documentación \((\d+)\+?\)/.exec(text);
    assert(m && Number(m[1]) === titles.length, `encabezado "${text.split('\n')[0]}" vs ${titles.length} filas`);
    assert(titles.includes(D.arqTitle) && titles.includes(D.aduanaTitle), `faltan públicos: ${titles}`);
    assert(!titles.includes(D.infraTitle), `aparece el público de /infra/ (externalBlocked)`);
    assert(!titles.includes(D.internalTitle), `aparece un documento internal`);
    return `${text.split('\n')[0]}: ${titles.join(' | ')}`;
  });

  await step('O3', 'opus-web: sección en el detalle del requisito público', async () => {
    const reqTitle = 'Revisión de grabaciones para despliegue productivo';
    await page.getByText(reqTitle, { exact: false }).first().click();
    const resp = await page.waitForResponse(
      (r) => r.url().includes(`/api/opus/docs/documents?`) && r.url().includes(`requirementId=${D.publicRequirement}`),
      { timeout: 20000 }
    );
    assert(resp.status() === 200, `GET requirementId → ${resp.status()}`);
    // El detalle no es un role=dialog: es la segunda sección "Documentación" de la página.
    const section = page.locator('section[aria-label="Documentación"]').nth(1);
    await section.locator('a').first().waitFor({ timeout: 15000 });
    const titles = await section.locator('a').allInnerTexts();
    await shot(page, 'opus-detalle-requisito');
    assert(titles.length === 1 && titles[0] === D.aduanaTitle, `documentos del requisito: ${titles}`);
    await page.keyboard.press('Escape');
    // El requisito 54 es internal: no debe estar en el tablero del externo.
    const board = await page.locator('main').innerText();
    assert(!board.includes('Salida a producción'), 'el requisito internal 54 aparece en el portal');
    return `requisito ${D.publicRequirement}: ${titles.join(', ')}; requisito ${D.requirement} (internal) oculto`;
  });

  await step('O4', 'opus-web: visor del documento público (markdown seguro, imagen, sin edición)', async () => {
    const imgResp = page.waitForResponse((r) => r.url().includes(`/attachments/${D.arqAttachment}`), { timeout: 20000 });
    const docResp = page.waitForResponse((r) => r.url().endsWith(`/api/opus/docs/documents/${D.arq}`), { timeout: 20000 });
    await page.goto(`${OPUS}/projects/${D.project}/requirements`);
    await page.locator('section[aria-label="Documentación"] a', { hasText: D.arqTitle }).click();
    await page.waitForURL(new RegExp(`/projects/${D.project}/docs/${D.arq}`));
    await page.getByRole('heading', { name: D.arqTitle }).first().waitFor();
    const r = await imgResp;
    assert(r.status() === 302, `imagen: ${r.status()} (se esperaba 302 a la URL prefirmada)`);
    await page.waitForTimeout(1500);
    const main = page.locator('main');
    const img = await main.locator(`img[src*="${D.arqAttachment}"]`).evaluate((i) => i.naturalWidth);
    assert(img > 0, 'la imagen del adjunto no cargó');
    const html = await main.innerHTML();
    assert(!/<script/i.test(html) && !/onerror=/i.test(html), 'el HTML crudo del markdown llegó al DOM');
    assert(w.dialogs.length === 0, `se ejecutó JS del documento: ${w.dialogs}`);
    const text = await main.innerText();
    assert(!/^---$/m.test(text) && !text.includes('requirements: ['), 'se muestra el frontmatter');
    for (const label of ['Editar', 'Compartir', 'Guardar']) {
      assert((await page.getByRole('button', { name: label }).count()) === 0, `hay botón "${label}"`);
    }
    // D-11: el documento llega sin frontmatter (respuesta de red, no solo lo que se pinta).
    const dr = await docResp;
    const doc = await dr.json();
    assert(dr.status() === 200 && typeof doc.content === 'string', `GET documento: ${dr.status()}`);
    assert(!doc.content.trimStart().startsWith('---') && !/requirements:|projects:/.test(doc.content), 'la respuesta trae el frontmatter');
    // S-090: un enlace a otro documento (`/docs/{id}` de la web interna) se ve como texto.
    const hrefs = await main.locator('a').evaluateAll((as) => as.map((a) => a.getAttribute('href') || ''));
    const toDocs = hrefs.filter((h) => /\/docs\/[0-9A-Z]{26}([?#]|$)/.test(h));
    assert(toDocs.length === 0, `enlaces a documentos como <a>: ${toDocs.join(', ')}`);
    const linkText = /\[([^\]]+)\]\([^)]*\/docs\/[0-9A-Z]{26}\)/.exec(doc.content)?.[1];
    if (linkText) {
      assert(text.includes(linkText), `no aparece el texto del enlace "${linkText}"`);
      assert((await main.locator('a', { hasText: linkText }).count()) === 0, `"${linkText}" sigue siendo un <a>`);
    }
    await shot(page, 'opus-documento-1440');
    return (
      `imagen 302 → ${r.headers().location?.split('?')[0]}; sin JS; sin edición; respuesta sin frontmatter (${doc.content.length} B)` +
      (linkText ? `; enlace "${linkText}" como texto` : '; OBS: el documento no tiene enlaces a otros documentos')
    );
  });

  await step('O5', 'opus-web: descarga del adjunto por /opus/docs/documents/{doc}/attachments/{id}', async () => {
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
    assert(body.slice(1, 4).toString() === 'PNG', 'el archivo no es el PNG');
    // Adjunto de otro documento colgado de un documento público, y adjunto de un internal.
    const cross = await ctx.request.get(`${OPUS}/api/docs/documents/${D.aduana}/attachments/${D.arqAttachment}`, { maxRedirects: 0 });
    const priv = await ctx.request.get(`${OPUS}/api/docs/documents/${D.arq}/attachments/${D.privAttachment}`, { maxRedirects: 0 });
    assert(cross.status() === 404, `adjunto de otro documento: ${cross.status()}`);
    assert(priv.status() === 404, `adjunto de un documento internal: ${priv.status()}`);
    return `302 → storage 200 (${body.length} B; la pestaña nueva terminó en ${popupUrl.split('?')[0]}); cruzado 404; internal 404`;
  });

  await step('O6', 'opus-web: internal y /infra/ no se abren por URL directa', async () => {
    const out = [];
    for (const [id, label] of [
      [D.internal, 'internal'],
      [D.infra, '/infra/'],
    ]) {
      const api = await ctx.request.get(`${OPUS}/api/opus/docs/documents/${id}`);
      assert(api.status() === 404, `${label}: api ${api.status()}`);
      await page.goto(`${OPUS}/projects/${D.project}/docs/${id}`);
      await page.getByText('Este documento no existe o no está compartido con vos.').waitFor({ timeout: 15000 });
      out.push(`${label} → 404 + mensaje`);
    }
    await shot(page, 'opus-documento-no-visible');
    // Un documento público pero de un proyecto sin permiso: los e2e de otro proyecto.
    const other = await ctx.request.get(`${OPUS}/api/opus/docs/documents/01M5RCXWR9WGC9JMRSE3E3FX1R`);
    out.push(`público de otro proyecto → ${other.status()}`);
    assert(other.status() === 404, out.join('; '));
    return out.join('; ');
  });

  await step('O7', 'opus-web: responsive del documento (390 px)', async () => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(`${OPUS}/projects/${D.project}/docs/${D.arq}`);
    await page.getByRole('heading', { name: D.arqTitle }).first().waitFor();
    await page.waitForTimeout(1000);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    await shot(page, 'opus-documento-390');
    await page.goto(`${OPUS}/projects/${D.project}/requirements`);
    await page.locator('section[aria-label="Documentación"] a').first().waitFor({ timeout: 15000 });
    const overflow2 = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    await shot(page, 'opus-tablero-390');
    await page.setViewportSize({ width: 1440, height: 900 });
    assert(overflow <= 0, `scroll horizontal de ${overflow}px en el documento`);
    return `sin scroll horizontal (documento ${overflow}, tablero ${overflow2})`;
  });

  if (withDown) {
    await step('O8', 'opus-web: módulo caído → aviso; vuelve al levantarlo', async () => {
      docker('stop', DOCS_CONTAINER);
      try {
        const ms = await waitModule(ctx, OPUS, '/api/opus/modules', 'unavailable');
        await page.goto(`${OPUS}/projects/${D.project}/requirements`);
        await page.getByText('Los documentos no están disponibles').first().waitFor({ timeout: 20000 });
        await shot(page, 'opus-modulo-caido');
        await page.goto(`${OPUS}/projects/${D.project}/docs/${D.arq}`);
        await page.getByText(/no están disponibles/).first().waitFor({ timeout: 20000 });
        await shot(page, 'opus-documento-modulo-caido');
        return `unavailable a los ${Math.round(ms / 1000)} s; aviso en tablero y visor`;
      } finally {
        docker('start', DOCS_CONTAINER);
        await waitModule(ctx, OPUS, '/api/opus/modules', 'available');
        await page.goto(`${OPUS}/projects/${D.project}/requirements`);
        await page.locator('section[aria-label="Documentación"] a').first().waitFor({ timeout: 30000 });
      }
    });
  }

  await step('O10', 'opus-web: sin project-permissions → aviso "sin acceso" (no "no disponible")', async () => {
    const ext = await (await ctx.request.get(OPUS + '/api/auth/session')).json();
    const uid = process.env.E2E_EXTERNAL_USER_ID || ext?.user?.zitadelId || ext?.user?.sub || ext?.user?.id;
    if (!uid) throw skip(`la sesión no expone el id del externo (definir E2E_EXTERNAL_USER_ID): ${Object.keys(ext?.user || {})}`);
    const before = jikuSQL(`SELECT count(*) FROM user_project_permissions WHERE user_id='${uid}' AND project_id=${D.project}`);
    assert(before === '1', `el externo no tiene permiso sobre ${D.project} (${before})`);
    jikuSQL(`DELETE FROM user_project_permissions WHERE user_id='${uid}' AND project_id=${D.project}`);
    try {
      const respP = page.waitForResponse((r) => r.url().includes('/api/opus/docs/documents?'), { timeout: 20000 });
      await page.goto(`${OPUS}/projects/${D.project}/requirements`);
      const resp = await respP.catch(() => null);
      const forbidden = page.getByText('No tenés acceso a la documentación de este proyecto.');
      await forbidden.first().waitFor({ timeout: 20000 });
      const unavailable = await page.getByText('Los documentos no están disponibles').count();
      await shot(page, 'opus-sin-acceso');
      assert(unavailable === 0, 'aparece también el aviso de "no disponible"');
      return `GET documents → ${resp ? resp.status() : 'sin pedido'}; aviso de sin acceso`;
    } finally {
      jikuSQL(`INSERT INTO user_project_permissions (user_id, project_id) VALUES ('${uid}', ${D.project}) ON CONFLICT DO NOTHING`);
      await page.goto(`${OPUS}/projects/${D.project}/requirements`);
      await page.locator('section[aria-label="Documentación"] a').first().waitFor({ timeout: 30000 });
    }
  });

  await step('O9', 'web: el externo no entra a la web interna', async () => {
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
    assert(u.includes('/unauthorized'), `terminó en ${u}`);
    assert(att.status() === 403, `adjunto por la web con sesión de externo: ${att.status()}`);
    assert(anonAtt.status() >= 300 && anonAtt.status() < 400, `adjunto sin sesión: ${anonAtt.status()}`);
    return `${u}; /docs/{id} → ${doc.status()} ${doc.headers().location || ''}; adjunto con sesión externa → 403; sin sesión → ${anonAtt.status()} ${anonAtt.headers().location || ''}`;
  });

  await ctx.close();
}

// ------------------------------------------------------------------------------------------
// Link público (creado por el CLI del servicio) abierto en un navegador sin sesión
// ------------------------------------------------------------------------------------------
async function publicSuite(browser) {
  await step('P1', 'link público: página sin sesión, cabeceras, sin JS, adjunto, revocar → 404', async () => {
    const share = cliJSON('docs', 'share', D.arq, '--expires', '1d', '--note', 'e2e navegador');
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
      await shot(page, 'publico-link');
      assert(w.dialogs.length === 0, `se ejecutó JS: ${w.dialogs}`);
      assert(!/<script>alert/i.test(html), 'el <script> del markdown llegó al HTML');
      assert(!text.includes('requirements:'), 'se muestra el frontmatter');
      const links = await page.locator('a[href*="/docs/"]').count();
      const hdr = ['content-security-policy', 'x-robots-tag', 'referrer-policy', 'cache-control'].map((k) => `${k}=${h[k] ? 'sí' : 'NO'}`);
      assert(h['content-security-policy'] && /noindex/.test(h['x-robots-tag'] || ''), hdr.join(' '));
      const img = await page.locator('img').first().evaluate((i) => i.naturalWidth).catch(() => 0);
      // La imagen apunta a /p/docs/{token}/attachments/{id}, que responde 302 a la URL prefirmada.
      const src = await page.locator('img').first().getAttribute('src').catch(() => null);
      if (src) {
        assert(/^\/p\/docs\/jds_[^/]+\/attachments\/[0-9A-Z]{26}$/.test(src), `src de la imagen: ${src.replace(/jds_[^/]+/, 'jds_…')}`);
        const a302 = await ctx.request.get(new URL(src, url).href, { maxRedirects: 0 });
        assert(a302.status() === 302, `adjunto del link → ${a302.status()}`);
        att.push(302);
      }
      cli('docs', 'revoke', share.id);
      const r2 = await page.goto(url);
      assert(r2.status() === 404, `tras revocar → ${r2.status()}`);
      return `200; ${hdr.join(', ')}; sin JS; imagen ${img > 0 ? 'ok' : cspBlocked ? `302 pero la CSP (img-src 'self' https:) bloquea el storage ${cspBlocked} (http en local)` : 'NO carga'} (${att.join(',') || 'sin <img>'}); enlaces a documentos como <a>: ${links}; revocado → 404`;
    } finally {
      await ctx.close();
    }
  });
}

// ------------------------------------------------------------------------------------------
// web, usuario interno
// ------------------------------------------------------------------------------------------
async function webSuite(browser) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();
  const w = watch(page);
  const net = [];
  page.on('response', (r) => {
    if (/\/api\/docs|\/docs\/attachments|\/api\/modules/.test(r.url())) net.push(`${r.status()} ${r.request().method()} ${r.url()}`);
  });
  // La web habla con la api desde Server Actions de Next (POST a la propia página con la cabecera
  // `next-action`), no con fetch del navegador: lo que se puede observar es el cuerpo del POST
  // (los argumentos serializados) y la respuesta RSC.
  const actions = [];
  page.on('request', (r) => {
    if (r.method() === 'POST' && r.headers()['next-action']) actions.push({ url: r.url(), body: r.postData() || '', req: r });
  });
  const lockOf = (id) => cliJSON('docs', 'query', 'documents.get', '--id', id, '--include', 'lock').lock || null;
  let logged = false;
  const all = ['W2', 'W3', 'W4b', 'W4', 'W5', 'W6', 'W7', 'W8', 'W9', 'W10', 'W11', 'W12'];

  await step('W1', 'web: login del interno, /modules y entrada "Documentación"', async () => {
    await login(page, WEB, 'USER', creds);
    logged = true;
    // GET /modules lo hace el servidor de Next (Server Action): desde el navegador se ve su efecto.
    // La entrada del menú aparece solo con el módulo instalado; /docs sin aviso, solo disponible.
    await page.getByRole('link', { name: 'Documentación' }).first().waitFor({ timeout: 30000 });
    await shot(page, 'web-home');
    await go(page, `${WEB}/docs`);
    await page.locator('[aria-label="Contenido de la carpeta"]').waitFor({ timeout: 30000 });
    assert((await page.getByText(/no está disponible/).count()) === 0, 'aviso de módulo no disponible');
    return 'entrada "Documentación" en el menú; /docs lista carpetas sin aviso (docs=available)';
  });
  if (!logged) {
    const why = results.find((r) => r.id === 'W1')?.note || '';
    for (const id of all) results.push({ id, name: 'web (interno)', status: 'no probado', note: `sin login del usuario interno (W1: ${why})` });
    return ctx.close();
  }

  const panelTitles = async () => {
    const card = page.locator('[aria-label="Documentos vinculados"]');
    await card.waitFor({ timeout: 20000 });
    return card.locator('a').allInnerTexts();
  };

  await step('W2', 'web: card "Documentación" en proyecto, requisito y tarea', async () => {
    const out = [];
    await go(page, `${WEB}/projects/${D.project}`);
    let t = await panelTitles();
    assert(t.includes(D.arqTitle) && t.includes(D.infraTitle), `proyecto: ${t}`);
    await shot(page, 'web-ficha-proyecto-1440');
    out.push(`proyecto: ${t.length} (${t.join(' | ')})`);
    await go(page, `${WEB}/requirements/${D.requirement}`);
    t = await panelTitles();
    assert(t.includes(D.arqTitle), `requisito: ${t}`);
    out.push(`requisito: ${t.join(' | ')}`);
    await go(page, `${WEB}/objectives/${D.task}`);
    t = await panelTitles();
    assert(t.includes(D.arqTitle), `tarea: ${t}`);
    out.push(`tarea: ${t.join(' | ')}`);
    await page.setViewportSize({ width: 390, height: 844 });
    await go(page, `${WEB}/projects/${D.project}`);
    await panelTitles();
    await shot(page, 'web-ficha-proyecto-390');
    await page.setViewportSize({ width: 1440, height: 900 });
    return out.join('; ');
  });

  await step('W3', 'web: /docs, carpetas (incluidas importadas), búsqueda sobre importados (frase y exclusión) y apertura', async () => {
    const folderTable = page.locator('[aria-label="Contenido de la carpeta"]');
    // La tabla trae primero las subcarpetas y los documentos recién con "Ver más" (P-6 de la web):
    // se pagina hasta encontrar la fila.
    const row = async (name, exact = true) => {
      const link = folderTable.getByRole('link', { name, exact }).first();
      for (let i = 0; i < 10; i++) {
        if (await link.isVisible().catch(() => false)) return link;
        const more = page.getByRole('button', { name: 'Ver más' });
        if (await more.count()) await more.first().click();
        await page.waitForTimeout(1200);
      }
      await link.waitFor({ timeout: 5000 });
      return link;
    };
    const openPath = async (names) => {
      await go(page, `${WEB}/docs`);
      for (const n of names) {
        const before = page.url();
        await (await row(n)).click();
        await page.waitForURL((u) => u.href !== before && /\/docs\?folder=/.test(u.href));
        await folderTable.waitFor();
      }
    };
    await openPath(['clientes', 'exo', 'videoconferencia']);
    await row(D.arqTitle);
    await shot(page, 'web-docs-carpeta');
    await openPath(D.impFolders);
    await row(D.impFolderDoc);
    const impCount = await folderTable.locator('tbody tr').count();
    await shot(page, 'web-docs-carpeta-importada');
    const results = page.locator('[aria-label="Resultados de la búsqueda"]');
    const search = page.getByPlaceholder('Buscar en la documentación');
    const runSearch = async (q) => {
      await search.fill(q);
      await results.waitFor({ timeout: 20000 });
      // La búsqueda va por Server Action con debounce: se espera a que la lista se estabilice.
      let prev = null;
      for (let i = 0; i < 20; i++) {
        await page.waitForTimeout(700);
        const cur = (await results.locator('a').allInnerTexts()).join('|');
        if (cur && cur === prev) break;
        prev = cur;
      }
      return results.locator('a').allInnerTexts();
    };
    const phrase = await runSearch(D.searchPhrase);
    const excl = await runSearch(D.searchExclude);
    await shot(page, 'web-docs-busqueda');
    assert(phrase.includes(D.searchPhraseHit), `la frase no trajo "${D.searchPhraseHit}": ${phrase.join(' | ')}`);
    assert(phrase.includes(D.searchExcluded), `sin "${D.searchExcluded}" en la frase no se puede probar la exclusión: ${phrase.join(' | ')}`);
    assert(excl.includes(D.searchPhraseHit) && !excl.includes(D.searchExcluded), `exclusión: ${excl.join(' | ')}`);
    await results.getByRole('link', { name: D.searchPhraseHit }).first().click();
    await page.waitForURL(/\/docs\/[0-9A-Z]{26}/);
    await page.getByRole('heading', { name: D.searchPhraseHit }).first().waitFor({ timeout: 15000 });
    return `carpeta importada ${D.impFolders.join('/')}: ${impCount} filas; frase: ${phrase.length} resultados; exclusión: ${excl.length} (sin ${D.searchExcluded}); abre "${D.searchPhraseHit}"`;
  });

  await step('W4b', 'web: nota importada (imagen por /docs/attachments 302 y enlace interno a otra nota)', async (bug) => {
    const imgResp = page.waitForResponse((r) => r.url().includes(`/docs/attachments/${D.impImageAtt}`), { timeout: 20000 });
    await go(page, `${WEB}/docs/${D.impImage}`);
    await page.getByRole('heading', { name: D.impImageTitle }).first().waitFor({ timeout: 20000 });
    const r = await imgResp;
    assert(r.status() === 302, `imagen importada → ${r.status()}`);
    await page.waitForTimeout(1500);
    const nat = await page.locator(`img[src*="${D.impImageAtt}"]`).first().evaluate((i) => i.naturalWidth).catch(() => 0);
    assert(nat > 0, 'la imagen importada no cargó');
    const text = await page.locator('main').innerText();
    if (/^---$/m.test(text) || /shortid:|^title:/m.test(text)) bug('JIKU-WEB-1', `nota ${D.impImage}`);
    await shot(page, 'web-visor-importado');
    await go(page, `${WEB}/docs/${D.impLinkSrc}`);
    const link = page.locator('main').getByRole('link', { name: D.impLinkText, exact: true }).first();
    await link.waitFor({ timeout: 20000 });
    const href = await link.getAttribute('href');
    // Los enlaces del contenido se abren en otra pestaña (target=_blank del visor).
    const [tab] = await Promise.all([ctx.waitForEvent('page', { timeout: 20000 }), link.click()]);
    await tab.waitForURL(new RegExp(`/docs/${D.impLinkDst}`), { timeout: 20000 });
    await tab.getByRole('heading', { name: D.impLinkDstTitle }).first().waitFor({ timeout: 30000 });
    await tab.close();
    return `imagen 302 → ${r.headers().location?.split('?')[0]} (${nat}px); enlace "${D.impLinkText}" (${href?.replace(/^https?:\/\/[^/]+/, '')}) abre "${D.impLinkDstTitle}" en otra pestaña`;
  });

  // Documento de prueba con XSS, imagen y enlace interno (creado por el CLI del servicio).
  let xssId = null;
  await step('W4', 'web: visor seguro (script, javascript:, onerror), imagen 302 y enlace interno', async () => {
    const md = [
      '# Prueba XSS e2e',
      '',
      '<script>window.__xss=1;alert("script")</script>',
      '<img src=x onerror="window.__xss=2;alert(\'onerror\')">',
      '',
      '[enlace js](javascript:window.__xss=3;alert(1))',
      '',
      `![diagrama](/docs/attachments/${D.arqAttachment})`,
      '',
      // Los enlaces del visor solo pueden ser http(s), mailto o adjuntos (documento.md, CA-28):
      // uno relativo a otro documento queda sin href; uno absoluto a la web sí navega.
      `Ver [arquitectura](${WEB}/docs/${D.arq}) y [relativo](/docs/${D.arq}).`,
      '',
    ].join('\n');
    const f = path.join(OUT, 'xss.md');
    fs.writeFileSync(f, md);
    const name = `xss-e2e-${Date.now()}.md`;
    const created = cliJSON('docs', 'new', f, '--folder', '/clientes/exo/videoconferencia/', '--name', name);
    xssId = created.id;
    await go(page, `${WEB}/docs/${xssId}`);
    await page.getByRole('heading', { name: 'Prueba XSS e2e' }).first().waitFor({ timeout: 20000 });
    await page.waitForTimeout(2000);
    const jsLink = page.getByRole('link', { name: 'enlace js' });
    if (await jsLink.count()) await jsLink.click().catch(() => {});
    await page.waitForTimeout(500);
    const flag = await page.evaluate(() => window.__xss);
    assert(!flag && w.dialogs.length === 0, `se ejecutó JS: __xss=${flag} dialogs=${w.dialogs}`);
    const imgReq = net.find((l) => l.includes(`/docs/attachments/${D.arqAttachment}`));
    const r = await ctx.request.get(`${WEB}/docs/attachments/${D.arqAttachment}`, { maxRedirects: 0 });
    assert(r.status() === 302, `/docs/attachments → ${r.status()}`);
    const nat = await page.locator(`img[src*="${D.arqAttachment}"]`).evaluate((i) => i.naturalWidth).catch(() => 0);
    assert(nat > 0, 'la imagen no cargó');
    await shot(page, 'web-visor-xss');
    const relHref = await page.locator('main a', { hasText: 'relativo' }).first().getAttribute('href').catch(() => null);
    assert(!relHref, `el enlace relativo quedó con href ${relHref}`);
    const [tab] = await Promise.all([ctx.waitForEvent('page', { timeout: 20000 }), page.getByRole('link', { name: 'arquitectura' }).click()]);
    await tab.waitForURL(new RegExp(`/docs/${D.arq}`));
    await tab.getByRole('heading', { name: D.arqTitle }).first().waitFor({ timeout: 30000 });
    await tab.close();
    return `sin ejecución; imagen 302 → ${r.headers().location?.split('?')[0]} (${imgReq || 'request no capturada'}); enlace absoluto abre el documento en otra pestaña; relativo sin href`;
  });

  await step('W5', 'web: responsive del visor (390 / 1440)', async () => {
    await go(page, `${WEB}/docs/${D.arq}`);
    await page.getByRole('heading', { name: D.arqTitle }).first().waitFor();
    await shot(page, 'web-visor-1440');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.reload();
    await page.getByRole('heading', { name: D.arqTitle }).first().waitFor();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    await shot(page, 'web-visor-390');
    await page.setViewportSize({ width: 1440, height: 900 });
    assert(overflow <= 0, `scroll horizontal de ${overflow}px`);
    return 'sin scroll horizontal';
  });

  let newId = null;
  await step('W6', 'web: crear documento nuevo desde la UI', async (bug) => {
    await go(page, `${WEB}/docs?folder=${D.clientFolder}`);
    await page.getByRole('button', { name: 'Nuevo documento' }).click();
    const title = `E2E navegador ${Date.now()}`;
    await page.getByLabel('Título').fill(title);
    await page.getByRole('button', { name: 'Crear y editar' }).click();
    await page.waitForURL(/\/docs\/[0-9A-Z]{26}/, { timeout: 20000 });
    newId = page.url().match(/\/docs\/([0-9A-Z]{26})/)[1];
    const got = cliJSON('docs', 'query', 'documents.get', '--id', newId, '--include', 'content');
    assert(got.content === `# ${title}\n`, `contenido creado: ${JSON.stringify(got.content)}`);
    const editing = page.getByText('Lo estás editando');
    const ok = await editing.waitFor({ timeout: 20000 }).then(() => true, () => false);
    if (!ok) {
      // P-10: tras crear se entra directo en edición. Si no pasó, se recarga ?editar=1 (con carga
      // completa sí funciona) para que W7 pruebe la edición igual.
      await shot(page, 'web-crear-sin-edicion');
      bug('JIKU-WEB-2', `${page.url().replace(WEB, '')}, lock ${lockOf(newId) ? 'tomado' : 'no pedido'}`);
      await go(page, `${WEB}/docs/${newId}?editar=1`);
      await editing.waitFor({ timeout: 20000 });
    }
    return `id ${newId}, "${title}" en ${D.clientFolder}; editor abierto`;
  });

  await step('W7', 'web: editar con lock (toma, guardado con versión base, liberación)', async () => {
    assert(newId, 'sin documento de W6');
    const held = lockOf(newId);
    assert(held?.sessionId, 'el documento no quedó con lock al abrir el editor');
    const ta = page.getByLabel('Contenido (markdown)');
    await ta.fill((await ta.inputValue()) + '\nPrimera edición desde el navegador.\n');
    const n0 = actions.length;
    await page.getByRole('button', { name: 'Guardar' }).click();
    await page.getByText('Lo estás editando').waitFor({ state: 'detached', timeout: 20000 });
    await page.waitForTimeout(1500);
    const save = actions.slice(n0).find((a) => a.body.includes(newId) && a.body.includes('baseVersion'));
    assert(save, 'no se vio el guardado (Server Action con baseVersion)');
    assert(/"baseVersion":1\b/.test(save.body) && save.body.includes(held.sessionId), 'el guardado no lleva baseVersion=1 y el sessionId del lock');
    const got = cliJSON('docs', 'query', 'documents.get', '--id', newId, '--include', 'lock', '--include', 'content');
    assert(!got.lock, `el lock sigue tomado: ${JSON.stringify(got.lock)}`);
    assert(got.version === 2 && got.content.includes('Primera edición desde el navegador.'), `versión ${got.version}`);
    return `lock ${held.sessionId.slice(0, 8)}… (${held.via || 'web'}), guardado con baseVersion=1 y esa sesión → versión ${got.version}; lock liberado`;
  });

  await step('W8', 'web: renovación del lock y liberación al salir de la página', async () => {
    assert(newId, 'sin documento de W6');
    await go(page, `${WEB}/docs/${newId}`);
    await page.getByRole('button', { name: 'Editar' }).click();
    await page.getByText('Lo estás editando').waitFor();
    const before = cliJSON('docs', 'query', 'documents.get', '--id', newId, '--include', 'lock', '--include', 'content').lock;
    assert(before, 'sin lock tras Editar');
    await page.waitForTimeout(65000); // RENEW_INTERVAL_MS = 60 s
    const after = cliJSON('docs', 'query', 'documents.get', '--id', newId, '--include', 'lock', '--include', 'content').lock;
    assert(after && after.expiresAt > before.expiresAt, `no renovó: ${before.expiresAt} → ${after?.expiresAt}`);
    await go(page, `${WEB}/docs`); // pagehide/desmontaje → unlock con keepalive
    await page.waitForTimeout(2000);
    const gone = cliJSON('docs', 'query', 'documents.get', '--id', newId, '--include', 'lock', '--include', 'content').lock;
    assert(!gone, `el lock sigue tomado al salir: ${JSON.stringify(gone)}`);
    return `renovado ${before.expiresAt} → ${after.expiresAt}; liberado al salir`;
  });

  await step('W9', 'web: conflicto mientras el humano edita (version_conflict y document_locked)', async () => {
    assert(newId, 'sin documento de W6');
    const out = [];
    // a) document_locked: el servicio intenta guardar; el humano intenta editar.
    await go(page, `${WEB}/docs/${newId}`);
    await page.getByRole('button', { name: 'Editar' }).click();
    await page.getByText('Lo estás editando').waitFor();
    const cur = cliJSON('docs', 'query', 'documents.get', '--id', newId, '--include', 'lock', '--include', 'content');
    const f = path.join(OUT, 'conflict.md');
    fs.writeFileSync(f, (cur.content || '') + '\nCambio del servicio por CLI.\n');
    // Con el lock del humano, el put del servicio sin lock tiene que fallar con document_locked.
    let lockedErr = '';
    try {
      cli('docs', 'put', newId, f, '--base-version', String(cur.version));
    } catch (e) {
      lockedErr = String(e.stderr || e.message);
    }
    out.push(`put del servicio con lock ajeno: ${/document_locked/.test(lockedErr) ? 'document_locked' : lockedErr.slice(0, 80) || 'GUARDÓ'}`);
    // b) version_conflict: el humano suelta el lock (Cancelar), el servicio guarda y el humano guarda sobre la base vieja.
    await page.getByRole('button', { name: 'Cancelar' }).click();
    await page.getByRole('button', { name: 'Editar' }).click();
    await page.getByText('Lo estás editando').waitFor();
    const ta = page.getByLabel('Contenido (markdown)');
    await ta.fill((await ta.inputValue()) + '\nTexto del humano.\n');
    // El servicio fuerza (manage): libera el lock del humano y guarda.
    await page.waitForTimeout(1000);
    const humanLock = lockOf(newId);
    assert(humanLock?.sessionId, 'el humano no tiene el lock tras Editar');
    cli('docs', 'unlock', newId, '--force', '--session', humanLock.sessionId);
    cli('docs', 'put', newId, f, '--base-version', String(cur.version));
    const n0 = actions.length;
    await page.getByRole('button', { name: 'Guardar' }).click();
    const notice = page.getByText(/guardó la versión|está editando|Se perdió la reserva/).first();
    await notice.waitFor({ timeout: 20000 }).catch(() => {});
    await page.waitForTimeout(1000);
    const save = actions.slice(n0).find((a) => a.body.includes(newId) && a.body.includes('baseVersion'));
    const rsc = save ? await (await save.req.response())?.text().catch(() => '') : '';
    const code = /version_conflict|document_locked/.exec(rsc || '')?.[0];
    await shot(page, 'web-editor-conflicto');
    const noticeText = (await notice.count()) ? await notice.innerText() : '';
    out.push(`guardado del humano → ${code || 'sin código'}; aviso: ${noticeText.slice(0, 160)}`);
    assert(['version_conflict', 'document_locked'].includes(code), out.join('; '));
    assert(noticeText, out.join('; '));
    await page.getByRole('button', { name: /Guardar/ }).click();
    await page.waitForTimeout(2000);
    await go(page, `${WEB}/docs`);
    return out.join('; ');
  });

  await step('W10', 'web: subir adjunto desde el editor e insertarlo', async () => {
    assert(newId, 'sin documento de W6');
    await go(page, `${WEB}/docs/${newId}`);
    await page.getByRole('button', { name: 'Editar' }).click();
    await page.getByText('Lo estás editando').waitFor();
    const png = Buffer.from(
      'iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAEklEQVR4nGP4z8CAFWEXHbQSACj/P8Fu7N9hAAAAAElFTkSuQmCC',
      'base64'
    );
    await page.locator('input[type=file]').setInputFiles({ name: 'e2e.png', mimeType: 'image/png', buffer: png });
    const ta = page.getByLabel('Contenido (markdown)');
    // Subida: Server Action (attachments.new) → PUT a la URL prefirmada → confirm; al terminar,
    // el editor inserta el markdown de la imagen.
    await page.waitForFunction(() => /\/docs\/attachments\//.test(document.querySelector('textarea')?.value || ''), null, { timeout: 30000 });
    const md = await ta.inputValue();
    await page.getByRole('button', { name: 'Guardar' }).click();
    await page.waitForTimeout(2500);
    const att = md.match(/\/docs\/attachments\/([0-9A-Z]{26})/)[1];
    const nat = await page.locator(`img[src*="${att}"]`).first().evaluate((i) => i.naturalWidth).catch(() => 0);
    await shot(page, 'web-adjunto-insertado');
    assert(nat > 0, 'la imagen insertada no carga en el visor');
    return `adjunto ${att} insertado y visible`;
  });

  await step('W11', 'web: compartir (link una vez, página pública, listar, revocar → 404; bloqueado en /infra/)', async () => {
    // El rol user tiene write en /clientes/: para Compartir hace falta manage. Se le da una regla
    // por usuario en la carpeta de prueba y se quita al terminar.
    const sess = await (await ctx.request.get(WEB + '/api/auth/session')).json();
    const uid = process.env.E2E_WEB_USER_ID || sess?.user?.zitadelId || sess?.user?.sub || sess?.user?.id;
    if (!uid) throw skip(`la sesión no expone el id del interno (definir E2E_WEB_USER_ID): ${Object.keys(sess?.user || {})}`);
    const rules = (list) => cli('docs', 'cmd', `folders.${D.clientFolder}.acl.replace`, JSON.stringify({ rules: list }));
    // Se guardan las reglas que tenía la carpeta para devolverlas tal cual al terminar.
    const prev = (cliJSON('docs', 'query', 'folders.get', '--id', D.clientFolder, '--include', 'acl').acl || []).map(
      ({ principalType, principal, permission }) => ({ principalType, principal, permission })
    );
    rules([...prev.filter((r) => !(r.principalType === 'user' && r.principal === uid)), { principalType: 'user', principal: uid, permission: 'manage' }]);
    const anon = await browser.newContext();
    try {
      await go(page, `${WEB}/docs/${D.arq}`);
      await page.getByRole('heading', { name: D.arqTitle }).first().waitFor();
      const btn = page.getByRole('button', { name: 'Compartir' });
      await btn.waitFor({ timeout: 15000 });
      await btn.click();
      await page.getByRole('button', { name: 'Crear link' }).click();
      const link = page.getByRole('textbox', { name: 'Link', exact: true });
      await link.waitFor({ timeout: 15000 });
      const url = await link.inputValue();
      assert(/\/p\/docs\/jds_/.test(url), `link: ${url.slice(0, 40)}`);
      await shot(page, 'web-compartir-creado', { mask: [link, page.getByText(creds.WEB_USER_LOGIN)] });
      const pub = await anon.newPage();
      const r = await pub.goto(url.replace(/^https?:\/\/[^/]+/, PUBLIC));
      const hdr = r.headers();
      await shot(pub, 'web-link-publico');
      assert(r.status() === 200, `página pública: ${r.status()}`);
      await page.getByRole('dialog').getByRole('button', { name: 'Cerrar', exact: true }).first().click();
      await btn.click();
      const rows = page.locator('[aria-label="Links de este documento"] tbody tr');
      await rows.first().waitFor();
      const n = await rows.count();
      assert((await page.getByRole('textbox', { name: 'Link', exact: true }).count()) === 0, 'el link se vuelve a mostrar');
      await shot(page, 'web-compartir-lista');
      await page.getByRole('button', { name: 'Revocar' }).first().click();
      await page.getByRole('dialog').getByRole('button', { name: 'Revocar' }).last().click();
      await page.waitForTimeout(1500);
      const r2 = await pub.goto(url.replace(/^https?:\/\/[^/]+/, PUBLIC));
      assert(r2.status() === 404, `tras revocar: ${r2.status()}`);
      await page.keyboard.press('Escape');
      // /infra/: read para el rol user y externalBlocked → sin "Compartir".
      await go(page, `${WEB}/docs/${D.infra}`);
      await page.getByRole('heading', { name: D.infraTitle }).first().waitFor();
      const infraShare = await page.getByRole('button', { name: 'Compartir' }).count();
      await shot(page, 'web-infra-sin-compartir');
      assert(infraShare === 0, 'en /infra/ aparece "Compartir"');
      return `link → 200 (CSP ${hdr['content-security-policy'] ? 'sí' : 'no'}, noindex ${hdr['x-robots-tag'] || 'no'}); ${n} link(s) listados; revocado → 404; /infra/ sin Compartir`;
    } finally {
      await anon.close();
      rules(prev);
    }
  });

  if (withDown) {
    await step('W12', 'web: módulo caído → aviso y acciones deshabilitadas; vuelve', async () => {
      docker('stop', DOCS_CONTAINER);
      try {
        // La web pide /modules con el Bearer de la sesión: se espera el aviso en la UI (poll de 60 s).
        const t0 = Date.now();
        await go(page, `${WEB}/docs`);
        await page.getByText(/no está disponible/).first().waitFor({ timeout: 150000 });
        const ms = Date.now() - t0;
        const disabled = await page.getByPlaceholder('Buscar en la documentación').isDisabled();
        await shot(page, 'web-modulo-caido-docs');
        await go(page, `${WEB}/projects/${D.project}`);
        await page.getByText(/no está disponible/).first().waitFor({ timeout: 30000 });
        await shot(page, 'web-modulo-caido-ficha');
        assert(disabled, 'la búsqueda no se deshabilitó');
        return `aviso visible${ms ? ` (unavailable a los ${Math.round(ms / 1000)} s)` : ''}; búsqueda deshabilitada`;
      } finally {
        docker('start', DOCS_CONTAINER);
        await go(page, `${WEB}/docs`);
        await page.getByRole('link', { name: 'clientes' }).first().waitFor({ timeout: 150000 });
      }
    });
  }

  await ctx.close();
}

const browser = await chromium.launch({ headless: !headed });
try {
  if (!only || only === 'opus') await opusSuite(browser);
  if (!only || only === 'public') await publicSuite(browser);
  if (!only || only === 'web') await webSuite(browser);
} finally {
  await browser.close();
  // Nunca dejar el módulo abajo.
  try {
    const st = docker('inspect', '-f', '{{.State.Running}}', DOCS_CONTAINER);
    if (st !== 'true') docker('start', DOCS_CONTAINER);
  } catch {}
}

const table = ['| id | escenario | estado | detalle |', '| --- | --- | --- | --- |', ...results.map((r) => `| ${r.id} | ${r.name} | ${r.status} | ${r.note.replace(/\|/g, '\\|')} |`)].join('\n');
fs.writeFileSync(path.join(OUT, 'resultados.md'), table + '\n');
log('\n' + table + `\n\nCapturas y resultados en ${OUT}`);
// 1: alguna falla. 2: nada falló pero quedaron escenarios sin probar (no es "verde").
// "bug jiku" (bugs conocidos de Jiku, ver KNOWN) no cuenta salvo con E2E_STRICT=1.
const failed = results.some((r) => r.status === 'falla' || (STRICT && r.status === 'bug jiku'));
process.exitCode = failed ? 1 : results.some((r) => r.status === 'no probado') ? 2 : 0;
