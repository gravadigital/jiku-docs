# jiku docs sync: servicio de usuario de systemd

24 de octubre de 2026 · Lautaro Alvarez

Especificación de cómo se instala, arranca y opera el daemon `jiku docs sync` en Linux. Complementa la especificación técnica de `jiku-docs` v1 (sección 10.4, daemon de sincronización) y reemplaza, para Linux, la idea de un subcomando `jiku docs sync install`.

## 1. Resumen

El daemon corre como **servicio de usuario de systemd**, instalado por el paquete de `jiku-go`. Arranca al iniciar sesión, se detiene al cerrarla, se reinicia si se cae y se integra con systemd, journald, el llavero del sistema y las notificaciones de escritorio.

Para cada persona, la puesta en marcha queda en dos comandos:

```
jiku login
systemctl --user enable --now jiku-docs-sync
```

### Alcance

- Solo Linux con systemd. Todo el equipo usa Linux; macOS y Windows quedan fuera.
- No cambia el protocolo ni el comportamiento de sincronización definido en la especificación v1: cambia cómo se empaqueta, se configura y se opera el daemon.
- `jiku docs sync` en primer plano sigue funcionando igual, para depurar o para quien no quiera el servicio.

### Por qué servicio de usuario y no de sistema

El daemon actúa con la identidad de la persona (su login de Jiku) y escribe en su carpeta personal. Un servicio de usuario corre solo mientras esa persona tiene sesión abierta, con sus credenciales y sus permisos. Un servicio de sistema correría sin nadie logueado y necesitaría credenciales guardadas fuera de la sesión, que es justo lo que se quiere evitar.

## 2. Empaquetado

`jiku-go` se distribuye como paquete `.deb` y `.rpm`, generados con nfpm desde GoReleaser.

| Archivo instalado | Ruta |
| --- | --- |
| Binario | `/usr/bin/jiku` |
| Unit de usuario | `/usr/lib/systemd/user/jiku-docs-sync.service` |
| Configuración de ejemplo | `/usr/share/doc/jiku/docs-sync.example.toml` |

Reglas del paquete:

- **No habilita el servicio al instalar.** Cada persona lo habilita con `systemctl --user enable --now jiku-docs-sync`, porque antes tiene que hacer `jiku login`.
- **No reinicia servicios de usuario al actualizar.** Un paquete no puede operar sobre los managers de usuario de cada sesión. El script posterior a la instalación muestra un mensaje indicando que, para tomar la versión nueva, se ejecute `systemctl --user daemon-reload && systemctl --user restart jiku-docs-sync` (o que se cierre y abra sesión).
- **Al desinstalar** no borra configuración, estado ni documentos del usuario.

## 3. Unit

```ini
[Unit]
Description=Sincronización de jiku docs
Documentation=https://github.com/gravadigital/jiku-go
StartLimitIntervalSec=300
StartLimitBurst=5

[Service]
Type=notify
ExecStart=/usr/bin/jiku docs sync
Restart=on-failure
RestartSec=10
RestartPreventExitStatus=77 78
WatchdogSec=60
TimeoutStopSec=30

# Aislamiento: solo puede escribir en su carpeta, su estado y su configuración
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=%h/JikuDocs %S/jiku-docs %E/jiku
PrivateTmp=yes

[Install]
WantedBy=default.target
```

| Directiva | Motivo |
| --- | --- |
| `Type=notify` | El daemon avisa cuándo está realmente listo (sección 4); `systemctl --user status` refleja el estado real |
| `Restart=on-failure` + `RestartSec=10` | Se reinicia solo ante un fallo, sin bucles rápidos |
| `StartLimitIntervalSec` / `StartLimitBurst` | Si falla 5 veces en 5 minutos, systemd deja de reiniciarlo y queda en estado `failed` |
| `RestartPreventExitStatus=77 78` | No reiniciar ante errores que un reinicio no arregla: credenciales inválidas (77) o configuración inválida (78) |
| `WatchdogSec=60` | Si el daemon se cuelga sin morir, systemd lo reinicia |
| `TimeoutStopSec=30` | Tiempo para terminar de subir cambios pendientes al detenerse |
| Aislamiento | Todo el sistema de archivos queda de solo lectura salvo la carpeta de documentos, el estado (`%S` = `~/.local/state`) y la configuración (`%E` = `~/.config`) |

