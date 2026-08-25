# Shared Schemas

JSON Schema definitions shared between the API, the Agent, and the frontend.

Phase 0 expresses the API↔Agent contract directly in Go
(`shared/protocol`), which is the single source of truth. Schemas are
generated here once operations carry non-trivial payloads (Phase 2).
