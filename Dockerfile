# SHELF local image (E-72, NFR-OPS-004). Built by `make docker`; never pushed.
# It wraps the default static binary (`make build`, noavif, CGO=0) — the -avif
# variant is dynamically linked and cannot run on scratch.

FROM alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f AS base
RUN mkdir /out-config /out-cache && chown 65532:65532 /out-config /out-cache

FROM scratch
# oidc mode (E-74) dials the identity provider over HTTPS.
COPY --from=base /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
# Named volumes inherit this ownership on first use, so the non-root user can
# write the config directory (shelf.yaml, index.db, user.db) and the cache.
COPY --from=base --chown=65532:65532 /out-config /config
COPY --from=base --chown=65532:65532 /out-cache /cache
COPY dist/shelf /shelf
ENV SHELF_CONFIG=/config/shelf.yaml
USER 65532:65532
# /data is the media, mounted read-only: -v <media>:/data:ro
VOLUME ["/config", "/data", "/cache"]
EXPOSE 8080
ENTRYPOINT ["/shelf"]
