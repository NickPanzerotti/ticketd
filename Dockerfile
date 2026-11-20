# syntax=docker/dockerfile:1

# ---- Build stage -----------------------------------------------------------
FROM golang:1.27-alpine AS build
WORKDIR /src

# Copy the module files and the vendor tree first so this layer is cached
# until dependencies actually change. The build runs fully offline.
COPY go.mod go.sum ./
COPY vendor/ vendor/
COPY . .

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -mod=vendor -o /out/ticketd .

# ---- Runtime stage ---------------------------------------------------------
# The binary embeds templates/ and static/ via go:embed, so the final image
# needs nothing but the single static binary. All state lives in /data.
FROM scratch

COPY --from=build /out/ticketd /ticketd

# Store all state (SQLite DB + uploads) on the mounted volume.
ENV DATA_DIR=/data

EXPOSE 8080
VOLUME /data

ENTRYPOINT ["/ticketd"]

# No HEALTHCHECK instruction: the scratch image has no shell and no curl, so
# there is nothing inside the container to run a check with. Probe
# http://127.0.0.1:8080/login from the host (see README) or an external
# monitor instead.
