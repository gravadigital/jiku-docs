# jiku docs sync: systemd user service

October 24, 2026 · Lautaro Alvarez

Specification of how the `jiku docs sync` daemon is installed, started and operated on Linux. It complements the `jiku-docs` v1 technical specification (section 10.4, sync daemon) and replaces, for Linux, the idea of a `jiku docs sync install` subcommand.

The texts the daemon shows (systemd `STATUS`, desktop notifications, `jiku docs sync status` output) are output of `jiku-go`, a separate repository whose interface is in Spanish; they are quoted literally in Spanish, with their English meaning.

## 1. Summary

The daemon runs as a **systemd user service**, installed by the `jiku-go` package. It starts at login, stops at logout, restarts if it crashes, and integrates with systemd, journald, the system keyring and desktop notifications.

For each person, getting started takes two commands:

```
jiku login
systemctl --user enable --now jiku-docs-sync
```

### Scope

- Linux with systemd only. The whole team uses Linux; macOS and Windows are out of scope.
- It does not change the protocol or the sync behaviour defined in the v1 specification: it changes how the daemon is packaged, configured and operated.
- `jiku docs sync` in the foreground keeps working the same, for debugging or for whoever does not want the service.

### Why a user service and not a system service

The daemon acts with the person's identity (their Jiku login) and writes into their home folder. A user service runs only while that person has an open session, with their credentials and permissions. A system service would run with nobody logged in and would need credentials stored outside the session, which is exactly what we want to avoid.

## 2. Packaging

`jiku-go` is distributed as `.deb` and `.rpm` packages, built with nfpm from GoReleaser.

| Installed file | Path |
| --- | --- |
| Binary | `/usr/bin/jiku` |
| User unit | `/usr/lib/systemd/user/jiku-docs-sync.service` |
| Example configuration | `/usr/share/doc/jiku/docs-sync.example.toml` |

Package rules:

- **It does not enable the service on install.** Each person enables it with `systemctl --user enable --now jiku-docs-sync`, because they first have to run `jiku login`.
- **It does not restart user services on upgrade.** A package cannot operate on the user managers of each session. The post-install script shows a message saying that, to pick up the new version, run `systemctl --user daemon-reload && systemctl --user restart jiku-docs-sync` (or log out and back in).
- **On uninstall** it does not delete the user's configuration, state or documents.

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

# Isolation: it can only write to its folder, its state and its configuration
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=%h/JikuDocs %S/jiku-docs %E/jiku
PrivateTmp=yes

[Install]
WantedBy=default.target
```

(`Description` is the unit's Spanish label in `jiku-go`: "jiku docs sync".)

| Directive | Reason |
| --- | --- |
| `Type=notify` | The daemon signals when it is really ready (section 4); `systemctl --user status` reflects the real state |
| `Restart=on-failure` + `RestartSec=10` | It restarts only on failure, without fast loops |
| `StartLimitIntervalSec` / `StartLimitBurst` | If it fails 5 times in 5 minutes, systemd stops restarting it and it stays in the `failed` state |
| `RestartPreventExitStatus=77 78` | Do not restart on errors a restart does not fix: invalid credentials (77) or invalid configuration (78) |
| `WatchdogSec=60` | If the daemon hangs without dying, systemd restarts it |
| `TimeoutStopSec=30` | Time to finish uploading pending changes when stopping |
| Isolation | The whole file system is read-only except the documents folder, the state (`%S` = `~/.local/state`) and the configuration (`%E` = `~/.config`) |

### Documents folder other than the default

`ReadWritePaths` includes `~/JikuDocs`. Whoever configures another folder (section 5) has to add it with a drop-in, which is the standard systemd mechanism for adjusting a unit without editing it:

```
systemctl --user edit jiku-docs-sync
```

```ini
[Service]
ReadWritePaths=%h/Documentos/Jiku
```

If the configured folder is not writable, the daemon exits with code 78 and a message explaining how to add the drop-in.

### Isolation compatibility

Some isolation options in user services depend on the systemd version and on the kernel allowing user namespaces. The unit is validated on the distributions the team uses; if an option is not available on one of them, that line is removed from the unit for everyone, instead of maintaining variants.

## 4. systemd integration in the binary

The daemon uses the `sd_notify` protocol (in Go, package `github.com/coreos/go-systemd/v22/daemon`). If it does not run under systemd (`NOTIFY_SOCKET` variable absent), all these calls are no-ops and the foreground behaviour does not change.

| Moment | Message |
| --- | --- |
| Connected to the bus and initial reconciliation finished | `READY=1` |
| Each relevant state change | `STATUS=<text>` (see below) |
| Every `WatchdogSec / 2` (30 s), while the main loop responds | `WATCHDOG=1` |
| On receiving `SIGTERM` | `STOPPING=1`, then it finishes uploading pending changes and exits |

The watchdog heartbeat is emitted from the main loop, not from an independent goroutine: if the loop blocks, the heartbeat stops and systemd restarts the process.

`STATUS` texts (seen in `systemctl --user status jiku-docs-sync`):

- `Sincronizado · 1.240 documentos · último cambio 14:32` (synced · 1,240 documents · last change 14:32)
- `Subiendo 3 cambios` (uploading 3 changes)
- `Sin conexión, reintentando` (offline, retrying)
- `Módulo de documentación no disponible` (documentation module unavailable)
- `2 conflictos pendientes` (2 pending conflicts)

### Exit codes

| Code | Meaning | Does systemd restart? |
| --- | --- | --- |
| 0 | Normal stop | No |
| 1 | Unexpected error | Yes |
| 77 | Invalid or expired credentials (`jiku login` must be run) | No |
| 78 | Invalid configuration or non-writable folder | No |

### A single instance

The daemon takes an exclusive lock (`flock`) on `~/.local/state/jiku-docs/sync.lock`. If an instance is already running (for example, the service and someone running `jiku docs sync` by hand), the second one exits with a clear message and code 1, without touching files.

## 5. Configuration

The configuration lives in a file, not in unit arguments, so that the unit is identical for everyone.

Path: `~/.config/jiku/docs-sync.toml` (`$XDG_CONFIG_HOME/jiku/docs-sync.toml`).

```toml
# Local folder where the documents are mirrored
root = "~/JikuDocs"

