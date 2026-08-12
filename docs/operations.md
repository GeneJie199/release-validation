# ReleaseGuard Operations

ReleaseGuard is a resumable release validation runner with an optional local decision console. The console is read-only unless an approval token is explicitly configured. Plans are executable policy because `command` checks run shell commands; store and review them like deployment code.

## Validation flow

```bash
releaseguard check --plan release-plan.json --state releaseguard-runs.db --out release-report.json
releaseguard runs --state releaseguard-runs.db
releaseguard confirm --report release-report.json --decision GO --by release-manager --note "reviewed"
releaseguard serve --report release-report.json --state releaseguard-runs.db --addr 127.0.0.1:8771
```

The validation report and approval are immutable by default. `check --force` is intended only for an explicitly disposable path. The approval contains the SHA-256 digest of the report it approves.

Exit behavior:

| Decision | Meaning | Exit |
|---|---|---|
| `GO` | All checks passed | 0 |
| `HOLD` | Only optional checks failed | 1 |
| `NO-GO` | A required check failed | 1 |
| usage error | Invalid command or flags | 2 |
| interrupted | Validation or observation was interrupted and remains resumable | 3 |

`GO` remains a tool recommendation until an approval artifact is recorded.

## Evidence sources

- DevCycle release candidate readiness and criterion evidence coverage
- Git base/target commit manifest and unexpected sensitive file changes
- command, HTTP, file digest, JSON, SQL, `.env`, and Compose checks
- mandatory recovery checks for previous artifacts, backups, restore connectivity, or rollback tooling
- InfraScout drift with explicit stable-ID allowlisting
- FleetScope node health, freshness, version labels, open critical alerts, and observation samples
- FleetScope native metric baselines and post-release regression comparisons

## Resumable observation

The run database stores the normalized plan, current report, observation deadline, every FleetScope sample, and final decision. It contains operational evidence and is forced to mode `0600`. During deterministic checks it receives live `checking` checkpoints. Observation uses `observing`; after checks finish the run enters `finalizing`, and only becomes `completed` after the immutable report is on disk. A renewable 30-second lease prevents two processes from executing the same active run. Graceful interruption releases it immediately; after a crash, retry once the lease expires. Matching uses `release_id` plus the exact plan SHA-256; a changed plan starts a new run.

`releaseguard runs --state releaseguard-runs.db` lists history without printing plan headers or check payloads. The Viewer opens the database read-only and prefers an active report over an older final report, so the observation screen can refresh safely while another process writes checkpoints.

An interrupted run stays active so the same plan can resume it. When recovery is intentionally no longer desired, close it with an audit reason instead of editing SQLite:

```bash
releaseguard runs --state releaseguard-runs.db --abandon RUN_ID --reason "change window closed"
```

Environment and Compose comparisons record changed key names, never the values. SQL credentials are read only from the environment variable named in `dsn_env`; queries run inside read-only transactions and values are omitted unless `include_sql_preview` is explicitly enabled. Still use a database account that cannot write.

## Report viewer

The viewer defaults to loopback. Use an SSH tunnel for remote review:

```bash
ssh -L 8771:127.0.0.1:8771 release@host
```

`--allow-remote` is available only for an authenticated reverse proxy and restrictive network policy. Reports contain file names, commit subjects, node IDs, and operational evidence.

To allow a human reviewer to create the one immutable approval artifact from the Web UI, set a long random token for the service process:

```bash
export RELEASEGUARD_APPROVAL_TOKEN='replace-with-a-long-random-token'
releaseguard serve --report release-report.json --state /var/lib/releaseguard/releaseguard-runs.db --addr 127.0.0.1:8771
```

The browser sends the token only in the approval request. Tokens shorter than 24 characters are rejected. The endpoint uses constant-time comparison, strict single-document JSON parsing, report SHA-256 binding, and atomic no-overwrite creation. Human decisions can preserve or tighten the automated decision but can never relax it. Approval is disabled while any validation run is active. Remove the token after the decision is recorded. Keep the default systemd unit read-only unless the change process explicitly requires Web approval.

## systemd

The example service expects a dedicated `releaseguard` user and `/var/lib/releaseguard/release-report.json`:

```bash
sudo useradd --system --home /var/lib/releaseguard --shell /usr/sbin/nologin releaseguard
sudo sh ./scripts/install.sh ./releaseguard-linux-amd64 ./checksums.txt
sudo chown -R releaseguard:releaseguard /var/lib/releaseguard
sudo install -m 0644 deploy/releaseguard-report.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now releaseguard-report
```

The checksum argument is optional for local builds and required for downloaded release binaries. The installer verifies the selected binary entry before installing it. The release workflow runs the full test and security gate before any tagged artifact is built.
