// Recorrido end-to-end en navegador de la integración de jiku-docs en web y opus-web, con el
// modelo de P-1 (documentación sin carpetas, contrato 1.3.0).
//
//   node run.mjs [--only web|opus|public] [--down] [--headed] [--steps W1,W5] [--keep]
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
const keep = args.includes('--keep');

const WEB = process.env.E2E_WEB_URL || 'http://localhost:3000';
const OPUS = process.env.E2E_OPUS_URL || 'http://localhost:3001';
const PUBLIC = process.env.E2E_PUBLIC_URL || 'http://127.0.0.1:3100';
const DOCS_CONTAINER = process.env.E2E_DOCS_CONTAINER || 'jiku-docs-local';
// Comando (con argumentos) que corre `jiku` como el machine user del servicio jiku-docs (rol
// docs, clase service: lee y escribe todo, también `restricted`), p. ej. "/ruta/as.sh docs".
// E2E_CLI_ADMIN es el nombre anterior y se sigue aceptando.
const CLI_DOCS = process.env.E2E_CLI_DOCS || process.env.E2E_CLI_ADMIN || '';
const OUT =
  process.env.E2E_OUT ||
  path.join(repoRoot, '.local/e2e-web', new Date().toISOString().replace(/[:.]/g, '-'));
fs.mkdirSync(OUT, { recursive: true });

// Datos del stack local (prueba por el bus previa, ver README).
const D = {
  project: Number(process.env.E2E_PROJECT_ID || 43),
  projectName: process.env.E2E_PROJECT_NAME || 'EXO - Videoconferencia',
  projectWord: 'Videoconferencia', // aparece solo en el nombre de ese proyecto
  client: Number(process.env.E2E_CLIENT_ID || 1),
  clientName: process.env.E2E_CLIENT_NAME || 'EXO',
  requirement: Number(process.env.E2E_REQUIREMENT_ID || 54),
  publicRequirement: Number(process.env.E2E_PUBLIC_REQUIREMENT_ID || 15),
  task: Number(process.env.E2E_TASK_ID || 2716),
  arq: process.env.E2E_DOC_PUBLIC || '01M5RAFX6A1R2VD9T277DEY0XP', // public, proyecto/req/tarea, cliente 1
  arqTitle: 'Arquitectura de videoconferencia',
  arqTopic: 'arquitectura',
  aduana: '01M5RAYHDTK4B43C52NZ16G5ZM', // public, requisito 15
  aduanaTitle: 'Integración con aduana',
  infra: '01M5RAN06ZMZ07EMMT69E274TY', // en /infra/: la migración de P-1 lo dejó internal
  infraTitle: 'Servidor de staging',
  internal: '01M5RAYNGC4J6Z0RE7YQ8DQ944', // internal, tarea 2697 del proyecto (indirecto)
  internalTitle: 'Reunión semanal',
  arqAttachment: '01M5RB1RRDH3AX4NWRQ10FFCDB', // diagrama.png de arq
  privAttachment: '01M5RB3FJXEEP0WH66VAJTTHH1', // adjunto de un documento internal
  otherProjectDoc: '01M5RCXWR9WGC9JMRSE3E3FX1R', // de otro proyecto (restricted tras P-1)
  // Notas importadas de HedgeDoc (lectura del rol user).
  impImage: process.env.E2E_IMPORTED_IMAGE_DOC || '01KPP6J840WSMXAM5CF9NX830Z', // con imagen
  impImageTitle: 'Opus / Arquitectura del sistema',
  impImageAtt: '01KPP6J840V6XKJBAKS6FVB49G',
  impLinkSrc: process.env.E2E_IMPORTED_LINK_DOC || '01EFPRY9405SWET0DANRE36EBX', // Wecerty.md
  impLinkText: 'Versión 1',
  impLinkDst: '01G2NH12ZGDXS8EE7P1KC5ZV5F',
  impLinkDstTitle: 'Wecerty - versión 1',
};

// Datos de esta corrida: todo lo que crea la suite lleva el prefijo "e2e-p1 " y la marca TAG (una
// palabra que no existe en otro lado, para buscarla). Al terminar se borra (salvo --keep).
const TAG = `p1${Date.now().toString(36)}`;
const PREFIX = 'e2e-p1';
const created = []; // ids a borrar al final

const creds = loadCreds();
const results = [];
const log = (...a) => console.log(redact(a.join(' '), creds));

// Bugs de Jiku (no de jiku-docs) reportados y sin arreglar. Un escenario que los encuentra sigue
// con lo demás y termina como "bug jiku" en vez de "ok"; no cuenta como falla para el código de
// salida salvo con E2E_STRICT=1. Si el bug se arregla, el escenario vuelve a "ok" solo: la
// condición se sigue verificando.
const KNOWN = {
  'JIKU-WEB-1': 'el visor de la web interna muestra el frontmatter (documento.md: "el frontmatter no se muestra")',
  'JIKU-WEB-2': '"Crear y editar" no entra en edición: tras router.push la página queda en ?editar=1 sin pedir el lock',
  'JIKU-WEB-3':
    'Cliente/Proyecto del alta y la edición: GET /api/projects no trae `client`, así que con un cliente elegido el selector de Proyecto queda solo con "Sin proyecto" y elegir el proyecto no completa el cliente',
  'JIKU-WEB-4':
    'la web compara el titular del lock y el autor (id de Zitadel) con `session.user.id` (uuid de Jiku): la persona nunca se reconoce a sí misma ("Lo estás editando en otra pestaña", "vos")',
  'JIKU-WEB-5':
    'lo que escribe una persona desde la web queda sin nombre (lock.holderName, updatedByName, authorName en null): el sobre `actor` de la api no trae name ni username y jiku-docs no lo busca en core, así que la web muestra "Otra persona" y "—" (el ítem 12 sigue)',
};
const STRICT = process.env.E2E_STRICT === '1';

// --steps W1,W11: corre solo esos escenarios (para depurar; los que dependen de otro fallan).
const STEPS = args.includes('--steps') ? args[args.indexOf('--steps') + 1].split(',') : null;

async function step(id, name, fn) {
  if (STEPS && !STEPS.includes(id)) return;
  const t0 = Date.now();
  // Un mismo bug encontrado varias veces en el escenario se informa una vez, con todos los detalles.
  const found = new Map();
  const bug = (code, detail = '') => found.set(code, [...(found.get(code) || []), detail].filter(Boolean));
  const bugsOf = () => [...found].map(([code, ds]) => `${code} ${KNOWN[code]}${ds.length ? ` (${ds.join('; ')})` : ''}`);
  try {
    let note = (await fn(bug)) || '';
    const bugs = bugsOf();
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
    if (status === 'falla' && failPage) await shot(failPage, `FALLA-${id}`);
    const msg = redact(e.message, creds).split('\n')[0];
    const bugs = bugsOf();
    results.push({ id, name, status, note: (bugs.length ? bugs.join('; ') + ' — ' : '') + msg });
    log(`${status === 'falla' ? 'FALLA' : 'skip '} ${id} ${name} — ${msg}`);
  }
}
// Página que se captura cuando un escenario falla (la de la suite en curso).
let failPage = null;
const skip = (msg) => Object.assign(new Error(msg), { skip: true });
const assert = (cond, msg) => {
  if (!cond) throw new Error(msg);
};
// Las capturas tapan cualquier texto con el login de los usuarios de prueba.
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
const getDoc = (id, ...include) => cliJSON('docs', 'query', 'documents.get', '--id', id, ...include.flatMap((i) => ['--include', i]));
const lockOf = (id) => getDoc(id, 'lock').lock || null;
function totalOf(...filters) {
  if (!CLI_DOCS) throw skip('E2E_CLI_DOCS no definido');
  const [bin, ...pre] = CLI_DOCS.split(' ');
  const out = execFileSync(
    'sh',
    ['-c', '"$@" 2>&1 >/dev/null', 'sh', bin, ...pre, 'docs', 'query', 'documents.list', ...filters.flatMap((f) => ['--filter', f]), '--count', '--limit', '1', '-o', 'json'],
    { encoding: 'utf8' }
  );
  const m = /(\d+) total/.exec(out);
  return m ? Number(m[1]) : 0;
}

