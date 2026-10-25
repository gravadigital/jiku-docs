# e2e en navegador: web y opus-web

Recorre con Playwright (Chromium) la integración de jiku-docs en la web interna y en opus-web,
con usuarios humanos reales del Zitadel hosteado, contra el stack local levantado.

## Correr

```sh
cd test/e2e/web
npm install                       # playwright; usa el Chromium de ~/.cache/ms-playwright
npx playwright install chromium   # solo si no está
E2E_CLI_ADMIN="/ruta/a/as.sh admin" node run.mjs            # todo
node run.mjs --only opus --down                              # solo opus-web, con módulo caído
```

- `--only opus|public|web`: una suite. `--down`: incluye el escenario de módulo caído (hace
  `docker stop`/`docker start` de `jiku-docs-local`; siempre lo vuelve a levantar). `--headed`.
- Credenciales: `.local/test-users.env` del repo (o `$TEST_USERS_ENV`) con `WEB_USER_LOGIN`,
  `WEB_USER_PASSWORD` (rol `user`) y `WEB_EXTERNAL_LOGIN`, `WEB_EXTERNAL_PASSWORD`
  (`external-user`). El script nunca las imprime y las tapa en las capturas.
- `E2E_CLI_ADMIN`: comando que corre `jiku` con un machine user `admin` (prepara datos, crea y
  revoca links, simula a otra identidad). Sin él, los pasos que lo necesitan quedan "no probado".
- Salida: capturas y `resultados.md` en `.local/e2e-web/<fecha>/` (o `$E2E_OUT`). No guardar
  capturas fuera de `.local/` o de un directorio temporal.
- URLs: `E2E_WEB_URL` (3000), `E2E_OPUS_URL` (3001), `E2E_PUBLIC_URL` (`http://127.0.0.1:3100`).

## Datos que espera

Los que deja `test/e2e/local-bus.sh` con el stack local: proyecto 43 (requisitos 54 internal y 15
public, tarea 2716), carpetas `/clientes/exo/videoconferencia/`, `/infra/` (`externalBlocked`) e
`/interno/`, y los documentos y adjuntos cuyos ids están en `D` al principio de `run.mjs`
(sobrescribibles por variables `E2E_*`). Además:

- El externo tiene que tener `project-permissions` sobre el proyecto 43
  (`user_project_permissions` en la base de Jiku).
- La fila de cada humano en `users` de core tiene que tener sus roles. Core solo la espeja en el
  evento del auth-callout (NATS) o cuando la api manda un **comando** en su nombre; un humano que
  solo navegó la web nunca la actualiza y jiku-docs le responde `caller_not_authorized`. Para
  forzar el espejo basta con una acción que sea comando (p. ej. suscribirse a un requisito).
