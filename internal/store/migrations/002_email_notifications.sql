-- Email notifications: optional address per account, and admin-editable
-- settings stored in the database (the settings UI is the source of truth
-- once saved).
ALTER TABLE users ADD COLUMN email TEXT;

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
