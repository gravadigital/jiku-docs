# syntax=docker/dockerfile:1
# jiku-docs: static Go binary on distroless, non-root.

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/jiku-docs ./cmd/jiku-docs

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="jiku-docs" \
      org.opencontainers.image.description="Módulo de documentación de Jiku" \
      org.opencontainers.image.vendor="Grava Digital"
COPY --from=build /out/jiku-docs /usr/local/bin/jiku-docs
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=3 CMD ["/usr/local/bin/jiku-docs", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/jiku-docs"]
CMD ["serve"]
