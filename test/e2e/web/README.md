# e2e en navegador: web y opus-web

Recorre con Playwright (Chromium) la integración de jiku-docs en la web interna y en opus-web,
con usuarios humanos reales del Zitadel hosteado, contra el stack local levantado.

## Correr

```sh
cd test/e2e/web
npm install                       # playwright; usa el Chromium de ~/.cache/ms-playwright
npx playwright install chromium   # solo si no está
E2E_CLI_DOCS="/ruta/a/as.sh docs" node run.mjs --down       # todo, con módulo caído
node run.mjs --only opus --down                              # solo opus-web, con módulo caído
node run.mjs --only web --steps W1,W11                        # depurar escenarios sueltos
```

- `--only opus|public|web`: una suite. `--down`: incluye el escenario de módulo caído (hace
  `docker stop`/`docker start` de `jiku-docs-local`; siempre lo vuelve a levantar). `--headed`.
  `--steps`: solo esos ids (los que dependen de otro escenario fallan; W1/O1 hacen el login).
- Credenciales: `.local/test-users.env` del repo (o `$TEST_USERS_ENV`) con `WEB_USER_LOGIN`,
  `WEB_USER_PASSWORD` (rol `user`) y `WEB_EXTERNAL_LOGIN`, `WEB_EXTERNAL_PASSWORD`
  (`external-user`). El script nunca las imprime y las tapa en las capturas.
- `E2E_CLI_DOCS` (antes `E2E_CLI_ADMIN`, que se sigue aceptando): comando que corre `jiku` como
  el machine user del **servicio** jiku-docs (`internal-app` + `docs`, clase `service`: lee y
  escribe todo). Prepara datos, cambia ACL, crea y revoca links y hace de "otra persona" en los
  conflictos (queda como autor con `via: service`). No hace falta ninguna persona `admin`. Sin él,
  los pasos que lo necesitan quedan "no probado". Un wrapper mínimo:

  ```sh
  #!/usr/bin/env bash
  # as.sh docs <args de jiku...>
  export JIKU_SERVERS=nats://localhost:4222 JIKU_INSTANCE=dev JIKU_ISSUER=https://id.grava.io \
    JIKU_CREDS=<jiku>/deploy/nats/creds/sentinel-client.creds JIKU_PROJECT_ID=<zitadel.project_id> \
    JIKU_CONFIG_DIR=<dir propio> JIKU_KEY_FILE=<jiku-docs>/.local/jiku_docs.service_user.json
  shift; exec <jiku-go>/bin/jiku "$@"
  ```

  Las personas de la web entran con usuario y contraseña (no necesitan token del CLI). Para usar
  las mismas personas desde el CLI (p. ej. `test/e2e/local-bus.sh`), `device-login.mjs` obtiene su
  token del device flow; ver `../README.md`.
- Salida: capturas y `resultados.md` en `.local/e2e-web/<fecha>/` (o `$E2E_OUT`). No guardar
  capturas fuera de `.local/` o de un directorio temporal.
- URLs: `E2E_WEB_URL` (3000), `E2E_OPUS_URL` (3001), `E2E_PUBLIC_URL` (`http://127.0.0.1:3100`).
- Código de salida: 0 todo ok, 1 alguna falla, 2 sin fallas pero con escenarios "no probado".
  Estado "bug jiku": el escenario encontró un bug conocido de Jiku (lista `KNOWN` en `run.mjs`,
  con el síntoma exacto que se verifica) y probó el resto; no cuenta como falla salvo con
  `E2E_STRICT=1`. Cuando el bug se arregla, el escenario vuelve a "ok" solo.
- La web habla con la api por **Server Actions** de Next (POST a la propia página con la cabecera
  `next-action`), no con `fetch` del navegador: los escenarios miran la UI, el cuerpo de esos POST
  y el estado en jiku-docs (por el CLI del servicio), no `/api/docs/*` en la red. Solo
  `/docs/attachments/{id}` y el `unlock` con keepalive son rutas HTTP del navegador.
- Los enlaces del contenido abren en otra pestaña; un enlace relativo a otro documento
  (`/docs/{id}`) queda sin `href` (el visor solo deja http(s), mailto y adjuntos).
- Si Zitadel pide cambiar la contraseña inicial de un usuario, la suite no la cambia: marca su
  parte como "no probado". Entrar una vez a mano, cambiarla y actualizar el `.env`.
- O10 quita y devuelve el `project-permissions` del externo sobre el proyecto (SQL en
  `jiku-local-database`, o `$E2E_JIKU_DB_CONTAINER`). W11 le da al interno una regla `manage`
  por usuario en `/clientes/exo/videoconferencia/` (vía `E2E_CLI_DOCS`) y al terminar devuelve
  las reglas que tenía la carpeta. Los ids (de Zitadel) salen de `user.zitadelId` de la sesión, o
  de `E2E_EXTERNAL_USER_ID` / `E2E_WEB_USER_ID`.

## Datos que espera

Los que deja `test/e2e/local-bus.sh` con el stack local: proyecto 43 (requisitos 54 internal y 15
public, tarea 2716), carpetas `/clientes/exo/videoconferencia/`, `/infra/` (`externalBlocked`) e
`/interno/`, y los documentos y adjuntos cuyos ids están en `D` al principio de `run.mjs`
(sobrescribibles por variables `E2E_*`), y notas importadas de HedgeDoc (W3, W4b): la carpeta
`/clientes/wecerty/wecerty/tecnico/`, la nota con imagen `Opus / Arquitectura del sistema` y el
enlace `Versión 1` de `Wecerty.md`. Además:

- El externo tiene que tener `project-permissions` sobre el proyecto 43
  (`user_project_permissions` en la base de Jiku).
- La fila de cada humano en `users` de core tiene que tener sus roles. Core solo la espeja en el
  evento del auth-callout (NATS) o cuando la api manda un **comando** en su nombre; un humano que
  solo navegó la web nunca la actualiza y jiku-docs le responde `caller_not_authorized`. Para
  forzar el espejo basta con una acción que sea comando (p. ej. suscribirse a un requisito).
