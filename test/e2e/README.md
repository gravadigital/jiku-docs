# e2e local

Dos recorridos contra el stack local levantado (Jiku con `deploy/local.sh up` y jiku-docs con
`docker compose -f docker-compose.local.yml up -d`):

- `local-bus.sh`: jiku-docs **por el bus**, con el CLI `jiku` de jiku-go.
- `web/`: la integración en la web interna y en opus-web, con Playwright (ver `web/README.md`).

Los dos usan las mismas tres identidades, y ninguna es un machine user de prueba:

| identidad  | quién                                               | cómo se autentica                          |
| ---------- | --------------------------------------------------- | ------------------------------------------ |
| `docs`     | el machine user del servicio jiku-docs (`internal-app` + `docs`; clase `service`: lee y escribe todo) | key file `.local/jiku_docs.service_user.json` |
| `user`     | una persona con rol `user` (`WEB_USER_*`)           | device flow, token en su `JIKU_CONFIG_DIR` |
| `external` | una persona con rol `external-user` (`WEB_EXTERNAL_*`) | device flow, token en su `JIKU_CONFIG_DIR` |

`docs` hace el bootstrap y todo lo que pide `manage` (carpetas, ACL, marcar `public`, `unlock
--force`, crear y revocar links). No hay una persona `admin`: lo que antes probaba un admin
humano lo prueba el servicio, que queda como autor con `via: service`. Las reglas de ACL por rol
`admin` se siguen creando (ejercitan la ACL), pero nadie de la corrida las usa.

## Preparar las personas (device flow, una vez)

Las credenciales de las personas van en `.local/test-users.env` (nunca versionado):
`WEB_USER_LOGIN`, `WEB_USER_PASSWORD`, `WEB_EXTERNAL_LOGIN`, `WEB_EXTERNAL_PASSWORD`. La
contraseña inicial de Zitadel tiene que estar cambiada (entrar una vez a mano).

Cada persona necesita su propio `JIKU_CONFIG_DIR` con un token del device flow. No usar la
config ni los tokens de `~/.config/jiku/` de quien corre la prueba: de ahí solo se **leen**
`zitadel.client_id` (la app nativa con Device Code) y `zitadel.project_id`.

Automático (Playwright completa la aprobación con las credenciales del `.env`):

```sh
cd test/e2e/web && npm install
node device-login.mjs USER     ../../../.local/cli/human-user
node device-login.mjs EXTERNAL ../../../.local/cli/human-external
```

A mano, para cada persona:

```sh
JIKU_CONFIG_DIR=.local/cli/human-user JIKU_SERVERS=nats://localhost:4222 JIKU_INSTANCE=dev \
JIKU_CREDS=../../jiku/deploy/nats/creds/sentinel-client.creds JIKU_ISSUER=https://id.grava.io \
JIKU_CLIENT_ID=<zitadel.client_id> JIKU_PROJECT_ID=<zitadel.project_id> \
  ../../jiku-go/bin/jiku login          # imprime una URL y un código en stderr: aprobar como esa persona
```

y verificar con `jiku whoami` y `jiku doctor` en el mismo entorno. La primera request de una
persona puede perder la carrera con su propio evento de autenticación (core todavía no tiene su
fila de `users`): reintentar. El token dura ~20 h y el CLI lo refresca solo con el refresh token.

## local-bus.sh

```sh
test/e2e/local-bus.sh                       # recorrido completo
E2E_DISRUPTIVE=1 test/e2e/local-bus.sh      # además: docker stop/start y deregister del módulo
```

Por defecto toma la key del servicio de `.local/jiku_docs.service_user.json` y los config dirs de
las personas de `.local/cli/human-user` y `.local/cli/human-external`
(`E2E_DOCS_KEY`, `E2E_USER_CONFIG_DIR`, `E2E_EXTERNAL_CONFIG_DIR`; el resto de las variables está
en la cabecera del script). Los user-id salen de `jiku whoami`.

- Todo lo que crea vive bajo `/e2e-<timestamp>/`. jiku-docs no borra carpetas: al terminar se le
  quitan las reglas que daban acceso, así las personas no ven en la web otra copia de `clientes`,
  `infra` e `interno` por corrida (`E2E_KEEP_VISIBLE=1` las deja).
- El externo es una persona real que puede tener ya `project-permissions` sobre el proyecto que
  elige el script: si lo tiene, se le quita durante la corrida y se le devuelve al terminar
  (también si el script se corta).
- Sale con 0 si todo pasó.
