# Project status

This is a status record, not a promise to implement every idea below. The old local
pipeline and dynamic-queue plans are design history; their unchecked boxes do not
mean those capabilities are missing.

## Implemented and covered by regression tests

- Portable helpers and metadata caching; staged audio publication protects existing files.
- Three shared download workers, independent collection queues, deduplication and forced terminal retries.
- Session summaries, suspicious-duration detection and selected YouTube fallback retries.
- Queue-scoped cancellation, a shared queue view, explicit history clearing and new-session controls.
- Portable FFmpeg/FFprobe pairs with system PATH fallback and dependency diagnostics.
- Atomic settings persistence, truthful save failures and guarded search/pagination responses.
- StrictMode-safe queue lifecycle, bulk scheduling and shared adaptive progress polling.
- Consistent directory templates for audio, cover and lyrics downloads; M3U8 paths survive history clearing.
- Frontend tests, lint/build, Go tests/vet and Windows application build in CI before release builds.

See [the audit](docs/audits/2026-10-08-project-audit.md) and
[its resolution record](docs/audits/2026-10-08-audit-resolution.md) for evidence and verification limits.

## Optional future work

- Persist and resume queues across application restarts. Current queues intentionally live for one session.
- Add cancellation-aware metadata enrichment and more complete dependency diagnostics.
- Localization, richer keyboard/screen-reader coverage and a full visual audit of every workflow.
- Queue export and retry controls by outcome if actual workflows justify them.

These extensions require their own design and tests; they are separate from the audit fixes.
