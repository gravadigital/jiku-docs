# jiku-docs — convenciones del repo

- Go 1.25 (`go.mod` en `go 1.25.0`): no subir dependencias que exijan una versión mayor.
- Los contratos de `docs/apis/` son la fuente de verdad de forma. Un cambio de forma va al contrato, al
  código y al `CHANGELOG.md` (`[Unreleased]`) en el mismo commit. `docs/decisiones-integracion.md` manda
  sobre `docs/especificacion-v1.md`.
- Las fichas de recursos (`internal/service/specs.go`) alimentan la validación y `meta.describe`;
  `internal/contract` verifica que coincidan con el YAML.
- Deny-by-default: claves, filtros, campos e includes desconocidos son `invalid_fields`; lo no visible es
  `*_not_found`. Nunca se concede acceso si core no responde (`upstream_unavailable`).
- Toda escritura: transacción con evento en `changelog` (outbox). El contenido se sube a S3 antes de la
  transacción. Nada de contenido, tokens ni URLs prefirmadas en logs o eventos.
- Tests: `go test -race ./...` (integración con Docker; `JIKU_DOCS_SKIP_INTEGRATION=1` la saltea). Solo
  fixtures sintéticos: el dump de HedgeDoc y las credenciales viven en `.local/` y nunca se commitean ni se
  copian.
- Commits convencionales en español, uno por preocupación, en `dev`. Nunca `git push`. Nunca commitear
  `.local/`, `_research/`, `.env` ni credenciales.
- Antes de cerrar: `go vet ./...`, `golangci-lint run ./...`, `go test -race ./...`, `docker build .`.
