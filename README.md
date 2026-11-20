# ticketd — lightweight IT ticketing

ticketd is a small help-desk for internal IT teams, shipped as a **single self-contained
binary**: the application, an SQLite database and all file uploads live together on one
volume, so deploying it is one container (or one executable) and backing it up is copying
one directory. It has two roles — admins and users — and a simple ticket workflow
(open → in progress → closed, reopenable on request). Ticket numbers auto-increment and
are **never reused**. Admins can search and filter the queue, take tickets or assign them
to other admins, and leave internal notes that users never see. Attachments of any file
type are supported up to `MAX_UPLOAD_MB`. The UI is server-rendered with a dark/light
theme and has no external dependencies — nothing talks to the internet, nothing else
needs to run.

## Quick start (Docker)

ticketd is published as `ghcr.io/nickpanzerotti/ticketd:latest` — no build step needed
on any machine with Docker.

With Docker Compose — the provided `docker-compose.yml` pulls the published image,
exposes the app on port 8080, and keeps all state in the `ticketd-data` volume. Edit the
`ADMIN_USERNAME`/`ADMIN_PASSWORD` placeholders first (they bootstrap the first admin
account on an empty database):

```bash
docker compose up -d
```

Or with plain `docker run`:

```bash
docker run -d --name ticketd \
  -p 8080:8080 \
  -v ticketd-data:/data \
  -e BIND_HOST=0.0.0.0 \
  -e TICKET_PREFIX='NP-' \
  -e ADMIN_USERNAME=admin \
  -e ADMIN_PASSWORD='change-me' \
  ghcr.io/nickpanzerotti/ticketd:latest
```

`BIND_HOST=0.0.0.0` makes the app reachable from other machines on your LAN at
`http://<host-ip>:8080`. The image has no shell, so health checks run from outside the
container, e.g. `curl -fsS http://localhost:8080/login`.

## Configuration

Every setting can be given as an environment variable or as a CLI flag
(`ticketd -bind-host … -bind-port … -data-dir … -max-upload-mb …`); flags win over
environment variables.

| Variable | CLI flag | Default | Description |
|---|---|---|---|
| `BIND_HOST` | `-bind-host` | `0.0.0.0` | Interface to listen on. Set to `0.0.0.0` to expose on the local network. |
| `BIND_PORT` | `-bind-port` | `8080` | TCP port to listen on. |
| `DATA_DIR` | `-data-dir` | `./data` (`/data` in the container) | Directory holding the SQLite database and uploads. |
| `MAX_UPLOAD_MB` | `-max-upload-mb` | `50` | Maximum size of each uploaded attachment, in MB. |
| `TICKET_PREFIX` | `-ticket-prefix` | *(empty)* | Optional display prefix for ticket numbers, e.g. `IT-`. |
| `COOKIE_SECURE` | `-cookie-secure` | `false` | Set to `true` when serving over HTTPS. |
| `ADMIN_USERNAME` / `ADMIN_PASSWORD` | `-admin-username` / `-admin-password` | *(unset)* | Bootstrap the first admin account on an empty database. If unset and a terminal is attached, the app prompts interactively. |

## Roles

- **Admins** (IT team): dashboard with queue statistics; search and filter all tickets;
  take or assign tickets to other admins; change status, priority and category; reply
  publicly or leave internal notes visible only to admins; manage categories and user
  accounts (create, promote, deactivate). There is no self-signup — admins create accounts.
- **Users** (staff): see and create their own tickets, attach files, follow the ticket
  thread and reply. Replying on a closed ticket submits a reopen request for an admin to
  decide on.

## Ticket numbers

Numbers are minted from a dedicated counter inside the same transaction that inserts the
ticket, so they strictly increase and are **never reused** — closing or deleting a ticket
does not free its number. They are shown as `#1042`, optionally prefixed via
`TICKET_PREFIX` (e.g. `IT-1042`).

## Updating

`latest` tracks the `main` branch; version tags (`:0.1.0`) are immutable. To update a
running installation — your tickets, accounts and uploads live in the volume and are not
touched:

```bash
docker compose pull && docker compose up -d
# or without compose:
docker pull ghcr.io/nickpanzerotti/ticketd:latest
docker rm -f ticketd && docker run -d …same flags… ghcr.io/nickpanzerotti/ticketd:latest
```

The GitHub Actions workflow publishes a new image on every push to `main` and on every
`v*` tag.

## Development

Requires Go 1.27 or newer.

```bash
go run .        # starts on http://localhost:8080, data in ./data
go test ./...   # run the test suite
```

The web UI needs **no JavaScript build step** — htmx is vendored in `static/` and served
as-is, and templates are plain Go templates embedded into the binary.

## Build your own image

```bash
docker build -t ticketd:latest .
```

The build is fully offline (`-mod=vendor`, CGO disabled) and produces a scratch-based
image containing nothing but the static binary. Run it like the `docker run` example
above, or point your own orchestration at it — the only requirement is a writable
`/data` directory (or set `DATA_DIR` elsewhere).

## License

Released under the Apache-2.0 license — see [LICENSE](LICENSE).