# Folders of the tree to sync. Empty = everything readable
include = ["/clients/acme/", "/infra/", "/best-practices/"]
exclude = ["/clients/acme/archive/"]

# Wait without changes before uploading a modified file
debounce = "2s"

# Desktop notifications
notifications = true

# debug | info | warn | error
log_level = "info"
```

Rules:

- The file is optional; without it the values above are used with an empty `include`.
- Precedence: command-line flags, then environment variables (`JIKU_DOCS_SYNC_ROOT`, etc.), then the file.
- An unknown key or an invalid value is an error: the daemon exits with code 78 indicating the line.
- Changing the configuration requires `systemctl --user restart jiku-docs-sync`.

## 6. Paths

The XDG conventions are followed. The documents folder contains only documents.

| What | Path | XDG variable |
| --- | --- | --- |
| Configuration | `~/.config/jiku/docs-sync.toml` | `XDG_CONFIG_HOME` |
| State (SQLite with versions, checksums, `lastSeq`) | `~/.local/state/jiku-docs/state.db` | `XDG_STATE_HOME` |
| Single-instance lock | `~/.local/state/jiku-docs/sync.lock` | `XDG_STATE_HOME` |
| Documents | `~/JikuDocs/` (configurable) | — |

**Migration:** the v1 specification placed the state in `~/JikuDocs/.jiku-docs/state.db`. If the daemon finds that path and the new one does not exist, it moves the file to the new path on startup and deletes the old folder.

## 7. Credentials

- `jiku login` stores the refresh token in the system keyring through the Secret Service API over D-Bus (GNOME Keyring, KWallet). In Go, `github.com/zalando/go-keyring` covers this case.
- The daemon reads the token from the keyring on startup. If the keyring is locked, it waits for it to be unlocked (normally it is unlocked at login) and meanwhile reports `STATUS=Esperando el llavero del sistema` (waiting for the system keyring).
- If no Secret Service is available (for example, a machine without a graphical environment), `jiku login` stores the token in `~/.config/jiku/credentials` with `0600` permissions and shows a warning.
- If the token is rejected (expired or revoked), the daemon notifies (section 9) and exits with code 77. After a successful `jiku login`, it is started again with `systemctl --user restart jiku-docs-sync`; `jiku login` can do it by itself if it detects the service is enabled.

## 8. Logs

- The daemon writes to stderr; journald captures it without additional configuration. There are no log files of its own.
- Each line carries the syslog priority prefix that journald interprets (`<3>` error, `<4>` warning, `<6>` info, `<7>` debug), so that it can be filtered with `journalctl -p`.
- Document content is never written to the logs: only paths, ids, versions and error codes.

Common queries:

```
journalctl --user -u jiku-docs-sync -f          # follow live
journalctl --user -u jiku-docs-sync -p warning  # warnings and errors only
journalctl --user -u jiku-docs-sync --since today
```

## 9. Desktop notifications

The daemon notifies over D-Bus (`org.freedesktop.Notifications`), which is what shows native notices in GNOME and KDE. If there is no session bus, notifications are skipped and the fact remains only in the log.

| Event | Notification | Urgency |
| --- | --- | --- |
| Conflict (remote version saved as a copy) | "Conflicto en `<ruta>`: se guardó la versión remota como copia" (conflict in `<path>`: the remote version was saved as a copy) | Normal |
| Expired or revoked credentials | "jiku docs se detuvo: ejecutá `jiku login`" (jiku docs stopped: run `jiku login`) | Critical |
| Module unavailable for more than 5 minutes | "La documentación no está disponible; los cambios se subirán al volver" (documentation is unavailable; changes will be uploaded when it comes back) | Normal |
| Module uninstalled (`not_installed`) | "El módulo de documentación ya no está instalado; la sincronización se detuvo" (the documentation module is no longer installed; sync stopped) | Critical |
| Local file rejected (no write permission, extension other than `.md`, invalid frontmatter) | "No se pudo subir `<ruta>`: <motivo>" (could not upload `<path>`: <reason>) | Normal |

To avoid flooding, each event type is notified at most once every 5 minutes per file; repeats are accumulated in a single notification ("y 4 archivos más", and 4 more files). With `notifications = false` none is sent.

## 10. `jiku docs sync status` command

Shows the state without depending on systemd, reading the state database:

```
$ jiku docs sync status
Servicio:      activo (systemd --user)
Carpeta:       /home/lautaro/JikuDocs
Documentos:    1.240
Último cambio: hoy 14:32 (subido)
Pendientes:    0
Conflictos:    2
  /clients/acme/pzt/meetings/2026-10-24.remoto-v8.md
  /infra/servers/staging.remoto-v12.md
