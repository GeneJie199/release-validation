# release-validation v2.0.0-rc.2 Community

ReleaseGuard presents the existing conclusion, gates, expected-change coverage, runtime observations, history, decisions and evidence dialog in the shared Suite design. A GO remains a report-backed gate result and grants no production deployment permission.

## Changes in this candidate

- Shared Light / Dark / System appearance, responsive sidebar/mobile navigation and consistent cards, forms, dialogs and readable state colors. Module links preserve the current Suite hosts, ports and theme.
- The gate-distribution SVG counts actual checks in the loaded report. It is not a historical success rate. Missing checks show an explicit empty state, and the quickstart demo is labelled as an example rather than current release readiness.
- Theme CSS and JavaScript are embedded in the product binary and served locally. The redesigned appearance requires no external CDN; the existing report and approval APIs retain their behavior and strict Content Security Policy.
- Read-only `GET /api/v1/diagnostics/runtime` exposes seven aggregate process counters: goroutines, heap allocation, heap system/in-use bytes, total runtime system bytes, GC count and uptime. It exposes no profiles, configuration, environment, process arguments or credentials.
- Diagnostics retain the local Host/Origin boundary. No new approval or execution authority is added: normal report viewing can remain read-only, and human approval keeps its existing report binding and token checks.

## Installation and independent use

Use this component's [standalone CLI quick start](https://github.com/GeneJie199/release-validation/blob/main/README.md#quick-start) and [operations guide](https://github.com/GeneJie199/release-validation/blob/main/docs/operations.md). The component archive contains its binary, README, installation scripts, operations documents and license material.

For the combined Suite, follow the [online quick start](https://github.com/GeneJie199/ai-devops-open-suite/blob/main/docs/quickstart.md) or the [full offline installation guide](https://github.com/GeneJie199/ai-devops-open-suite/blob/main/docs/offline-install.md). Those Suite documents are linked online rather than assumed to exist inside a standalone component archive. Pin the requested candidate version, verify published checksums and use an isolated evaluation prefix. Back up configuration, evidence and run state before an upgrade or rollback.

The delivery interface is the CLI binary/archive and vendored Corresponding Source. Historical Go module paths do not include `/v2`; this candidate makes no `go install ...@v2` compatibility promise. Native macOS CLI checks are separate from complete Suite offline installation, whose supported package scope is Windows/Linux amd64.

On the local Windows acceptance host, two cold source builds with Go1.27.3 encountered internal compiler failures. A fresh vendored offline build passed with `GOMAXPROCS=2` and `go build -p=1`; the cause is unconfirmed. This bounded local result does not erase the default-build failures or replace the separately recorded hosted platform and release-binary checks.

## Candidate and safety scope

This is an RC, not GA. These changes do not assert completed final 136/168 functional acceptance, uninterrupted 24-hour stability, or live GPT/Kimi/DeepSeek three-model collaboration. Those require their own version-bound evidence. Appearance screenshots and the labelled demo GO do not prove a real release, a passed recovery check or production approval.

The Suite generic CLI runtime remains for trusted single-user local environments. Worktrees and role path checks do not provide an OS sandbox for untrusted users. Enforcing isolation policies fail closed, R4 remains blocked, and a ReleaseGuard GO grants no production deployment permission. No model credentials, subscription/API entitlement, automatic purchase, HA or untrusted multi-tenancy are added here.

New v2 source is AGPL-3.0-only; historical v1.x keeps its original Apache-2.0 terms. Archives retain full license texts, third-party notices, inventory and SBOM. Corresponding Source is distributed separately with vendored Go dependencies and checksums. Commercial alternatives remain subject to verified rights and a separate agreement; no signed CLA, copyright transfer or third-party relicensing right is asserted. Published target binaries must pass the configured reachable-vulnerability scans. See SECURITY.md for reporting.
