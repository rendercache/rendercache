# RenderCache working instructions

Read RESUME.md before making changes. It records the current session, accepted scope, checks and exact next steps.

Work one authorized session at a time (approximately 2–3 hours of scope). Finish a runnable checkpoint, run meaningful checks, update RESUME.md and commit/push before reporting and pausing. Resume only on the user's command. Do not silently move into the following session. Do not claim elapsed work hours that were not actually spent.

The engine repository is rendercache/rendercache; its prototype is Apache 2.0. Website work has a separate repository, recorded in RESUME.md. Do not change domain configuration; the user will connect the domain later. Do not apply the engine's licensing statements to the private website repository.

Implement the agreed pre-render cache for JavaScript pages, not a PDF or arbitrary-computation cache. Keep the service in Go, with headless Chrome/Chromium as an external dependency. Warm cache coverage and clear delivery records are central. Do not claim that a user-agent match verifies a bot, that serving proves indexing/citation, or that every crawler has a universal fixed timeout.

Only public, unauthenticated content may enter future shared snapshots. Background jobs must not inherit visitor cookies or authorization. Keep admin routes off the public listener. When rendering is added, constrain navigations and network access, preserve structured data, keep failed refreshes from destroying good snapshots and make cold behavior explicit.

Use standard Go formatting, go vet, meaningful unit/integration tests and the race detector. Test real rendering with the JavaScript fixture when the renderer exists. Record unavailable checks honestly. Keep changes small enough to inspect and do not add speculative abstractions or dependencies.
