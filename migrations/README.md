# Migrations

SQL migrations for the JotHost PostgreSQL database (DATABASE.md).

Phase 0 introduces no schema, so this directory is empty apart from this file.
The first migrations arrive in Phase 1 along with the migration runner.

## Rules

Every schema change requires a matching pair (CLAUDE.md section 8):

```text
NNNN_description.up.sql
NNNN_description.down.sql
```

- Never modify a migration that has been applied anywhere; add a new one.
- Never change production schema by hand.
- Use transactions for multi-step changes.
- A `down` migration must actually reverse its `up`.

## Naming

```text
0001_create_users.up.sql
0001_create_users.down.sql
0002_create_roles.up.sql
0002_create_roles.down.sql
```