### Carpeta de documentos distinta de la default

`ReadWritePaths` incluye `~/JikuDocs`. Quien configure otra carpeta (sección 5) tiene que agregarla con un drop-in, que es el mecanismo estándar de systemd para ajustar un unit sin editarlo:

```
systemctl --user edit jiku-docs-sync
```

```ini
[Service]
ReadWritePaths=%h/Documentos/Jiku
```

Si la carpeta configurada no es escribible, el daemon termina con código 78 y un mensaje que explica cómo agregar el drop-in.

### Compatibilidad del aislamiento

Algunas opciones de aislamiento en servicios de usuario dependen de la versión de systemd y de que el kernel permita namespaces de usuario. El unit se valida en las distribuciones que usa el equipo; si alguna opción no está disponible en una de ellas, se quita esa línea del unit para todos, en lugar de mantener variantes.

## 4. Integración con systemd en el binario

El daemon usa el protocolo `sd_notify` (en Go, paquete `github.com/coreos/go-systemd/v22/daemon`). Si no corre bajo systemd (variable `NOTIFY_SOCKET` ausente), todas estas llamadas son no-ops y el comportamiento en primer plano no cambia.

| Momento | Mensaje |
| --- | --- |
| Conectado al bus y terminada la reconciliación inicial | `READY=1` |
| Cada cambio de estado relevante | `STATUS=<texto>` (ver abajo) |
| Cada `WatchdogSec / 2` (30 s), mientras el loop principal responde | `WATCHDOG=1` |
| Al recibir `SIGTERM` | `STOPPING=1`, luego termina de subir cambios pendientes y sale |

El latido del watchdog se emite desde el loop principal, no desde una goroutine independiente: si el loop se bloquea, el latido se corta y systemd reinicia el proceso.

Textos de `STATUS` (se ven en `systemctl --user status jiku-docs-sync`):

- `Sincronizado · 1.240 documentos · último cambio 14:32`
- `Subiendo 3 cambios`
- `Sin conexión, reintentando`
- `Módulo de documentación no disponible`
- `2 conflictos pendientes`

### Códigos de salida

| Código | Significado | ¿systemd reinicia? |
| --- | --- | --- |
| 0 | Detención normal | No |
| 1 | Error inesperado | Sí |
| 77 | Credenciales inválidas o vencidas (hay que ejecutar `jiku login`) | No |
| 78 | Configuración inválida o carpeta no escribible | No |

### Una sola instancia

El daemon toma un lock exclusivo (`flock`) sobre `~/.local/state/jiku-docs/sync.lock`. Si ya hay una instancia corriendo (por ejemplo, el servicio y alguien que ejecuta `jiku docs sync` a mano), la segunda termina con un mensaje claro y código 1, sin tocar archivos.

## 5. Configuración

La configuración vive en un archivo, no en argumentos del unit, para que el unit sea idéntico para todos.

Ruta: `~/.config/jiku/docs-sync.toml` (`$XDG_CONFIG_HOME/jiku/docs-sync.toml`).

```toml
# Carpeta local donde se espejan los documentos
root = "~/JikuDocs"

# Carpetas del árbol a sincronizar. Vacío = todo lo legible
include = ["/clientes/acme/", "/infra/", "/buenas-practicas/"]
exclude = ["/clientes/acme/archivo/"]

# Espera sin cambios antes de subir un archivo modificado
debounce = "2s"

# Notificaciones de escritorio
notifications = true

# debug | info | warn | error
log_level = "info"
```

Reglas:

