# Changelog

## 0.3.0 - 2026-08-12

- Compare FleetScope native metric baselines with post-release observation windows.
- Persist run stages and samples in SQLite and resume interrupted observations without rerunning gates.
- Add run history CLI/API and live Viewer status with final-report approval lockout.
- Require executable recovery-prerequisite checks before GO and display check phases in the Viewer.
- Add bounded command logs, process-tree cancellation, read-only SQL transactions, optional SQL previews, Git ancestry/cleanliness enforcement, and explicit metric series reducers.
- Introduce a resumable `finalizing` stage so a run is completed only after its immutable report is published.
- Prevent human decisions from relaxing automated gates and require strong temporary Web approval tokens.
- Add renewable single-owner run leases, read-only live-WAL coverage, cross-platform CI, test-gated tag builds, and installer checksum verification.
- Harden the decision console against malformed evidence, path disclosure, static-resource writes, approval brute force, stale polling, and inaccessible navigation state.
- Unify suite navigation and contextual FleetScope handoff, separate success/info/error notifications, consolidate viewer styles, localize dynamic decision summaries, and tighten mobile layout.

## 0.2.0 - 2026-08-12

- Add command, HTTP, file, JSON, read-only SQL, environment, and Compose release checks.
- Revalidate DevCycle candidates, Git ranges, sensitive files, InfraScout drift, Fleet node versions, and observation windows.
- Produce immutable release reports and separate SHA-256-bound human approval artifacts.
- Add an authenticated, write-once Web approval flow with strict JSON parsing and overwrite protection.
- Replace the raw report viewer with a responsive decision console, structured evidence, export, print, and failure recovery.
- Add CI, cross-platform release archives, checksums, installation assets, and operations documentation.
- Upgrade SQL gate dependencies to patched versions and enforce `govulncheck` in CI.