// Crea un documento por el CLI del servicio, sin carpeta (la ubicación la genera el servidor).
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

// Vigila diálogos y errores de JS: un XSS que se ejecute los dispara.
function watch(page) {
  const w = { dialogs: [], errors: [] };
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
// Datos de la corrida (por el CLI del servicio)
// ------------------------------------------------------------------------------------------
// R: restricted, vinculado al proyecto, a los requisitos 54 (internal) y 15 (public) y a la
// tarea; con un tema y una palabra propios y un adjunto. Nadie salvo admin y servicios lo ve.
// SA y SB: internal sin proyecto; SA nombra el proyecto en el título ("Asignar sugerido"), SB no.
const F = { R: null, RTitle: `${PREFIX} restringido ${TAG}`, RTopic: `secreto${TAG}`, RWord: `zarzamora${TAG}`, RAtt: null };
function setupData() {
  if (!CLI_DOCS) return 'E2E_CLI_DOCS no definido: sin datos de la corrida';
  const r = newDoc('restringido', {
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
  const png = path.join(OUT, 'restringido.png');
  fs.writeFileSync(png, Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAEklEQVR4nGP4z8CAFWEXHbQSACj/P8Fu7N9hAAAAAElFTkSuQmCC', 'base64'));
  F.RAtt = cliJSON('docs', 'attach', r.id, png).id;
  F.SA = newDoc('sin-proyecto-sugerido', {
    title: `${PREFIX} ${D.projectWord} ${TAG}`,
    body: `Notas sueltas de la ${D.projectWord}, todavía sin proyecto.`,
  });
  F.SB = newDoc('sin-proyecto', { title: `${PREFIX} huerfano ${TAG}`, body: 'Texto sin pistas de ningún proyecto.' });
  return `R=${F.R} (${r.path}), SA=${F.SA.id}, SB=${F.SB.id}, tag ${TAG}`;
}

function cleanup() {
  if (keep || !CLI_DOCS) return;
  for (const id of created) {
    try {
      const d = getDoc(id);
      cli('docs', 'rm', id, '--base-version', String(d.version));
    } catch (e) {
      log(`limpieza: no se pudo borrar ${id}: ${e.message.split('\n')[0]}`);
    }
  }
}

// ------------------------------------------------------------------------------------------
// opus-web, usuario externo
// ------------------------------------------------------------------------------------------
async function opusSuite(browser) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();
  failPage = page;
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

  await step('O2', 'opus-web: "Documentación (n)" del proyecto con solo públicos (sin internal ni restricted)', async () => {
    const docsResp = page.waitForResponse((r) => r.url().includes('/api/opus/docs/documents?'), { timeout: 20000 });
    await page.goto(`${OPUS}/projects/${D.project}/requirements`);
    const r = await docsResp;
    assert(r.status() === 200, `GET /api/opus/docs/documents → ${r.status()} ${(await r.text()).slice(0, 200)}`);
    const body = await r.json().catch(() => ({}));
    const section = page.locator('section[aria-label="Documentación"]');
    await section.locator('a').first().waitFor({ timeout: 15000 });
    const text = await section.innerText();
    await shot(page, 'opus-tablero');
    const titles = await section.locator('a').allInnerTexts();
    const m = /Documentación \((\d+)\+?\)/.exec(text);
    assert(m && Number(m[1]) === titles.length, `encabezado "${text.split('\n')[0]}" vs ${titles.length} filas`);
    assert(titles.includes(D.arqTitle) && titles.includes(D.aduanaTitle), `faltan públicos: ${titles}`);
    assert(!titles.includes(D.infraTitle), `aparece "${D.infraTitle}" (internal)`);
    assert(!titles.includes(D.internalTitle), `aparece un documento internal`);
    assert(!titles.includes(F.RTitle), `aparece el documento restricted`);
    const raw = JSON.stringify(body);
    assert(!F.R || !raw.includes(F.R), 'la respuesta de la api trae el id del restricted');
    const vis = [...new Set((body.items || []).map((i) => i.visibility))];
    return `${text.split('\n')[0]}: ${titles.join(' | ')}; visibilidades en la respuesta: ${vis.join(',') || '—'}`;
  });

  await step('O3', 'opus-web: sección en el detalle del requisito público (sin el restricted vinculado)', async () => {
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
    return `requisito ${D.publicRequirement}: ${titles.join(', ')} (el restricted vinculado no aparece); requisito ${D.requirement} (internal) oculto`;
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
    // P-1: a un externo no le llegan clientes, requisitos ni tareas (ids internos).
    const leaked = ['clients', 'requirements', 'tasks'].filter((k) => Array.isArray(doc[k]) && doc[k].length > 0);
    assert(leaked.length === 0, `la respuesta trae ${leaked.join(', ')} al externo`);
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
    // Adjunto de otro documento colgado de un documento público, de un internal y de un restricted.
    const cross = await ctx.request.get(`${OPUS}/api/docs/documents/${D.aduana}/attachments/${D.arqAttachment}`, { maxRedirects: 0 });
    const priv = await ctx.request.get(`${OPUS}/api/docs/documents/${D.arq}/attachments/${D.privAttachment}`, { maxRedirects: 0 });
    assert(cross.status() === 404, `adjunto de otro documento: ${cross.status()}`);
    assert(priv.status() === 404, `adjunto de un documento internal: ${priv.status()}`);
    let restr = 'sin restricted';
    if (F.R) {
      const rr = await ctx.request.get(`${OPUS}/api/docs/documents/${F.R}/attachments/${F.RAtt}`, { maxRedirects: 0 });
      assert(rr.status() === 404, `adjunto del restricted: ${rr.status()}`);
      restr = 'restricted 404';
    }
    return `302 → storage 200 (${body.length} B; la pestaña nueva terminó en ${popupUrl.split('?')[0]}); cruzado 404; internal 404; ${restr}`;
  });

  await step('O6', 'opus-web: internal, restricted y de otro proyecto no se abren por URL directa', async () => {
    const out = [];
    const cases = [
      [D.internal, 'internal'],
      [D.infra, `${D.infraTitle} (internal)`],
      ...(F.R ? [[F.R, 'restricted del proyecto']] : []),
    ];
    for (const [id, label] of cases) {
      const api = await ctx.request.get(`${OPUS}/api/opus/docs/documents/${id}`);
      assert(api.status() === 404, `${label}: api ${api.status()}`);
      await page.goto(`${OPUS}/projects/${D.project}/docs/${id}`);
      await page.getByText('Este documento no existe o no está compartido con vos.').waitFor({ timeout: 15000 });
      out.push(`${label} → 404 + mensaje`);
    }
    await shot(page, 'opus-documento-no-visible');
    const other = await ctx.request.get(`${OPUS}/api/opus/docs/documents/${D.otherProjectDoc}`);
    out.push(`de otro proyecto → ${other.status()}`);
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

  await step('P2', 'link público: un restricted no admite links (sharing_blocked)', async () => {
    if (!F.R) throw skip('sin el documento restricted de la corrida');
    let err = '';
    try {
      cli('docs', 'share', F.R, '--expires', '1d');
    } catch (e) {
      err = String(e.stderr || e.message);
    }
    assert(/sharing_blocked/.test(err), `share de un restricted: ${err.slice(0, 120) || 'CREÓ EL LINK'}`);
    return 'sharing_blocked';
  });
}

// ------------------------------------------------------------------------------------------
// web, usuario interno (rol user)
// ------------------------------------------------------------------------------------------
async function webSuite(browser) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();
  failPage = page;
  const w = watch(page);
  // La web habla con la api desde Server Actions de Next (POST a la propia página con la cabecera
  // `next-action`), no con fetch del navegador: lo que se puede observar es el cuerpo del POST
  // (los argumentos serializados) y la respuesta RSC.
  const actions = [];
  page.on('request', (r) => {
    if (r.method() === 'POST' && r.headers()['next-action']) actions.push({ url: r.url(), body: r.postData() || '', req: r });
  });
  let logged = false;
  let me = { id: null, zitadelId: null, name: null };
  const all = ['W2', 'W3', 'W4', 'W5', 'W6', 'W7', 'W8', 'W9', 'W10', 'W11', 'W12', 'W13', 'W14', 'W15', 'W16'];

  // --- helpers de la pantalla /docs ---------------------------------------------------------
  const searchBox = () => page.getByRole('combobox', { name: 'Buscar en la documentación' });
  const activeTokens = () => page.getByRole('list', { name: 'Filtros activos' }).locator('li').allInnerTexts();
  const suggestions = () => page.getByRole('listbox', { name: 'Sugerencias' });
  // Espera a que la tabla de resultados se estabilice y devuelve los títulos.
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
  // Escribe en el buscador y elige una sugerencia (de un grupo, o la primera que coincida).
  const addFilter = async (text, optionName, group) => {
    await searchBox().fill(text);
    const scope = group ? suggestions().getByRole('group', { name: group }) : suggestions();
    const opt = scope.getByRole('option', { name: optionName }).first();
    await opt.waitFor({ timeout: 15000 });
    const before = page.url();
    await opt.click();
    await page.waitForURL((u) => u.href !== before, { timeout: 15000 });
  };
  // Abre un `Select` del DS (role=combobox con buscador) y elige una opción.
  const pick = async (scope, label, search, option) => {
    await scope.getByRole('combobox', { name: label, exact: true }).click();
    if (search) await scope.getByRole('textbox', { name: `Buscar en ${label}` }).fill(search);
    await scope.getByRole('option', { name: option }).first().click();
  };
  const optionsOf = async (scope, label) => {
    await scope.getByRole('combobox', { name: label, exact: true }).click();
    await page.waitForTimeout(400);
    const opts = await scope.getByRole('option').allInnerTexts();
    // Escape cerraría también el diálogo "Nuevo documento": se cierra con otro click.
    await scope.getByRole('combobox', { name: label, exact: true }).click();
    return opts;
  };
  const noPath = (text, where) => {
    const m = /\/(clientes|general|infra|interno|importado)\/[^\s]*|[\w-]+\.md\b|Ruta de almacenamiento/.exec(text);
    assert(!m, `${where} muestra la ruta: "${m?.[0]}"`);
  };

  await step('W1', 'web: login del interno, entrada "Documentación" e inicio de /docs sin carpetas', async () => {
    await login(page, WEB, 'USER', creds);
    logged = true;
    const s = await (await ctx.request.get(WEB + '/api/auth/session')).json();
    me = { id: s?.user?.id, zitadelId: s?.user?.zitadelId, name: s?.user?.name || null };
    await page.getByRole('link', { name: 'Documentación' }).first().waitFor({ timeout: 30000 });
    await go(page, `${WEB}/docs`);
    await searchBox().waitFor({ timeout: 30000 });
    assert((await page.getByText(/no está disponible/).count()) === 0, 'aviso de módulo no disponible');
    assert((await page.locator('[aria-label="Contenido de la carpeta"]').count()) === 0, 'sigue la tabla de carpetas');
    const groups = await page.locator('main').getByText(/^(En mis proyectos activos|Editados recientemente)$/).allInnerTexts();
    const titles = await resultTitles('Editados recientemente');
    const main = await page.locator('main').innerText();
    noPath(main, 'el inicio');
    for (const v of ['Vistas', 'Plantilla']) assert(!new RegExp(`\\b${v}\\b`).test(main), `aparece "${v}" (decisiones 6 y 7)`);
    assert(!titles.includes(F.RTitle), 'el inicio muestra el restricted');
    await shot(page, 'web-docs-inicio');
    return `grupos: ${groups.join(', ') || '—'}; ${titles.length} recientes; sin ruta, sin vistas ni plantillas`;
  });
  if (!logged) {
    const why = results.find((r) => r.id === 'W1')?.note || '';
    for (const id of all) results.push({ id, name: 'web (interno)', status: 'no probado', note: `sin login del usuario interno (W1: ${why})` });
    return ctx.close();
  }

  await step('W2', 'web: buscador con filtros como tokens (proyecto, cliente, tema, texto con Enter, "Sin proyecto"), sin ruta y paginado', async () => {
    const out = [];
    await go(page, `${WEB}/docs`);
    // Proyecto
    await addFilter(D.projectWord, new RegExp(`^${D.projectName}`), 'Proyectos');
    assert(new URL(page.url()).searchParams.get('proyecto') === String(D.project), `URL: ${page.url()}`);
    let t = await resultTitles();
    let tokens = await activeTokens();
    assert(tokens.includes(`Proyecto ${D.projectName}`), `tokens: ${tokens}`);
    assert(t.includes(D.arqTitle) && !t.includes(F.RTitle), `proyecto: ${t}`);
    const headProj = await page.getByRole('region', { name: 'Documentos', exact: true }).locator('thead').innerText();
    noPath(await page.locator('main').innerText(), 'el listado por proyecto');
    await shot(page, 'web-buscador-proyecto');
    out.push(`proyecto → ${t.length} (${headProj.replace(/\s+/g, ' ').trim()})`);
    // Cliente
    await addFilter(D.clientName, new RegExp(`^${D.clientName}$`), 'Clientes');
    assert(new URL(page.url()).searchParams.get('cliente') === String(D.client), `URL: ${page.url()}`);
    t = await resultTitles();
    tokens = await activeTokens();
    assert(tokens.includes(`Cliente ${D.clientName}`), `tokens: ${tokens}`);
    assert(t.includes(D.arqTitle), `proyecto+cliente: ${t}`);
    out.push(`+cliente → ${t.join(' | ')}`);
    // Tema
    await addFilter(D.arqTopic, new RegExp(`^${D.arqTopic}`, 'i'), 'Temas');
    t = await resultTitles();
    tokens = await activeTokens();
    assert(tokens.some((x) => /^Tema /i.test(x)), `tokens: ${tokens}`);
    assert(t.length >= 1 && t.includes(D.arqTitle), `+tema: ${t}`);
    await shot(page, 'web-buscador-tres-tokens');
    out.push(`+tema → ${t.length}`);
    // Backspace con el campo vacío quita el último token.
    await searchBox().click();
    await searchBox().press('Backspace');
    await page.waitForTimeout(800);
    tokens = await activeTokens();
    assert(!tokens.some((x) => /^Tema /i.test(x)), `Backspace no quitó el tema: ${tokens}`);
    // Quitar todos
    await page.getByRole('button', { name: 'Quitar todos los filtros' }).click();
    await page.waitForURL((u) => !u.search.includes('proyecto='));
    // Texto con Enter
    await searchBox().fill(TAG);
    await searchBox().press('Enter');
    await page.waitForURL((u) => u.searchParams.get('q') === TAG, { timeout: 15000 });
    t = await resultTitles();
    tokens = await activeTokens();
    assert(tokens.includes(`“${TAG}”`), `token de texto: ${tokens}`);
    assert(!F.SA || t.includes(F.SA.title), `texto: ${t}`);
    assert(!t.includes(F.RTitle), 'la búsqueda trae el restricted');
    const textMain = await page.locator('main').innerText();
    noPath(textMain, 'los resultados de texto');
    assert(!textMain.includes(F.RWord), 'aparece la palabra del restricted');
    await shot(page, 'web-buscador-texto');
    out.push(`texto "${TAG}" (Enter) → ${t.length}`);
    // "Sin proyecto" (sugerencia del grupo inicial "Sin …")
    await addFilter('sin', /^Sin proyecto$/);
    assert(new URL(page.url()).searchParams.get('sinProyecto') === '1', `URL: ${page.url()}`);
    await page.getByRole('heading', { name: 'Sin proyecto' }).first().waitFor();
    t = await resultTitles();
    assert(t.some((x) => x.includes(TAG)), `sin proyecto + texto: ${t}`);
    await shot(page, 'web-buscador-sin-proyecto-texto');
    out.push(`+Sin proyecto → ${t.length}`);
    // Paginado: "Sin proyecto" solo tiene más de 20 documentos.
    await go(page, `${WEB}/docs?sinProyecto=1`);
    t = await resultTitles();
    const head = await page.getByRole('region', { name: 'Documentos', exact: true }).locator('thead th').first().innerText();
    const m = /(\d+)\s+de\s+(\d+\+?)/i.exec(head);
    assert(m && Number(m[1]) === t.length, `encabezado "${head}" con ${t.length} filas`);
    const nav = page.getByRole('navigation', { name: 'Paginación' });
    assert(await nav.count(), 'sin paginado');
    noPath(await page.locator('main').innerText(), '"Sin proyecto"');
    await nav.getByRole('button', { name: 'Página siguiente' }).click();
    await page.waitForURL((u) => u.searchParams.get('pagina') === '2', { timeout: 15000 });
    const t2 = await resultTitles();
    assert(t2.length > 0 && t2.join('|') !== t.join('|'), 'la página 2 es igual a la 1');
    await shot(page, 'web-buscador-pagina-2');
    out.push(`paginado: "${head.trim()}" → página 2 con ${t2.length}`);
    return out.join('; ');
  });

  // Documento que crea la persona desde "Nuevo documento".
  let newId = null;
  const newTitle = `${PREFIX} alta ${TAG}`;
  await step('W3', 'web: "Nuevo documento" sin carpeta ni plantilla (cliente/proyecto, temas libres, visibilidad) y "Crear y editar" sin recargar', async (bug) => {
    const out = [];
    await go(page, `${WEB}/docs`);
    await page.getByRole('button', { name: 'Nuevo documento' }).click();
    const dlg = page.getByRole('dialog', { name: 'Nuevo documento' });
    await dlg.waitFor();
    const text = await dlg.innerText();
    for (const v of ['Carpeta', 'Plantilla', 'Ruta', 'Ubicación']) assert(!new RegExp(`\\b${v}\\b`).test(text), `el alta muestra "${v}"`);
    const radios = await dlg.getByRole('radio').allInnerTexts();
    const checked = await dlg.getByRole('radio', { checked: true }).allInnerTexts();
    assert(checked.join() === 'Interno', `visibilidad por defecto: ${checked}`);
    assert(!radios.includes('Restringido'), `"Restringido" aparece para un user: ${radios}`);
    out.push(`visibilidad [${radios.join(' · ')}] (default ${checked})`);
    await dlg.getByLabel('Título').fill(newTitle);
    // Cliente y proyecto: con el cliente elegido, el proyecto se filtra por ese cliente.
    await pick(dlg, 'Cliente', D.clientName, new RegExp(`^${D.clientName}$`));
    const projOpts = await optionsOf(dlg, 'Proyecto');
    const filtered = projOpts.filter((o) => o !== 'Sin proyecto');
    if (filtered.length === 0) {
      await shot(page, 'web-alta-proyecto-vacio');
      bug('JIKU-WEB-3', `cliente ${D.clientName} → opciones de Proyecto: ${projOpts.join(', ')}`);
      // Se elige el proyecto sin cliente (así el alta igual lleva proyecto).
      await pick(dlg, 'Cliente', null, /^Sin cliente$/);
    } else {
      assert(filtered.some((o) => o.startsWith(D.projectName)), `proyectos de ${D.clientName}: ${filtered.slice(0, 5)}`);
    }
    await pick(dlg, 'Proyecto', D.projectWord, new RegExp(`^${D.projectName}`));
    const combos = await dlg.getByRole('combobox').allInnerTexts();
    out.push(`cliente/proyecto: ${combos.filter(Boolean).join(' / ')}`);
    if (!combos.includes(D.clientName) && filtered.length === 0) out.push('el proyecto no completó el cliente');
    // Temas libres: uno nuevo con Enter y uno existente desde la sugerencia.
    const topics = dlg.getByRole('combobox', { name: 'Temas' });
    await topics.fill(`${PREFIX}-tema`);
    await topics.press('Enter');
    await topics.fill(D.arqTopic.slice(0, 5));
    const sug = dlg.getByRole('listbox', { name: 'Temas existentes' }).getByRole('option', { name: new RegExp(`^${D.arqTopic}`, 'i') }).first();
    await sug.waitFor({ timeout: 10000 });
    await sug.click();
    const chips = await dlg.getByRole('list', { name: 'Temas elegidos' }).innerText();
    assert(/e2e-p1-tema/i.test(chips) && new RegExp(D.arqTopic, 'i').test(chips), `temas elegidos: ${chips}`);
    await shot(page, 'web-alta-formulario');
    // "Crear y editar" entra en edición sin recargar la página (JIKU-WEB-2).
    await page.evaluate(() => (window.__e2eSinRecarga = 1));
    await dlg.getByRole('button', { name: 'Crear y editar' }).click();
    await page.waitForURL(/\/docs\/[0-9A-Z]{26}/, { timeout: 20000 });
    newId = page.url().match(/\/docs\/([0-9A-Z]{26})/)[1];
    created.push(newId);
    const editing = page.getByText('Estás editando.', { exact: false });
    const ok = await editing.waitFor({ timeout: 20000 }).then(() => true, () => false);
    const sameDoc = await page.evaluate(() => window.__e2eSinRecarga === 1);
    if (!ok) {
      await shot(page, 'web-crear-sin-edicion');
      bug('JIKU-WEB-2', `${page.url().replace(WEB, '')}, lock ${lockOf(newId) ? 'tomado' : 'no pedido'}`);
      await go(page, `${WEB}/docs/${newId}?editar=1`);
      await editing.waitFor({ timeout: 20000 });
    } else {
      assert(sameDoc, 'entró en edición pero con una recarga completa');
      await page.waitForFunction(() => !location.search.includes('editar=1'), null, { timeout: 10000 }).catch(() => {});
    }
    await shot(page, 'web-alta-en-edicion');
    const got = getDoc(newId, 'content', 'topics');
    assert(got.visibility === 'internal', `visibilidad creada: ${got.visibility}`);
    assert((got.projects || []).includes(D.project), `proyectos: ${JSON.stringify(got.projects)}`);
    assert((got.topics || []).length >= 2, `temas: ${JSON.stringify(got.topics)}`);
    assert(lockOf(newId)?.sessionId, 'el documento no quedó con lock al entrar en edición');
    out.push(`id ${newId} → ${got.path} (ubicación generada); temas ${JSON.stringify(got.topics)}; clientes ${JSON.stringify(got.clients)}; en edición sin recarga, URL ${page.url().replace(WEB, '')}`);
    return out.join('; ');
  });

  await step('W4', 'web: edición por campos, "Mensaje de la versión" y nombre propio en el lock', async (bug) => {
    assert(newId, 'sin documento de W3');
    const out = [];
    const editor = page.locator('main');
    const labels = ['Título', 'Markdown', 'Mensaje de la versión (opcional)'];
    for (const l of labels) assert(await editor.getByLabel(l, { exact: true }).count(), `falta el campo "${l}"`);
    assert(await editor.getByRole('combobox', { name: 'Temas' }).count(), 'falta el campo Temas');
    assert(await editor.getByRole('combobox', { name: 'Cliente', exact: true }).count(), 'falta Cliente');
    assert(await editor.getByRole('combobox', { name: 'Proyecto', exact: true }).count(), 'falta Proyecto');
    const radios = await editor.getByRole('radiogroup', { name: 'Visibilidad' }).getByRole('radio').allInnerTexts();
    assert(!radios.includes('Restringido'), `"Restringido" en la edición de un user: ${radios}`);
    const ta = editor.getByLabel('Markdown', { exact: true });
    assert(!/^---/.test(await ta.inputValue()), 'el editor muestra el frontmatter en el cuerpo');
    // El lock con el nombre propio: el servicio lo ve con holderName; en otra pestaña la web lo dice.
    const lock = lockOf(newId);
    assert(lock?.sessionId, 'sin lock en edición');
    if (!lock.holderName) bug('JIKU-WEB-5', `lock.holderName ${JSON.stringify(lock.holderName)}`);
    const tab = await ctx.newPage();
    await tab.goto(`${WEB}/docs/${newId}`);
    await tab.getByRole('heading', { name: newTitle }).first().waitFor({ timeout: 20000 });
    const notice = tab.getByText(/lo está editando|Lo estás editando/).first();
    await notice.waitFor({ timeout: 15000 });
    const noticeText = await notice.innerText();
    await shot(tab, 'web-lock-otra-pestana');
    // La fila del listado anuncia quién lo edita (texto para lectores de pantalla). Solo el modo
    // listado trae el lock (la búsqueda de texto no).
    await tab.goto(`${WEB}/docs?proyecto=${D.project}`);
    await tab.getByRole('region', { name: 'Documentos', exact: true }).waitFor({ timeout: 20000 });
    await tab.waitForTimeout(1500);
    const rowSr = await tab.getByText(/lo está editando$/).allInnerTexts();
    await tab.close();
    if (/otra persona/i.test(noticeText) || rowSr.some((x) => /otra persona/i.test(x))) {
      bug('JIKU-WEB-5', `otra pestaña: "${noticeText.slice(0, 50)}…"; fila: "${rowSr.join(' / ')}"`);
    }
    if (!/Lo estás editando en otra pestaña/.test(noticeText)) bug('JIKU-WEB-4', `otra pestaña: "${noticeText.slice(0, 60)}…"`);
    out.push(`lock: holderName "${lock.holderName}"; otra pestaña: "${noticeText.slice(0, 70)}"; fila: ${rowSr.join(' / ') || '—'}`);
    // Editar campos y cuerpo, y guardar con mensaje.
    await editor.getByRole('combobox', { name: 'Temas' }).fill(`${PREFIX}-editado`);
    await editor.getByRole('combobox', { name: 'Temas' }).press('Enter');
    await ta.fill((await ta.inputValue()) + `\nPrimera edición desde el navegador ${TAG}.\n`);
    await editor.getByLabel('Mensaje de la versión (opcional)').fill(`${PREFIX} versión con mensaje`);
    await shot(page, 'web-edicion');
    const n0 = actions.length;
    await page.getByRole('button', { name: 'Guardar', exact: true }).click();
    const toast = page.getByText(/Guardaste la versión \d+/).first();
    await toast.waitFor({ timeout: 20000 });
    const toastText = await toast.innerText();
    await page.getByText('Estás editando.', { exact: false }).waitFor({ state: 'detached', timeout: 20000 });
    const save = actions.slice(n0).find((a) => a.body.includes(newId) && a.body.includes('baseVersion'));
    assert(save && /"baseVersion":1\b/.test(save.body) && save.body.includes(lock.sessionId), 'el guardado no lleva baseVersion=1 y el sessionId del lock');
    await page.waitForTimeout(1000);
    const got = getDoc(newId, 'lock', 'content');
    assert(!got.lock, `el lock sigue tomado: ${JSON.stringify(got.lock)}`);
    assert(got.version === 2 && got.content.includes(`Primera edición desde el navegador ${TAG}.`), `versión ${got.version}`);
    const topicsNow = getDoc(newId, 'topics').topics || [];
    assert(topicsNow.some((x) => /editado/.test(x)), `temas: ${topicsNow}`);
    const hist = cliJSON('docs', 'history', newId);
    const v2 = (hist.items || hist).find((v) => v.version === 2);
    assert(v2?.message === `${PREFIX} versión con mensaje`, `mensaje de la versión 2: ${JSON.stringify(v2?.message)}`);
    const meta = await page.getByText(/^Versión \d+ · actualizado/).first().innerText();
    if (!/por vos$/.test(meta)) bug('JIKU-WEB-4', `cabecera: "${meta}"`);
    await shot(page, 'web-guardado');
    if (!got.updatedByName) bug('JIKU-WEB-5', `updatedByName ${JSON.stringify(got.updatedByName)}`);
    out.push(`"${toastText}"; versión 2 con mensaje y temas ${JSON.stringify(topicsNow)}; lock liberado; cabecera "${meta}"`);
    return out.join('; ');
  });

  await step('W5', 'web: lectura sin frontmatter, breadcrumb, Propiedades sin ruta, historial ("Ver" y "Restaurar")', async (bug) => {
    assert(newId, 'sin documento de W3');
    const out = [];
    await go(page, `${WEB}/docs/${newId}`);
    await page.getByRole('heading', { name: newTitle }).first().waitFor({ timeout: 20000 });
    const main = page.locator('main');
    const text = await main.innerText();
    if (/^---$/m.test(text) || /^(title|visibility|topics|jiku):/m.test(text)) bug('JIKU-WEB-1', `documento ${newId}`);
    noPath(text, 'el documento');
    const crumb = page.getByRole('navigation', { name: 'Ruta' }).first();
    assert(await crumb.getByRole('link', { name: 'Documentación' }).count(), 'sin breadcrumb a Documentación');
    const props = await main.getByRole('heading', { name: 'Propiedades' }).locator('xpath=ancestor::*[.//dl][1]').innerText();
    assert(/Visibilidad/.test(props) && /Versión/.test(props) && !/Ruta/.test(props), `Propiedades: ${props.replace(/\s+/g, ' ')}`);
    const identity = await main.getByText(new RegExp(`${D.projectName}`)).first().innerText().catch(() => '');
    out.push(`breadcrumb "${(await crumb.innerText()).replace(/\s+/g, ' ')}"; identidad "${identity}"; Propiedades: ${props.replace(/\s+/g, ' ')}`);
    await shot(page, 'web-lectura');
    // Historial: dos versiones, la 2 con mensaje y protegida.
    await page.getByRole('button', { name: 'Historial de versiones' }).click();
    const panel = page.getByRole('dialog', { name: 'Historial de versiones' });
    await panel.getByText('Versión 1', { exact: true }).waitFor({ timeout: 15000 });
    const entries = await panel.locator('li').allInnerTexts();
    assert(entries.length >= 2, `versiones: ${entries.length}`);
    const v2 = entries.find((e) => e.startsWith('Versión 2')) || '';
    assert(/Actual/i.test(v2) && /Protegida/i.test(v2) && v2.includes(`${PREFIX} versión con mensaje`), `versión 2: ${v2.replace(/\s+/g, ' ')}`);
    assert(!entries.some((e) => /Otra persona/.test(e)), `autor "Otra persona" en el historial: ${entries.map((e) => e.replace(/\s+/g, ' ')).join(' / ')}`);
    if (entries.some((e) => /^—/m.test(e))) bug('JIKU-WEB-5', 'autor "—" en el historial');
    await shot(page, 'web-historial');
    out.push(`historial: ${entries.map((e) => e.split('\n').slice(0, 3).join(' ')).join(' / ')}`);
    // Ver la versión 1
    const v1 = panel.locator('li', { hasText: /^Versión 1/ });
    await v1.getByRole('button', { name: 'Ver' }).click();
    await page.waitForURL(/\?version=1/, { timeout: 15000 });
    await page.getByText(/Estás viendo la versión 1/).waitFor({ timeout: 15000 });
    const v1Text = await main.innerText();
    assert(!v1Text.includes(`Primera edición desde el navegador ${TAG}`), 'la versión 1 muestra el texto de la 2');
    await shot(page, 'web-historial-ver-v1');
    // Restaurar la versión 1
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
    assert(got.version === 3 && !got.content.includes(`Primera edición desde el navegador ${TAG}`), `tras restaurar: versión ${got.version}`);
    await shot(page, 'web-restaurado');
    out.push(`"Ver" v1 con aviso; "${toastText}"`);
    return out.join('; ');
  });

  await step('W6', 'web: compartir (crear link, abrir la página pública, revocar → 404)', async () => {
    assert(newId, 'sin documento de W3');
    const anon = await browser.newContext();
    try {
      await go(page, `${WEB}/docs/${newId}`);
      await page.getByRole('heading', { name: newTitle }).first().waitFor();
      const btn = page.getByRole('button', { name: 'Compartir' });
      await btn.click();
      const dlg = page.getByRole('dialog', { name: new RegExp(`^Compartir «${newTitle}»`) });
      await dlg.waitFor();
      const head = await dlg.innerText();
      noPath(head, 'el diálogo de compartir');
      await shot(page, 'web-compartir-dialogo');
      await dlg.getByRole('button', { name: 'Crear link' }).click();
      const link = dlg.getByRole('textbox', { name: 'Link', exact: true });
      await link.waitFor({ timeout: 15000 });
      const url = await link.inputValue();
      assert(/\/p\/docs\/jds_/.test(url), `link: ${url.slice(0, 40)}`);
      await shot(page, 'web-compartir-creado', { mask: [link, page.getByText(creds.WEB_USER_LOGIN)] });
      const pub = await anon.newPage();
      const r = await pub.goto(url.replace(/^https?:\/\/[^/]+/, PUBLIC));
      assert(r.status() === 200, `página pública: ${r.status()}`);
      const pubText = await pub.locator('body').innerText();
      assert(pubText.includes(newTitle) && !/^visibility:/m.test(pubText), 'la página pública no muestra el documento o muestra el frontmatter');
      await shot(pub, 'web-link-publico');
      await dlg.getByRole('button', { name: 'Cerrar', exact: true }).click();
      await btn.click();
      const rows = dlg.locator('[aria-label="Links de este documento"] tbody tr');
      await rows.first().waitFor();
      const n = await rows.count();
      assert((await dlg.getByRole('textbox', { name: 'Link', exact: true }).count()) === 0, 'el link se vuelve a mostrar');
      await shot(page, 'web-compartir-lista');
      await dlg.getByRole('button', { name: 'Revocar' }).first().click();
      await page.getByRole('dialog', { name: 'Revocar link' }).getByRole('button', { name: 'Revocar' }).click();
      await page.waitForTimeout(1500);
      const r2 = await pub.goto(url.replace(/^https?:\/\/[^/]+/, PUBLIC));
      assert(r2.status() === 404, `tras revocar: ${r2.status()}`);
      await page.keyboard.press('Escape');
      return `link de un internal creado por un user → 200; ${n} link(s) listados; revocado → 404`;
    } finally {
      await anon.close();
    }
  });

  await step('W7', 'web: "Sin proyecto": "Asignar sugerido" y "Asignar proyecto" solo agregan la relación', async () => {
    if (!F.SA) throw skip('sin los documentos de la corrida (E2E_CLI_DOCS)');
    const out = [];
    await go(page, `${WEB}/docs?sinProyecto=1&q=${encodeURIComponent(TAG)}`);
    await page.getByRole('heading', { name: 'Sin proyecto' }).first().waitFor();
    const titles = await resultTitles();
    assert(titles.includes(F.SA.title) && titles.includes(F.SB.title), `resultados: ${titles}`);
    const head = await page.getByRole('region', { name: 'Documentos', exact: true }).locator('thead').innerText();
    assert(/Acción/i.test(head) && !/Ruta/i.test(head), `columnas: ${head.replace(/\s+/g, ' ')}`);
    const rowA = page.locator('tbody tr', { hasText: `${PREFIX} ${D.projectWord} ${TAG}` });
    const rowB = page.locator('tbody tr', { hasText: `${PREFIX} huerfano ${TAG}` });
    await rowA.getByText(/^Sugerido: /).waitFor({ timeout: 20000 });
    const sugText = await rowA.getByText(/^Sugerido: /).innerText();
    assert(sugText.includes(D.projectName), `sugerencia de SA: ${sugText}`);
    await shot(page, 'web-sin-proyecto');
    const pathA = getDoc(F.SA.id).path;
    await rowA.getByRole('button', { name: 'Asignar sugerido' }).click();
    const toastA = page.getByText(/^Asignaste «/).first();
    await toastA.waitFor({ timeout: 20000 });
    out.push(`"${await toastA.innerText()}"`);
    const a = getDoc(F.SA.id);
    assert(a.projects.includes(D.project) && a.path === pathA, `SA tras asignar: proyectos ${a.projects}, ruta ${a.path} (antes ${pathA})`);
    // SB: sin sugerencia → "Asignar proyecto" abre el selector en la fila.
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
    assert(b.projects.includes(D.project) && b.path === pathB, `SB tras asignar: proyectos ${b.projects}, ruta ${b.path}`);
    assert(b.content.includes('Texto sin pistas de ningún proyecto.'), 'cambió el cuerpo');
    const hist = cliJSON('docs', 'history', F.SB.id);
    const last = (hist.items || hist)[0];
    out.push(`versión ${b.version} "${last?.message}"; rutas sin cambios`);
    await page.waitForTimeout(1500);
    const after = await resultTitles();
    await shot(page, 'web-sin-proyecto-asignados');
    out.push(`quedan en la lista: ${after.filter((t) => t.includes(TAG)).length}`);
    return out.join('; ');
  });

  await step('W8', 'web: card "Documentación (N)" del proyecto, "+" precargado, "Ver todos ›" y "Volver al proyecto"; cards de requisito y tarea sin ruta', async (bug) => {
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
    assert(expected === null || n === expected, `"${c.h2}": se esperaban ${expected} (sin restricted)`);
    assert(!c.titles.includes(F.RTitle), 'la card del proyecto muestra el restricted');
    noPath(c.text, 'la card del proyecto');
    const indirect = (c.text.match(/vinculado, sin proyecto propio/g) || []).length;
    await shot(page, 'web-proyecto-card');
    out.push(`proyecto: "${c.h2}" (esperado ${expected ?? '?'}), ${c.titles.length} filas, ${indirect} "vinculado, sin proyecto propio"`);
    // "+" con el proyecto ya elegido
    await card.getByRole('button', { name: 'Nuevo documento en este proyecto' }).click();
    const dlg = page.getByRole('dialog', { name: 'Nuevo documento' });
    await dlg.waitFor();
    await page.waitForTimeout(1000);
    const combos = (await dlg.getByRole('combobox').allInnerTexts()).filter(Boolean);
    await shot(page, 'web-proyecto-mas');
    if (!combos.some((x) => x.startsWith(D.projectName))) {
      // Con el cliente precargado, el selector filtra los proyectos por cliente y no encuentra
      // ninguno (JIKU-WEB-3): se ve "Sin proyecto". Se crea igual para ver qué relación guarda.
      bug('JIKU-WEB-3', `"+" del proyecto muestra ${combos.join(' / ')}`);
      const t = `${PREFIX} desde la card ${TAG}`;
      await dlg.getByLabel('Título').fill(t);
      await dlg.getByRole('button', { name: 'Crear y editar' }).click();
      await page.waitForURL(/\/docs\/[0-9A-Z]{26}/, { timeout: 20000 });
      const id = page.url().match(/\/docs\/([0-9A-Z]{26})/)[1];
      created.push(id);
      await page.getByText('Estás editando.', { exact: false }).waitFor({ timeout: 20000 }).catch(() => {});
      const combosEdit = (await page.locator('main').getByRole('combobox').allInnerTexts()).filter(Boolean);
      await shot(page, 'web-proyecto-mas-creado');
      const got = getDoc(id);
      out.push(`"+" → ${combos.join(' / ')}; creado con proyectos ${JSON.stringify(got.projects)} clientes ${JSON.stringify(got.clients)}; el editor muestra ${combosEdit.join(' / ')}`);
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
    assert(!list.includes(F.RTitle), '"Ver todos" lista el restricted');
    // El "+" pudo haber creado uno más: se vuelve a contar.
    const expected2 = CLI_DOCS ? totalOf(`projectId=${D.project}`) - totalOf(`projectId=${D.project}`, 'visibility=restricted') : null;
    assert(expected2 === null || list.length === Math.min(expected2, 20), `"Ver todos": ${list.length} filas (esperado ${expected2})`);
    const head = await page.getByRole('region', { name: 'Documentos', exact: true }).locator('thead').innerText();
    await shot(page, 'web-proyecto-ver-todos');
    await page.getByText('Volver al proyecto', { exact: true }).click();
    await page.waitForURL(new RegExp(`/projects/${D.project}$`), { timeout: 15000 });
    out.push(`"Ver todos" → ${u.pathname}${u.search} (${list.length} filas, columnas ${head.replace(/\s+/g, ' ').trim()}); "Volver al proyecto" → /projects/${D.project}`);
    // Requisito y tarea
    for (const [p, label] of [
      [`/requirements/${D.requirement}`, 'requisito'],
      [`/objectives/${D.task}`, 'tarea'],
    ]) {
      await go(page, WEB + p);
      c = await cardInfo();
      assert(c.titles.includes(D.arqTitle), `${label}: ${c.titles}`);
      assert(!c.titles.includes(F.RTitle), `la card del ${label} muestra el restricted`);
      noPath(c.text, `la card del ${label}`);
      await shot(page, `web-${label}-card`);
      out.push(`${label}: "${c.h2}" ${c.titles.join(' | ')}`);
    }
    return out.join('; ');
  });

  await step('W9', 'web: a 1440 px la card de requisito no desborda (ítem 13)', async () => {
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
    await shot(page, 'web-requisito-1440', { fullPage: false });
    assert(m.scroll <= 0 && m.over.length === 0, `la card (${m.width}px) desborda: scroll ${m.scroll}px; ${m.over.join(', ')}`);
    return `card de ${m.width}px sin desborde`;
  });

  await step('W10', 'web: un restricted no existe para el user (URL directa, buscador, sugerencias, cards, adjunto)', async () => {
    if (!F.R) throw skip('sin el documento restricted de la corrida (E2E_CLI_DOCS)');
    const out = [];
    await go(page, `${WEB}/docs/${F.R}`);
    await page.getByText('Este documento no existe o no tenés permiso para verlo.').waitFor({ timeout: 20000 });
    const body = await page.locator('main').innerText();
    assert(!body.includes(F.RTitle) && !body.includes(F.RWord), 'la página filtra el título o el contenido');
    await shot(page, 'web-restricted-url-directa');
    out.push('/docs/{id} → "no existe o no tenés permiso"');
    for (const v of ['?version=1', '?editar=1']) {
      await go(page, `${WEB}/docs/${F.R}${v}`);
      await page.getByText('Este documento no existe o no tenés permiso para verlo.').waitFor({ timeout: 20000 });
    }
    assert(!lockOf(F.R), '?editar=1 tomó el lock de un restricted');
    out.push('?version=1 y ?editar=1 igual, sin lock');
    // Buscador: por su palabra, su título y su tema.
    for (const q of [F.RWord, TAG]) {
      await go(page, `${WEB}/docs?q=${encodeURIComponent(q)}`);
      const t = await resultTitles();
      assert(!t.includes(F.RTitle), `la búsqueda "${q}" trae el restricted`);
      // El token del buscador repite lo buscado: se mira solo la tabla de resultados.
      const region = page.getByRole('region', { name: 'Documentos', exact: true });
      const shown = (await region.count()) ? await region.innerText() : '';
      assert(!shown.includes(F.RWord) && !shown.includes(F.RTitle), `la búsqueda "${q}" muestra su contenido`);
    }
    await go(page, `${WEB}/docs?tema=${encodeURIComponent(F.RTopic)}`);
    const byTopic = await resultTitles();
    assert(byTopic.length === 0, `el filtro por su tema trae: ${byTopic}`);
    await shot(page, 'web-restricted-tema-vacio');
    out.push('búsqueda por contenido, título y tema: nada');
    // Sugerencias del buscador: su tema no aparece en "Temas".
    await go(page, `${WEB}/docs`);
    await searchBox().fill(F.RTopic);
    await suggestions().getByRole('option').first().waitFor({ timeout: 15000 });
    await page.waitForTimeout(1500);
    const sug = await suggestions().innerText();
    await shot(page, 'web-restricted-sugerencias');
    assert(!(await suggestions().getByRole('group', { name: 'Temas' }).count()), `sugiere su tema: ${sug.replace(/\s+/g, ' ')}`);
    out.push(`sugerencias para su tema: "${sug.replace(/\s+/g, ' ').trim()}"`);
    // Adjunto
    const att = await ctx.request.get(`${WEB}/docs/attachments/${F.RAtt}`, { maxRedirects: 0 });
    assert(att.status() !== 302 && att.status() < 500, `adjunto del restricted → ${att.status()} ${att.headers().location?.split('?')[0] || ''}`);
    out.push(`adjunto → ${att.status()}`);
    // Cards: cubierto en W8 (proyecto, "Ver todos", requisito y tarea no lo listan).
    return out.join('; ');
  });

  await step('W11', 'web: lock del servicio con su nombre y renovación/liberación del lock propio', async () => {
    assert(newId, 'sin documento de W3');
    // a) El servicio toma el lock: la persona ve su nombre, no "Otra persona".
    const l = cliJSON('docs', 'lock', newId);
    let svcNotice = '';
    try {
      await go(page, `${WEB}/docs/${newId}`);
      const notice = page.getByText(/lo está editando hasta las/).first();
      await notice.waitFor({ timeout: 20000 });
      svcNotice = await notice.innerText();
      assert(await page.getByRole('button', { name: 'Editar' }).isDisabled(), 'Editar habilitado con lock ajeno');
      await shot(page, 'web-lock-servicio');
    } finally {
      cli('docs', 'unlock', newId, '--session', l.sessionId);
    }
    assert(!/otra persona/i.test(svcNotice), `aviso: ${svcNotice}`);
    // b) Renovación (RENEW_INTERVAL_MS = 60 s) y liberación al salir.
    await go(page, `${WEB}/docs/${newId}`);
    await page.getByRole('button', { name: 'Editar' }).click({ timeout: 70000 });
    await page.getByText('Estás editando.', { exact: false }).waitFor();
    const before = lockOf(newId);
    assert(before, 'sin lock tras Editar');
    await page.waitForTimeout(65000);
    const after = lockOf(newId);
    assert(after && after.expiresAt > before.expiresAt, `no renovó: ${before.expiresAt} → ${after?.expiresAt}`);
    await go(page, `${WEB}/docs`); // pagehide/desmontaje → unlock con keepalive
    await page.waitForTimeout(2000);
    const gone = lockOf(newId);
    assert(!gone, `el lock sigue tomado al salir: ${JSON.stringify(gone)}`);
    return `servicio: "${svcNotice}"; renovado ${before.expiresAt} → ${after.expiresAt}; liberado al salir`;
  });

  await step('W12', 'web: conflicto mientras el humano edita (document_locked y version_conflict)', async () => {
    assert(newId, 'sin documento de W3');
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
    out.push(`put del servicio con lock ajeno: ${/document_locked/.test(lockedErr) ? 'document_locked' : lockedErr.slice(0, 80) || 'GUARDÓ'}`);
    const ta = page.getByLabel('Markdown', { exact: true });
    await ta.fill((await ta.inputValue()) + '\nTexto del humano.\n');
    await page.waitForTimeout(1000);
    const humanLock = lockOf(newId);
    assert(humanLock?.sessionId, 'el humano no tiene el lock tras Editar');
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
    await shot(page, 'web-editor-conflicto');
    const noticeText = (await notice.count()) ? await notice.innerText() : '';
    out.push(`guardado del humano → ${code || 'sin código'}; aviso: ${noticeText.slice(0, 160)}`);
    assert(['version_conflict', 'document_locked'].includes(code), out.join('; '));
    assert(noticeText, out.join('; '));
    await page.getByRole('button', { name: /^Guardar/ }).click();
    await page.waitForTimeout(2500);
    await go(page, `${WEB}/docs`);
    return out.join('; ');
  });

  await step('W13', 'web: subir adjunto desde el editor e insertarlo', async () => {
    assert(newId, 'sin documento de W3');
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
    await shot(page, 'web-adjunto-insertado');
    assert(nat > 0, 'la imagen insertada no carga en el visor');
    return `adjunto ${att} insertado y visible`;
  });

  await step('W14', 'web: visor seguro (script, javascript:, onerror), imagen 302 y enlaces', async () => {
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
    assert(!flag && w.dialogs.length === 0, `se ejecutó JS: __xss=${flag} dialogs=${w.dialogs}`);
    const nat = await page.locator(`img[src*="${D.arqAttachment}"]`).evaluate((i) => i.naturalWidth).catch(() => 0);
    assert(nat > 0, 'la imagen no cargó');
    await shot(page, 'web-visor-xss');
    const relHref = await page.locator('main a', { hasText: 'relativo' }).first().getAttribute('href').catch(() => null);
    assert(!relHref, `el enlace relativo quedó con href ${relHref}`);
    const [tab] = await Promise.all([ctx.waitForEvent('page', { timeout: 20000 }), page.getByRole('link', { name: 'arquitectura' }).click()]);
    await tab.waitForURL(new RegExp(`/docs/${D.arq}`));
    await tab.getByRole('heading', { name: D.arqTitle }).first().waitFor({ timeout: 30000 });
    await tab.close();
    return `sin ejecución; imagen ok; enlace absoluto abre otra pestaña; relativo sin href (${xss.path})`;
  });

  await step('W15', 'web: nota importada (sin frontmatter, imagen 302 y enlace interno) y responsive del visor (390 px)', async (bug) => {
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
    noPath(text, 'la nota importada');
    await shot(page, 'web-visor-importado');
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
        ['documento', `${WEB}/docs/${D.arq}`],
        ['buscador', `${WEB}/docs?proyecto=${D.project}`],
      ]) {
        await go(page, u);
        await page.waitForTimeout(3000);
        over[k] = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
        await shot(page, `web-${k}-390`);
      }
    } finally {
      await page.setViewportSize({ width: 1440, height: 900 });
    }
    // El layout general de la web (fuera del alcance de P-1: sidebar y barra quedan como hoy) tiene
    // un ancho mínimo que da scroll horizontal en cualquier pantalla a 390 px; se informa sin fallar.
    const wide = Object.values(over).some((v) => v > 0);
    return `imagen 302 (${nat}px); enlace "${D.impLinkText}" abre "${D.impLinkDstTitle}"; 390 px: ${wide ? `OBS scroll horizontal del layout general ${JSON.stringify(over)}` : 'sin scroll horizontal'}`;
  });

  if (withDown) {
    await step('W16', 'web: módulo caído → aviso y acciones deshabilitadas; vuelve', async () => {
      docker('stop', DOCS_CONTAINER);
      try {
        const t0 = Date.now();
        await go(page, `${WEB}/docs`);
        await page.getByText(/no está disponible/).first().waitFor({ timeout: 150000 });
        const ms = Date.now() - t0;
        const disabled = await searchBox().isDisabled();
        await shot(page, 'web-modulo-caido-docs');
        await go(page, `${WEB}/projects/${D.project}`);
        await page.getByText(/no está disponible/).first().waitFor({ timeout: 30000 });
        await shot(page, 'web-modulo-caido-ficha');
        assert(disabled, 'la búsqueda no se deshabilitó');
        return `aviso visible (unavailable a los ${Math.round(ms / 1000)} s); búsqueda deshabilitada`;
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
    log(`datos de la corrida: ${setupData()}`);
  } catch (e) {
    log(`datos de la corrida: FALLA ${redact(e.message, creds).split('\n')[0]}`);
  }
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
  cleanup();
}

const table = ['| id | escenario | estado | detalle |', '| --- | --- | --- | --- |', ...results.map((r) => `| ${r.id} | ${r.name} | ${r.status} | ${r.note.replace(/\|/g, '\\|')} |`)].join('\n');
fs.writeFileSync(path.join(OUT, 'resultados.md'), table + '\n');
log('\n' + table + `\n\nCapturas y resultados en ${OUT}`);
// 1: alguna falla. 2: nada falló pero quedaron escenarios sin probar (no es "verde").
// "bug jiku" (bugs conocidos de Jiku, ver KNOWN) no cuenta salvo con E2E_STRICT=1.
const failed = results.some((r) => r.status === 'falla' || (STRICT && r.status === 'bug jiku'));
process.exitCode = failed ? 1 : results.some((r) => r.status === 'no probado') ? 2 : 0;