- El archivo es opcional; sin él se usan los valores de arriba con `include` vacío.
- Precedencia: flags de línea de comandos, después variables de entorno (`JIKU_DOCS_SYNC_ROOT`, etc.), después el archivo.
- Una clave desconocida o un valor inválido es error: el daemon termina con código 78 indicando la línea.
- Cambiar la configuración requiere `systemctl --user restart jiku-docs-sync`.

## 6. Rutas

Se siguen las convenciones XDG. La carpeta de documentos contiene solo documentos.

| Qué | Ruta | Variable XDG |
| --- | --- | --- |
| Configuración | `~/.config/jiku/docs-sync.toml` | `XDG_CONFIG_HOME` |
| Estado (SQLite con versiones, checksums, `lastSeq`) | `~/.local/state/jiku-docs/state.db` | `XDG_STATE_HOME` |
| Lock de instancia única | `~/.local/state/jiku-docs/sync.lock` | `XDG_STATE_HOME` |
| Documentos | `~/JikuDocs/` (configurable) | — |

**Migración:** la especificación v1 ubicaba el estado en `~/JikuDocs/.jiku-docs/state.db`. Si el daemon encuentra esa ruta y no existe la nueva, mueve el archivo a la ruta nueva al arrancar y borra la carpeta vieja.

## 7. Credenciales

- `jiku login` guarda el refresh token en el llavero del sistema mediante la API Secret Service por D-Bus (GNOME Keyring, KWallet). En Go, `github.com/zalando/go-keyring` cubre este caso.
- El daemon lee el token del llavero al arrancar. Si el llavero está bloqueado, espera a que se desbloquee (lo normal es que se desbloquee al iniciar sesión) y mientras tanto reporta `STATUS=Esperando el llavero del sistema`.
- Si no hay Secret Service disponible (por ejemplo, una máquina sin entorno gráfico), `jiku login` guarda el token en `~/.config/jiku/credentials` con permisos `0600` y muestra una advertencia.
- Si el token es rechazado (vencido o revocado), el daemon notifica (sección 9) y termina con código 77. Después de un `jiku login` exitoso, se vuelve a arrancar con `systemctl --user restart jiku-docs-sync`; `jiku login` puede hacerlo solo si detecta el servicio habilitado.

## 8. Logs

- El daemon escribe en stderr; journald lo captura sin configuración adicional. No hay archivos de log propios.
- Cada línea lleva el prefijo de prioridad de syslog que journald interpreta (`<3>` error, `<4>` advertencia, `<6>` info, `<7>` debug), para poder filtrar con `journalctl -p`.
- Nunca se escribe contenido de documentos en los logs: solo rutas, ids, versiones y códigos de error.

Consultas habituales:

```
journalctl --user -u jiku-docs-sync -f          # seguir en vivo
journalctl --user -u jiku-docs-sync -p warning  # solo advertencias y errores
journalctl --user -u jiku-docs-sync --since today
```

## 9. Notificaciones de escritorio

El daemon notifica por D-Bus (`org.freedesktop.Notifications`), que es lo que muestra los avisos nativos en GNOME y KDE. Si no hay bus de sesión, las notificaciones se omiten y el hecho queda solo en el log.

| Evento | Notificación | Urgencia |
| --- | --- | --- |
| Conflicto (versión remota guardada como copia) | "Conflicto en `<ruta>`: se guardó la versión remota como copia" | Normal |
| Credenciales vencidas o revocadas | "jiku docs se detuvo: ejecutá `jiku login`" | Crítica |
| Módulo no disponible durante más de 5 minutos | "La documentación no está disponible; los cambios se subirán al volver" | Normal |
| Módulo desinstalado (`not_installed`) | "El módulo de documentación ya no está instalado; la sincronización se detuvo" | Crítica |
| Archivo local rechazado (sin permiso de escritura, extensión no `.md`, frontmatter inválido) | "No se pudo subir `<ruta>`: <motivo>" | Normal |

Para no saturar, cada tipo de evento se notifica como máximo una vez cada 5 minutos por archivo; los repetidos se acumulan en una sola notificación ("y 4 archivos más"). Con `notifications = false` no se envía ninguna.

