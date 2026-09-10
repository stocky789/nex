# Nex project instructions

@AGENTS.md

The imported file is the canonical project contract. Follow its read-before-working guidance;
all project plans live under `docs/plans/`.

Start with its architecture invariant: Nex Core does the work and keeps the state, Nex Controller
only calls host APIs and is replaceable. Check every design against it before writing code.

Apply its required API impact check to every change and maintain [docs/API.md](docs/API.md).
Apply its living user guide gate: maintain [docs/guide/README.md](docs/guide/README.md), verify
applicable workflows, and follow [docs/DOCUMENTATION.md](docs/DOCUMENTATION.md).
