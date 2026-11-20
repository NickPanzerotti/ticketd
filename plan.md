# Plan: Lightweight IT Ticketing System ("ticketd")

## 1. Design principles
- **One container, zero dependencies** — app + database + uploads in a single Docker image and a single volume.
- **Small footprint** — ~15–25 MB image, runs happily on a Raspberry Pi or a small VM.
- **Everything useful for an IT desk, nothing more** — statuses, priorities, categories, assignment, search, activity history. No SLAs, no customer portal CRM, no live chat.

## 2. Tech stack
- **Backend:** Go — single static binary, trivial multi-stage Dockerfile (`FROM scratch`/alpine), no runtime deps.
- **Database:** SQLite (WAL mode) on a mounted volume. Trivially backed up by copying one file.
- **Frontend:** Server-rendered HTML + **htmx** + a single hand-written stylesheet (system fonts, dark/light theme). No JS framework, no build step — fast over a LAN, lightweight, yet modern: cards, status pills, clean tables.
- **Auth:** session cookie (HttpOnly, SameSite=Lax) + argon2id/bcrypt password hashing.

## 3. Data model
| Table | Key fields |
|---|---|
| `users` | id, username, display name, password hash, role (`admin`/`user`), active flag |
| `tickets` | **ticket_number** (the immutable public number), title, description, status, priority, category_id, created_by, assigned_to, created/updated/closed timestamps |
| `ticket_seq` | single-row counter used to mint ticket numbers |
| `categories` | id, name, colour, active — admin-managed, seeded with IT defaults (Hardware, Software, Network, Access/Accounts, Peripherals, Other) |
| `posts` | ticket replies — author, body, `public` vs `internal note` flag |
| `events` | audit trail: created, status change, assignment, category change — shown interleaved in the ticket timeline |
| `attachments` | ticket/post id, filename, stored path, content type, size |

### Ticket numbering (never reused)
A dedicated counter row is incremented inside the same transaction that inserts the ticket:
`UPDATE ticket_seq SET v = v + 1 RETURNING v`. Deleting or closing a ticket never frees a number,
so numbers monotonically increase forever. Displayed as `#1042`, with an optional configurable
prefix (e.g. `IT-1042`) via the `TICKET_PREFIX` setting.

## 4. Roles & features

Account provisioning: **admins create and manage user accounts** (no self-signup). Admins can
promote users to admin and deactivate accounts.

### Users (staff who raise tickets)
- Sign in; see **My tickets** with status badges.
- Create ticket: title, description, category, priority, attach files (drag & drop, preview before submit).
- View ticket thread (public admin replies + their own), post a reply, attach more files.
- Replying on a closed ticket submits a reopen request; an admin decides whether to reopen.

### Admins (IT team)
- **Dashboard**: counts by status/priority/unassigned, recent activity.
- **Queue view**: searchable (title/description/replies) and filterable by status, priority,
  category, assignee, date range. Sortable columns.
- **Take** a ticket (self-assign) or **assign to another admin**; assignment logged in the timeline.
- Reply publicly, or leave **internal notes** visible only to admins.
- Change status: Open → In progress → **Closed** (reopen any time). Set priority, recategorize.
- Manage categories (add/rename/retire) and users (create accounts, roles, deactivate).

### Shared
- Ticket detail is a single chronological thread: replies + system events, interleaved, as in a
  well-made modern help desk. Internal notes are visually distinct and hidden from users.
- Attachments served through the app with content-type checks; inline image previews, click for full size.

## 5. Attachments (any file type)
- Stored by UUID filename under `/data/uploads`; the original filename is preserved only for display/download.
- Size cap configurable via `MAX_UPLOAD_MB` (**default 50**).
- Magic-byte sniffing sets the correct content type; recognized types (png, jpg, gif, webp, pdf,
  txt/log/json, common office docs) preview or render inline; anything else appears as a downloadable attachment chip in the thread.
- SVGs rendered sanitized (no inline scripts); all responses use proper `Content-Disposition` guards and `X-Content-Type-Options: nosniff`.

## 6. Docker & hosting (0.0.0.0 requirement)
The server reads bind flags, so LAN exposure is a one-liner:

```bash
docker run -d --name ticketd \
  -p 8080:8080 \
  -v ticketd-data:/data \
  -e BIND_HOST=0.0.0.0 \
  ghcr.io/you/ticketd
```

- `BIND_HOST` / `BIND_PORT` environment variables, also usable as CLI flags (`--bind-host`) when
  running the binary directly on a host, where the choice matters; inside a container `0.0.0.0` is the default.
- All state lives in `/data` (SQLite DB + uploads) on a named volume → backup = copy one directory.
- First-run admin bootstrapped via `-e ADMIN_USERNAME/ADMIN_PASSWORD` or an interactive prompt.
- Other config: `MAX_UPLOAD_MB` (default 50), `TICKET_PREFIX` (default none).
- README includes a `docker compose` example for LAN deployment.

## 7. Security & safety
- Password hashing (argon2id/bcrypt), login rate limiting, CSRF tokens on all mutating forms.
- Upload validation: magic-byte check, size cap, UUID storage names, correct content types (see §5).
- Sessions invalidated on logout/password change.

## 8. Email notifications
Not included in v1 by design. The data model needs no changes to add SMTP alerts later.

## 9. Build order (milestones)
1. **Skeleton**: Go server, SQLite schema/migrations, auth + sessions, initial admin bootstrap.
2. **Ticket core**: creation flow, numbering, thread view, replies, user "my tickets" list.
3. **Admin workflow**: queue/search/filters, take/assign, status + priority + category controls, internal notes.
4. **Attachments**: upload, storage, inline rendering, any-file-type handling.
5. **Polish**: dashboard stats, activity timeline styling, dark/light theme, user management.
6. **Packaging**: Dockerfile, compose example, README, config flags.

## Decisions locked in
- Stack: **Go + SQLite** (single static binary).
- Accounts: **admins create them** (no self-signup).
- Attachments: **any file type**, 50 MB default cap.
- Email notifications: **none in v1**.
- **Internal notes**: yes, admin-only replies on tickets.