```

(Labels, in order: service — active; folder; documents; last change — today 14:32, uploaded; pending; conflicts. The `.remoto-vN` suffix, "remote", marks the copy of the remote version saved on a conflict.)

The "Servicio" (service) line is obtained by querying systemd over D-Bus (`org.freedesktop.systemd1`, user manager); if it is not available, it is omitted.

## 11. Operation for the user

| Task | Command |
| --- | --- |
| First time | `jiku login` and `systemctl --user enable --now jiku-docs-sync` |
| See the state | `jiku docs sync status` or `systemctl --user status jiku-docs-sync` |
| See the logs | `journalctl --user -u jiku-docs-sync -f` |
| Apply a configuration change | `systemctl --user restart jiku-docs-sync` |
| Stop temporarily | `systemctl --user stop jiku-docs-sync` |
| Disable | `systemctl --user disable --now jiku-docs-sync` |
| After upgrading `jiku-go` | `systemctl --user daemon-reload && systemctl --user restart jiku-docs-sync` |

### Common problems

| Symptom | Probable cause | Solution |
| --- | --- | --- |
| Service in `failed` with code 77 | Expired credentials | `jiku login` and restart the service |
| Service in `failed` with code 78 | Invalid configuration or non-writable folder | Check the log; fix `docs-sync.toml` or add the `ReadWritePaths` drop-in |
| Service in `failed` without code 77/78 | It failed 5 times in 5 minutes | Check `journalctl`; restart with `systemctl --user reset-failed jiku-docs-sync` and `start` |
| "Ya hay una instancia corriendo" (an instance is already running) | `jiku docs sync` was run by hand with the service active | Use one or the other |
| Stuck at `Esperando el llavero del sistema` (waiting for the system keyring) | Keyring locked | Unlock it (normally done at login) |

## 12. Test cases

| # | Scenario | Expected result |
| --- | --- | --- |
| 1 | Install the package | Binary and unit at their paths; the service is not enabled |
| 2 | `enable --now` with a valid login | `systemctl --user status` shows `active (running)` only after the initial reconciliation, with a readable `STATUS` |
| 3 | Log out and back in | The service stops at logout and starts by itself at login |
| 4 | Kill the process with `SIGKILL` | systemd restarts it after 10 s |
| 5 | Block the main loop (test with a debug flag) | The watchdog restarts it in less than 60 s |
| 6 | `systemctl --user stop` with pending changes | The changes are uploaded before exiting (within 30 s) |
| 7 | Token revoked in Zitadel | Critical notification, exit with code 77, no restarts |
| 8 | `docs-sync.toml` with an unknown key | Exit with code 78 indicating the line, no restarts |
| 9 | `root` in a folder not listed in `ReadWritePaths` | Exit with code 78 with the drop-in instruction |
| 10 | Run `jiku docs sync` by hand with the service active | The second instance exits without touching files |
| 11 | State at the old path (`~/JikuDocs/.jiku-docs/`) | It is migrated to `~/.local/state/jiku-docs/` and the old folder disappears |
| 12 | No Secret Service | `jiku login` stores the token in a `0600` file with a warning; the daemon works |
| 13 | No session bus | The daemon works; notifications are skipped and remain in the log |
| 14 | 20 conflicts in one minute | One grouped notification, not twenty |
| 15 | Running outside systemd | `jiku docs sync` in the foreground works the same; the `sd_notify` calls have no effect |

## 13. Agreed review (2026-10-25) — prevails over the previous sections

Adjustments agreed with the owner after reviewing the proposal against the jiku-go implementation and against
Ubuntu 26.04 / systemd 259:

1. **No file system isolation.** Tested on Ubuntu 26.04 (systemd 259,
   `kernel.apparmor_restrict_unprivileged_userns=1`): in a user service, `ProtectSystem=strict` and
   `ReadWritePaths` are silently ignored (the process wrote outside the allowed paths). The unit keeps only
   `NoNewPrivileges=yes`. `ProtectSystem`, `ReadWritePaths`, `PrivateTmp`, the drop-in of section 3, exit code 78 for
   "non-writable folder" (78 remains for invalid configuration) and test case 9 are removed.
2. **Early `READY=1`.** It is sent as soon as the daemon has loaded the configuration, taken the lock and set up the
   file watcher, even if offline. Bus connection and reconciliation are reported through `STATUS`
   (`Sin conexión, reintentando` — offline, retrying; `Reconciliando…` — reconciling). This way a start without
   network or a long reconciliation does not exceed systemd's start timeout. Case 2: `active (running)` once ready
   locally, with a `STATUS` that reflects the reconciliation.
3. **A single environment.** A single `jiku-docs-sync.service` unit (no template) that uses the same configuration as
   `jiku` without flags (production for the team). No extra steps: `jiku login` and
   `systemctl --user enable --now jiku-docs-sync`. To test against another environment, `jiku docs sync` is used in the
   foreground. State is stored per instance (`~/.local/state/jiku-docs/<instance>/`) so that a test in another
   environment never overwrites the production one.
4. **Configuration in YAML**, like the rest of jiku-go: `~/.config/jiku/docs-sync.yaml` (same keys as the TOML
   example in section 5). No new dependency.
5. **State in XDG, documents in the folder.** `state.db` and the lock go to `~/.local/state/jiku-docs/<instance>/`;
   the folder for documents that stopped being readable goes to `<root>/.sin-acceso/` ("no access"; they are the
   person's documents, not state). The state remembers folder and identity: if `root` or the identity changes, it does
   not reuse someone else's state. Migration from `<root>/.jiku-docs/` as section 6 says.
6. **Credentials in a file at this stage.** The current jiku-go `0600` file is kept
   (`~/.config/jiku/tokens-<instance>.json`); the system keyring (section 7) is left for later, for
   stability. An inter-process lock is added around the token refresh, so that the daemon and a `jiku` run by hand
   do not invalidate each other's refresh token if Zitadel rotates it.
7. **Code 77 only on a real rejection** (`invalid_grant` or Zitadel's equivalent). Zitadel unreachable or a network
   outage: the daemon goes offline and retries.
8. **Module uninstalled:** the process stays alive without syncing, watching the registry, with
   `STATUS=Módulo de documentación no instalado` (documentation module not installed), and resumes by itself when the
   key comes back. Critical notification once.
9. **Details:** syslog priority prefix only under journald (`JOURNAL_STREAM`); after upgrading the
   package `systemctl --user restart jiku-docs-sync` is enough (`daemon-reload` only if the unit changed); `sd_notify`
   implemented without dependencies; `.deb`/`.rpm` packaging with GoReleaser + nfpm, validated locally.
