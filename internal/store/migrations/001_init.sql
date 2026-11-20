CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    display_name  TEXT    NOT NULL,
    password_hash TEXT    NOT NULL,
    role          TEXT    NOT NULL CHECK (role IN ('admin','user')),
    active        INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
);

CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf         TEXT    NOT NULL,
    created_at   TEXT    NOT NULL,
    last_seen_at TEXT    NOT NULL,
    expires_at   TEXT    NOT NULL
);
CREATE INDEX idx_sessions_user ON sessions(user_id);

CREATE TABLE categories (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    color      TEXT    NOT NULL DEFAULT '#64748b',
    active     INTEGER NOT NULL DEFAULT 1,
    created_at TEXT    NOT NULL
);

-- Single-row counter that mints ticket numbers. Numbers are never reused:
-- deleting or closing a ticket never frees its number.
CREATE TABLE ticket_seq (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    next_value INTEGER NOT NULL
);
INSERT INTO ticket_seq (id, next_value) VALUES (1, 1);

CREATE TABLE tickets (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    number           INTEGER NOT NULL UNIQUE,
    title            TEXT    NOT NULL,
    description      TEXT    NOT NULL,
    status           TEXT    NOT NULL CHECK (status IN ('open','in_progress','closed')),
    priority         TEXT    NOT NULL CHECK (priority IN ('low','normal','high','urgent')),
    category_id      INTEGER REFERENCES categories(id),
    created_by       INTEGER NOT NULL REFERENCES users(id),
    assigned_to      INTEGER REFERENCES users(id),
    reopen_requested INTEGER NOT NULL DEFAULT 0,
    created_at       TEXT    NOT NULL,
    updated_at       TEXT    NOT NULL,
    closed_at        TEXT
);
CREATE INDEX idx_tickets_status   ON tickets(status);
CREATE INDEX idx_tickets_assigned ON tickets(assigned_to);
CREATE INDEX idx_tickets_created  ON tickets(created_at);
CREATE INDEX idx_tickets_creator  ON tickets(created_by);

CREATE TABLE posts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    ticket_id  INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    author_id  INTEGER NOT NULL REFERENCES users(id),
    body       TEXT    NOT NULL,
    internal   INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL
);
CREATE INDEX idx_posts_ticket ON posts(ticket_id);

CREATE TABLE events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    ticket_id  INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    actor_id   INTEGER REFERENCES users(id),
    kind       TEXT    NOT NULL,
    detail     TEXT    NOT NULL,
    created_at TEXT    NOT NULL
);
CREATE INDEX idx_events_ticket ON events(ticket_id);

CREATE TABLE attachments (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    ticket_id    INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    post_id      INTEGER REFERENCES posts(id) ON DELETE SET NULL,
    filename     TEXT    NOT NULL,
    stored_name  TEXT    NOT NULL,
    content_type TEXT    NOT NULL,
    size_bytes   INTEGER NOT NULL,
    created_by   INTEGER NOT NULL REFERENCES users(id),
    created_at   TEXT    NOT NULL
);
CREATE INDEX idx_attachments_ticket ON attachments(ticket_id);
