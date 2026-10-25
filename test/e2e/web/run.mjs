// Recorrido end-to-end en navegador de la integración de jiku-docs en web y opus-web.
//
//   node run.mjs [--only web|opus] [--down] [--headed]
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
// Comando (con argumentos) que corre `jiku` como admin, p. ej. "/ruta/as.sh admin".
const CLI_ADMIN = process.env.E2E_CLI_ADMIN || '';
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
};

const creds = loadCreds();
const results = [];
const log = (...a) => console.log(redact(a.join(' '), creds));

async function step(id, name, fn) {
  const t0 = Date.now();
  try {
    const note = (await fn()) || '';
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
  if (!CLI_ADMIN) throw skip('E2E_CLI_ADMIN no definido');
  const [bin, ...pre] = CLI_ADMIN.split(' ');
  return execFileSync(bin, [...pre, ...cmdArgs], { encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe'] });
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
    const hrefs = await main.locator('a').evaluateAll((as) => as.map((a) => a.getAttribute('href') || ''));
    const toWeb = hrefs.filter((h) => h.startsWith(WEB));
    await shot(page, 'opus-documento-1440');
    return (
      `imagen 302 → ${r.headers().location?.split('?')[0]}; sin JS; sin edición` +
      (toWeb.length ? `; OBS: enlace a la web interna: ${toWeb.join(', ')}` : '')
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

  await step('O9', 'web: el externo no entra a la web interna', async () => {
    const c2 = await browser.newContext();
    const p2 = await c2.newPage();
    await login(p2, WEB, 'EXTERNAL', creds).catch(() => {});
    await p2.waitForTimeout(1500);
    const u = p2.url();
    await c2.close();
    assert(u.includes('/unauthorized'), `terminó en ${u}`);
    return u;
  });

  await ctx.close();
}

// ------------------------------------------------------------------------------------------
// Link público (creado por CLI admin) abierto en un navegador sin sesión
// ------------------------------------------------------------------------------------------
async function publicSuite(browser) {
  await step('P1', 'link público: página sin sesión, cabeceras, sin JS, adjunto, revocar → 404', async () => {
    const share = cliJSON('docs', 'share', D.arq, '--expires', '1d', '--note', 'e2e navegador');
    const url = share.url.replace(/^https?:\/\/[^/]+/, PUBLIC);
    const ctx = await browser.newContext();
    const page = await ctx.newPage();
    const w = watch(page);
    const att = [];
    page.on('response', (r) => {
      if (r.url().includes('/attachments/')) att.push(r.status());
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
      cli('docs', 'revoke', share.id);
      const r2 = await page.goto(url);
      assert(r2.status() === 404, `tras revocar → ${r2.status()}`);
      return `200; ${hdr.join(', ')}; sin JS; imagen ${img > 0 ? 'ok' : 'NO carga'} (${att.join(',') || 'sin pedidos de adjunto'}); enlaces a documentos como <a>: ${links}; revocado → 404`;
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
  let logged = false;
  const all = ['W2', 'W3', 'W4', 'W5', 'W6', 'W7', 'W8', 'W9', 'W10', 'W11', 'W12'];

  await step('W1', 'web: login del interno, /modules y entrada "Documentación"', async () => {
    const modResp = page.waitForResponse((r) => /\/api\/modules$/.test(r.url()), { timeout: 60000 });
    await login(page, WEB, 'USER', creds);
    logged = true;
    const r = await modResp;
    const mods = await r.json();
    const docs = mods.find((m) => m.module === 'docs');
    assert(docs?.status === 'available', `GET /modules: ${JSON.stringify(mods)}`);
    await page.getByRole('link', { name: 'Documentación' }).first().waitFor({ timeout: 15000 });
    await shot(page, 'web-home');
    return `docs=${docs.status}, sharing=${docs.features?.sharing}`;
  });
  if (!logged) {
    for (const id of all) results.push({ id, name: 'web (interno)', status: 'no probado', note: 'sin login del usuario interno (W1)' });
    return ctx.close();
  }

  const panelTitles = async () => {
    const card = page.locator('table[aria-label="Documentos vinculados"]');
    await card.waitFor({ timeout: 20000 });
    return card.locator('a').allInnerTexts();
  };

  await step('W2', 'web: card "Documentación" en proyecto, requisito y tarea', async () => {
    const out = [];
    await page.goto(`${WEB}/projects/${D.project}`);
    let t = await panelTitles();
    assert(t.includes(D.arqTitle) && t.includes(D.infraTitle), `proyecto: ${t}`);
    await shot(page, 'web-ficha-proyecto-1440');
    out.push(`proyecto: ${t.length} (${t.join(' | ')})`);
    await page.goto(`${WEB}/requirements/${D.requirement}`);
    t = await panelTitles();
    assert(t.includes(D.arqTitle), `requisito: ${t}`);
    out.push(`requisito: ${t.join(' | ')}`);
    await page.goto(`${WEB}/objectives/${D.task}`);
    t = await panelTitles();
    assert(t.includes(D.arqTitle), `tarea: ${t}`);
    out.push(`tarea: ${t.join(' | ')}`);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(`${WEB}/projects/${D.project}`);
    await panelTitles();
    await shot(page, 'web-ficha-proyecto-390');
    await page.setViewportSize({ width: 1440, height: 900 });
    return out.join('; ');
  });

  await step('W3', 'web: /docs, carpetas, búsqueda (frase y exclusión) y apertura', async () => {
    await page.goto(`${WEB}/docs`);
    await page.getByRole('link', { name: 'clientes' }).first().click();
    await page.getByRole('link', { name: 'exo' }).first().click();
    await page.getByRole('link', { name: 'videoconferencia' }).first().click();
    await page.locator('table[aria-label="Contenido de la carpeta"]').getByRole('link', { name: D.arqTitle }).waitFor();
    await shot(page, 'web-docs-carpeta');
    const search = page.getByPlaceholder('Buscar en la documentación');
    await search.fill('"integración con aduana"');
    await page.locator('table[aria-label="Resultados de la búsqueda"]').waitFor({ timeout: 15000 });
    const phrase = await page.locator('table[aria-label="Resultados de la búsqueda"] a').allInnerTexts();
    await search.fill('aduana -livekit');
    await page.waitForTimeout(1500);
    await page.locator('table[aria-label="Resultados de la búsqueda"], [class*="empty"]').first().waitFor({ timeout: 15000 });
    const excl = await page.locator('table[aria-label="Resultados de la búsqueda"] a').allInnerTexts();
    await shot(page, 'web-docs-busqueda');
    assert(phrase.length > 0, 'la frase no dio resultados');
    assert(!excl.includes(D.arqTitle), `la exclusión -livekit no sacó "${D.arqTitle}"`);
    await page.locator('table[aria-label="Resultados de la búsqueda"] a').first().click();
    await page.waitForURL(/\/docs\/[0-9A-Z]{26}/);
    return `frase: ${phrase.join(' | ')}; exclusión: ${excl.join(' | ') || '(sin resultados)'}`;
  });

  // Documento de prueba con XSS, imagen y enlace interno (creado por CLI admin).
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
      `Ver [arquitectura](/docs/${D.arq}).`,
      '',
    ].join('\n');
    const f = path.join(OUT, 'xss.md');
    fs.writeFileSync(f, md);
    const name = `xss-e2e-${Date.now()}.md`;
    const created = cliJSON('docs', 'new', f, '--folder', '/clientes/exo/videoconferencia/', '--name', name);
    xssId = created.id;
    await page.goto(`${WEB}/docs/${xssId}`);
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
    await page.getByRole('link', { name: 'arquitectura' }).click();
    await page.waitForURL(new RegExp(`/docs/${D.arq}`));
    await page.getByRole('heading', { name: D.arqTitle }).first().waitFor();
    return `sin ejecución; imagen 302 → ${r.headers().location?.split('?')[0]} (${imgReq || 'request no capturada'}); enlace interno ok`;
  });

  await step('W5', 'web: responsive del visor (390 / 1440)', async () => {
    await page.goto(`${WEB}/docs/${D.arq}`);
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
  await step('W6', 'web: crear documento nuevo desde la UI', async () => {
    await page.goto(`${WEB}/docs?folder=${D.clientFolder}`);
    await page.getByRole('button', { name: 'Nuevo documento' }).click();
    const title = `E2E navegador ${Date.now()}`;
    await page.getByLabel('Título').fill(title);
    await page.getByRole('button', { name: 'Crear y editar' }).click();
    await page.waitForURL(/\/docs\/[0-9A-Z]{26}/, { timeout: 20000 });
    newId = page.url().match(/\/docs\/([0-9A-Z]{26})/)[1];
    await page.getByText('Lo estás editando').waitFor({ timeout: 20000 });
    return `id ${newId}`;
  });

  await step('W7', 'web: editar con lock (toma, guardado con versión base, liberación)', async () => {
    assert(newId, 'sin documento de W6');
    const lockReq = net.filter((l) => l.includes(`/docs/documents/${newId}/lock`));
    assert(lockReq.length > 0, 'no se pidió el lock');
    const ta = page.getByLabel('Contenido (markdown)');
    await ta.fill((await ta.inputValue()) + '\nPrimera edición desde el navegador.\n');
    const putResp = page.waitForResponse((r) => r.request().method() === 'PUT' && r.url().includes(`/docs/documents/${newId}`));
    await page.getByRole('button', { name: 'Guardar' }).click();
    const r = await putResp;
    const body = JSON.parse(r.request().postData() || '{}');
    assert(r.status() === 200, `PUT ${r.status()}`);
    assert(body.baseVersion === 1 && body.sessionId, `PUT sin baseVersion/sessionId: ${Object.keys(body)}`);
    await page.waitForTimeout(1500);
    const unlock = net.filter((l) => l.includes(`/docs/documents/${newId}/unlock`));
    const got = cliJSON('docs', 'query', 'documents.get', '--id', newId);
    assert(!got.lock, `el lock sigue tomado: ${JSON.stringify(got.lock)}`);
    return `lock ${lockReq[0].split(' ')[0]}, PUT baseVersion=1, unlock ${unlock.length ? unlock[0].split(' ')[0] : 'no visto'}; versión ${got.version}`;
  });

  await step('W8', 'web: renovación del lock y liberación al salir de la página', async () => {
    assert(newId, 'sin documento de W6');
    await page.goto(`${WEB}/docs/${newId}`);
    await page.getByRole('button', { name: 'Editar' }).click();
    await page.getByText('Lo estás editando').waitFor();
    const before = cliJSON('docs', 'query', 'documents.get', '--id', newId).lock;
    assert(before, 'sin lock tras Editar');
    await page.waitForTimeout(65000); // RENEW_INTERVAL_MS = 60 s
    const after = cliJSON('docs', 'query', 'documents.get', '--id', newId).lock;
    assert(after && after.expiresAt > before.expiresAt, `no renovó: ${before.expiresAt} → ${after?.expiresAt}`);
    await page.goto(`${WEB}/docs`); // pagehide/desmontaje → unlock con keepalive
    await page.waitForTimeout(2000);
    const gone = cliJSON('docs', 'query', 'documents.get', '--id', newId).lock;
    assert(!gone, `el lock sigue tomado al salir: ${JSON.stringify(gone)}`);
    return `renovado ${before.expiresAt} → ${after.expiresAt}; liberado al salir`;
  });

  await step('W9', 'web: conflicto mientras el humano edita (version_conflict y document_locked)', async () => {
    assert(newId, 'sin documento de W6');
    const out = [];
    // a) document_locked: el admin toma el lock y guarda; el humano intenta editar.
    await page.goto(`${WEB}/docs/${newId}`);
    await page.getByRole('button', { name: 'Editar' }).click();
    await page.getByText('Lo estás editando').waitFor();
    const cur = cliJSON('docs', 'query', 'documents.get', '--id', newId);
    const f = path.join(OUT, 'conflict.md');
    fs.writeFileSync(f, (cur.content || '') + '\nCambio del admin por CLI.\n');
    // Con el lock del humano, el put del admin sin lock tiene que fallar con document_locked.
    let lockedErr = '';
    try {
      cli('docs', 'put', newId, f, '--base-version', String(cur.version));
    } catch (e) {
      lockedErr = String(e.stderr || e.message);
    }
    out.push(`put del admin con lock ajeno: ${/document_locked/.test(lockedErr) ? 'document_locked' : lockedErr.slice(0, 80) || 'GUARDÓ'}`);
    // b) version_conflict: el humano suelta el lock (Cancelar), el admin guarda y el humano guarda sobre la base vieja.
    await page.getByRole('button', { name: 'Cancelar' }).click();
    await page.getByRole('button', { name: 'Editar' }).click();
    await page.getByText('Lo estás editando').waitFor();
    const ta = page.getByLabel('Contenido (markdown)');
    await ta.fill((await ta.inputValue()) + '\nTexto del humano.\n');
    // El admin fuerza: libera el lock del humano (admin) y guarda.
    try {
      cli('docs', 'unlock', newId, '--force', '--session', 'e2e-admin');
    } catch (e) {
      out.push(`unlock --force: ${String(e.stderr || e.message).slice(0, 80)}`);
    }
    cli('docs', 'put', newId, f, '--base-version', String(cur.version));
    const putResp = page.waitForResponse((r) => r.request().method() === 'PUT' && r.url().includes(`/docs/documents/${newId}`));
    await page.getByRole('button', { name: 'Guardar' }).click();
    const r = await putResp;
    const code = (await r.json().catch(() => ({}))).code;
    await page.waitForTimeout(1500);
    await shot(page, 'web-editor-conflicto');
    const notice = await page.locator('[role="alert"]').allInnerTexts();
    out.push(`PUT del humano → ${r.status()} ${code}; aviso: ${notice.join(' / ').slice(0, 160)}`);
    assert(['version_conflict', 'document_locked'].includes(code), out.join('; '));
    assert(notice.some((t) => /guardó la versión|está editando|reserva/.test(t)), out.join('; '));
    await page.getByRole('button', { name: /Guardar/ }).click();
    await page.waitForTimeout(2000);
    await page.goto(`${WEB}/docs`);
    return out.join('; ');
  });

  await step('W10', 'web: subir adjunto desde el editor e insertarlo', async () => {
    assert(newId, 'sin documento de W6');
    await page.goto(`${WEB}/docs/${newId}`);
    await page.getByRole('button', { name: 'Editar' }).click();
    await page.getByText('Lo estás editando').waitFor();
    const png = Buffer.from(
      'iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAEklEQVR4nGP4z8CAFWEXHbQSACj/P8Fu7N9hAAAAAElFTkSuQmCC',
      'base64'
    );
    const confirm = page.waitForResponse((r) => /\/docs\/attachments\/[0-9A-Z]{26}\/confirm/.test(r.url()), { timeout: 30000 });
    await page.locator('input[type=file]').setInputFiles({ name: 'e2e.png', mimeType: 'image/png', buffer: png });
    const c = await confirm;
    assert(c.ok(), `confirm ${c.status()}`);
    const ta = page.getByLabel('Contenido (markdown)');
    await page.waitForFunction(() => /\/docs\/attachments\//.test(document.querySelector('textarea')?.value || ''));
    const md = await ta.inputValue();
    await page.getByRole('button', { name: 'Guardar' }).click();
    await page.waitForTimeout(2500);
    const att = md.match(/\/docs\/attachments\/([0-9A-Z]{26})/)[1];
    const nat = await page.locator(`img[src*="${att}"]`).first().evaluate((i) => i.naturalWidth).catch(() => 0);
    await shot(page, 'web-adjunto-insertado');
    assert(nat > 0, 'la imagen insertada no carga en el visor');
    return `adjunto ${att} insertado y visible`;
  });

  await step('W11', 'web: compartir (link una vez, página pública, listar, revocar → 404)', async () => {
    await page.goto(`${WEB}/docs/${D.arq}`);
    await page.getByRole('heading', { name: D.arqTitle }).first().waitFor();
    const canShare = (await page.getByRole('button', { name: 'Compartir' }).count()) > 0;
    const perm = cliJSON('docs', 'query', 'documents.get', '--id', D.arq).folder?.effectivePermission;
    if (!canShare) {
      // Sin manage no hay botón: es lo esperado. Probamos en un documento propio con manage.
      if (!newId) throw skip('sin manage y sin documento propio');
    }
    const target = canShare ? D.arq : newId;
    await page.goto(`${WEB}/docs/${target}`);
    const btn = page.getByRole('button', { name: 'Compartir' });
    await btn.waitFor({ timeout: 15000 });
    await btn.click();
    await page.getByRole('button', { name: 'Crear link' }).click();
    const link = page.getByLabel('Link');
    await link.waitFor({ timeout: 15000 });
    const url = await link.inputValue();
    assert(/\/p\/docs\/jds_/.test(url), `link: ${url.slice(0, 40)}`);
    await shot(page, 'web-compartir-creado');
    const anon = await browser.newContext();
    const pub = await anon.newPage();
    const r = await pub.goto(url.replace(/^https?:\/\/[^/]+/, PUBLIC));
    const ok = r.status() === 200;
    const hdr = r.headers();
    await shot(pub, 'web-link-publico');
    await page.getByRole('button', { name: 'Cerrar' }).click();
    await btn.click();
    const rows = page.locator('table[aria-label="Links de este documento"] tbody tr');
    await rows.first().waitFor();
    const n = await rows.count();
    assert((await page.getByLabel('Link').count()) === 0, 'el link se vuelve a mostrar');
    await page.getByRole('button', { name: 'Revocar' }).first().click();
    await page.getByRole('dialog').getByRole('button', { name: 'Revocar' }).click();
    await page.waitForTimeout(1500);
    const r2 = await pub.goto(url.replace(/^https?:\/\/[^/]+/, PUBLIC));
    await anon.close();
    assert(ok, `página pública: ${r.status()}`);
    assert(r2.status() === 404, `tras revocar: ${r2.status()}`);
    return `permiso en arq: ${perm}; link → 200 (CSP ${hdr['content-security-policy'] ? 'sí' : 'no'}, noindex ${hdr['x-robots-tag'] || 'no'}); ${n} link(s) listados; revocado → 404`;
  });

  if (withDown) {
    await step('W12', 'web: módulo caído → aviso y acciones deshabilitadas; vuelve', async () => {
      docker('stop', DOCS_CONTAINER);
      try {
        const ms = await waitModule(ctx, WEB.replace(':3000', ':3100'), '/api/modules', 'unavailable').catch(() => null);
        await page.goto(`${WEB}/docs`);
        await page.getByText(/no está disponible/).first().waitFor({ timeout: 90000 });
        const disabled = await page.getByPlaceholder('Buscar en la documentación').isDisabled();
        await shot(page, 'web-modulo-caido-docs');
        await page.goto(`${WEB}/projects/${D.project}`);
        await page.getByText(/no está disponible/).first().waitFor({ timeout: 30000 });
        await shot(page, 'web-modulo-caido-ficha');
        assert(disabled, 'la búsqueda no se deshabilitó');
        return `aviso visible${ms ? ` (unavailable a los ${Math.round(ms / 1000)} s)` : ''}; búsqueda deshabilitada`;
      } finally {
        docker('start', DOCS_CONTAINER);
        await page.goto(`${WEB}/docs`);
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
process.exitCode = results.some((r) => r.status === 'falla') ? 1 : 0;