## 10. Comando `jiku docs sync status`

Muestra el estado sin depender de systemd, leyendo la base de estado:

```
$ jiku docs sync status
Servicio:      activo (systemd --user)
Carpeta:       /home/lautaro/JikuDocs
Documentos:    1.240
Último cambio: hoy 14:32 (subido)
Pendientes:    0
Conflictos:    2
  /clientes/acme/pzt/reuniones/2026-10-24.remoto-v8.md
  /infra/servidores/staging.remoto-v12.md
```

La línea "Servicio" se obtiene consultando a systemd por D-Bus (`org.freedesktop.systemd1`, manager de usuario); si no está disponible, se omite.

## 11. Operación para el usuario

| Tarea | Comando |
| --- | --- |
| Primera vez | `jiku login` y `systemctl --user enable --now jiku-docs-sync` |
| Ver estado | `jiku docs sync status` o `systemctl --user status jiku-docs-sync` |
| Ver logs | `journalctl --user -u jiku-docs-sync -f` |
| Aplicar un cambio de configuración | `systemctl --user restart jiku-docs-sync` |
| Detener temporalmente | `systemctl --user stop jiku-docs-sync` |
| Desactivar | `systemctl --user disable --now jiku-docs-sync` |
| Después de actualizar `jiku-go` | `systemctl --user daemon-reload && systemctl --user restart jiku-docs-sync` |

### Problemas frecuentes

| Síntoma | Causa probable | Solución |
| --- | --- | --- |
| Servicio en `failed` con código 77 | Credenciales vencidas | `jiku login` y reiniciar el servicio |
| Servicio en `failed` con código 78 | Configuración inválida o carpeta no escribible | Revisar el log; corregir `docs-sync.toml` o agregar el drop-in de `ReadWritePaths` |
| Servicio en `failed` sin código 77/78 | Falló 5 veces en 5 minutos | Revisar `journalctl`; reiniciar con `systemctl --user reset-failed jiku-docs-sync` y `start` |
| "Ya hay una instancia corriendo" | Se ejecutó `jiku docs sync` a mano con el servicio activo | Usar uno u otro |
| Queda en `Esperando el llavero del sistema` | Llavero bloqueado | Desbloquearlo (normalmente se hace al iniciar sesión) |

## 12. Casos de prueba

| # | Escenario | Resultado esperado |
| --- | --- | --- |
| 1 | Instalar el paquete | Binario y unit en sus rutas; el servicio no queda habilitado |
| 2 | `enable --now` con login válido | `systemctl --user status` muestra `active (running)` recién después de la reconciliación inicial, con un `STATUS` legible |
| 3 | Cerrar sesión y volver a entrar | El servicio se detiene al cerrar y arranca solo al entrar |
| 4 | Matar el proceso con `SIGKILL` | systemd lo reinicia a los 10 s |
| 5 | Bloquear el loop principal (prueba con flag de depuración) | El watchdog lo reinicia en menos de 60 s |
| 6 | `systemctl --user stop` con cambios pendientes | Los cambios se suben antes de terminar (dentro de 30 s) |
| 7 | Token revocado en Zitadel | Notificación crítica, salida con código 77, sin reinicios |
| 8 | `docs-sync.toml` con una clave desconocida | Salida con código 78 indicando la línea, sin reinicios |
| 9 | `root` en una carpeta no listada en `ReadWritePaths` | Salida con código 78 con la instrucción del drop-in |
| 10 | Ejecutar `jiku docs sync` a mano con el servicio activo | La segunda instancia termina sin tocar archivos |
| 11 | Estado en la ruta vieja (`~/JikuDocs/.jiku-docs/`) | Se migra a `~/.local/state/jiku-docs/` y la carpeta vieja desaparece |
| 12 | Sin Secret Service | `jiku login` guarda el token en archivo `0600` con advertencia; el daemon funciona |
| 13 | Sin bus de sesión | El daemon funciona; las notificaciones se omiten y quedan en el log |
| 14 | 20 conflictos en un minuto | Una notificación agrupada, no veinte |
| 15 | Correr fuera de systemd | `jiku docs sync` en primer plano funciona igual; las llamadas a `sd_notify` no tienen efecto |

