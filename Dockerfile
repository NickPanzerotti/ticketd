# syntax=docker/dockerfile:1

# ---- Build stage -----------------------------------------------------------
FROM golang:1.27-alpine AS build
WORKDIR /src

# Copy the module files and the vendor tree first so this layer is cached
# until dependencies actually change. The build runs fully offline.
COPY go.mod go.sum ./
COPY vendor/ vendor/
COPY . .

# VERSION is stamped by CI (git describe --tags); plain builds show "dev".
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X ticketd/internal/buildinfo.Version=$VERSION" -mod=vendor -o /out/ticketd .

# ---- Runtime stage ---------------------------------------------------------
# The binary embeds templates/ and static/ via go:embed, so the only extra
# file the final image needs is a CA trust store: without it Go cannot
# verify any TLS certificate (SMTP STARTTLS/TLS). The bundle is Alpine's
# copy of the Mozilla root set, taken from the build stage. All state
# lives in /data.
FROM scratch

COPY --from=build /out/ticketd /ticketd
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# Store all state (SQLite DB + uploads) on the mounted volume.
ENV DATA_DIR=/data

EXPOSE 8080
VOLUME /data

ENTRYPOINT ["/ticketd"]

# No HEALTHCHECK instruction: the scratch image has no shell and no curl, so
# there is nothing inside the container to run a check with. Probe
# http://127.0.0.1:8080/login from the host (see README) or an external
# monitor instead.
