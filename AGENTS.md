# AGENTS.md

This directory is the servelets collection: one servelet per directory.

- Follow `let/factory/servelets/AGENTS.md` for every servelet task.
- One servelet per directory, named after the `id` in its `servelet.json`.
- Never commit `dist/`; `let-servelet build` writes it.
- Record every schema change in the servelet's `README.md`, in its **Schema** table, together with the new `servelet.Migrate` step. Never edit or remove a step that has shipped.
- Keep `checks.json` covering every route the servelet answers, and run `npx let-servelet check <dir>` before committing.
