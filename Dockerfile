# Image used by goreleaser (`dockers_v2`): the prebuilt static binary of each platform is in the build context, under $TARGETPLATFORM.
# The linux binary is a static musl cgo build (.goreleaser.yaml), so the static distroless base is enough.
# LICENSE and NOTICE ship with the binary: it embeds SQL adapted from crystaldba/postgres-mcp and PgHero (MIT).
FROM gcr.io/distroless/static-debian13:nonroot
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/postgres-dba-mcp /postgres-dba-mcp
COPY LICENSE NOTICE /usr/share/doc/postgres-dba-mcp/
USER nonroot:nonroot
ENTRYPOINT ["/postgres-dba-mcp"]