## 13. Revisión acordada (2026-10-25) — manda sobre las secciones anteriores

Ajustes acordados con el dueño después de revisar la propuesta contra la implementación de jiku-go y contra
Ubuntu 26.04 / systemd 259:

1. **Sin aislamiento de sistema de archivos.** Se probó en Ubuntu 26.04 (systemd 259,
   `kernel.apparmor_restrict_unprivileged_userns=1`): en un servicio de usuario, `ProtectSystem=strict` y
   `ReadWritePaths` se ignoran en silencio (el proceso escribió fuera de las rutas permitidas). El unit conserva solo
   `NoNewPrivileges=yes`. Se eliminan `ProtectSystem`, `ReadWritePaths`, `PrivateTmp`, el drop-in de la sección 3, el
   código 78 por "carpeta no escribible" (78 queda para configuración inválida) y el caso de prueba 9.
2. **`READY=1` temprano.** Se envía apenas el daemon cargó la configuración, tomó el lock y armó el watcher de
   archivos, aunque esté offline. Conexión al bus y reconciliación se informan por `STATUS`
   (`Sin conexión, reintentando`, `Reconciliando…`). Así un arranque sin red o una reconciliación larga no superan el
   tiempo de arranque de systemd. Caso 2: `active (running)` al quedar listo localmente, con `STATUS` que refleja la
   reconciliación.
3. **Un solo entorno.** Un único unit `jiku-docs-sync.service` (sin plantilla) que usa la misma configuración que
   `jiku` sin flags (producción para el equipo). Sin pasos extra: `jiku login` y
   `systemctl --user enable --now jiku-docs-sync`. Para probar contra otro entorno se usa `jiku docs sync` en primer
   plano. El estado se guarda por instancia (`~/.local/state/jiku-docs/<instancia>/`) para que una prueba en otro
   entorno nunca pise el de producción.
4. **Configuración en YAML**, como el resto de jiku-go: `~/.config/jiku/docs-sync.yaml` (mismas claves que el ejemplo
   TOML de la sección 5). Sin dependencia nueva.
5. **Estado en XDG, documentos en la carpeta.** `state.db` y el lock van a `~/.local/state/jiku-docs/<instancia>/`;
   la carpeta de documentos que dejaron de ser legibles va a `<root>/.sin-acceso/` (son documentos de la persona, no
   estado). El estado recuerda carpeta e identidad: si cambia `root` o la identidad, no reutiliza un estado ajeno.
   Migración desde `<root>/.jiku-docs/` como dice la sección 6.
6. **Credenciales en archivo en esta etapa.** Se mantiene el archivo `0600` actual de jiku-go
   (`~/.config/jiku/tokens-<instancia>.json`); el llavero del sistema (sección 7) queda para más adelante, por
   estabilidad. Se agrega un lock entre procesos alrededor del refresco del token, para que el daemon y un `jiku` a mano
   no se invaliden el refresh token si Zitadel lo rota.
7. **Código 77 solo ante rechazo real** (`invalid_grant` o equivalente de Zitadel). Zitadel inalcanzable o corte de
   red: el daemon queda offline y reintenta.
8. **Módulo desinstalado:** el proceso sigue vivo sin sincronizar, observando el registro, con
   `STATUS=Módulo de documentación no instalado`, y retoma solo cuando la clave vuelve. Notificación crítica una vez.
9. **Detalles:** prefijo de prioridad de syslog solo bajo journald (`JOURNAL_STREAM`); después de actualizar el
   paquete basta `systemctl --user restart jiku-docs-sync` (`daemon-reload` solo si cambió el unit); `sd_notify`
   implementado sin dependencias; empaquetado `.deb`/`.rpm` con GoReleaser + nfpm, validado localmente.
