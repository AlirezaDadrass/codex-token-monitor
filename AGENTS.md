# Codex Token Monitor

## Installing this project for a user

If the user gives you this repository and asks you to install or connect the monitor, follow [docs/AGENT_INSTALL.md](docs/AGENT_INSTALL.md). Do the installation, not just an explanation. Ask the user for the computer's display name, hub address, and hub token (prefer a local secret-file path). Do not invent those values or use OpenAI authentication credentials. Keep secrets out of command arguments, logs, commits, screenshots, and your replies.

Use the native Go binary. The default `install` command requires a hub address and a token and checks the connection before registering MCP. Use `--non-interactive` with supplied values when working as an agent; never leave an unattended terminal waiting for missing input. A standalone installation requires the user's explicit choice and `--local`.

For a server installation, use [docs/SERVER.md](docs/SERVER.md) and the interactive `scripts/install-server.sh`. Do not install a server on the user's workstation merely because they asked to install the client. Only perform service, network, TLS, or firewall changes on the host the user authorized.

After installation, run an authenticated connection check and generate an HTML report. Report the actual number of connected devices. For routine report requests, call `generate_usage_report` once and show its `display_markdown`; do not read/rewrite the HTML with the model.

## Development

- Build, run and test natively. Do not introduce Docker, Compose, testcontainers, extra PostgreSQL, or Redis services. This project uses local files and does not need a database service.
- Keep collection incremental and read-only with respect to Codex's own files. Never read/copy Codex authentication files to discover the hub token.
- Before changing collectors or reports, read the affected implementation and tests. Use direct source reads for known files; broader discovery is only needed for a change crossing modules.
- Keep account quota observations separate from per-device token counts. Never describe raw token proportions as exact OpenAI quota attribution.
- Discover device counts from uploaded snapshots. Do not add a fixed maximum, expected fleet size, or minimum number of devices to installation, reporting, or coverage checks. Coverage concerns the registered devices' data only.
- Preserve duplicate-event handling, account separation, private token files, authenticated HTTP/MCP routes, and signed report downloads.
- Run `go test -race ./...`, `go vet ./...`, and a native build for behavior changes. For shell installers also run `bash -n scripts/*.sh` and the dry-run tests. Do not test server installation by changing an unrelated live host.
- Never commit `.local/`, `bin/`, `dist/`, configs, hub tokens, TLS keys, session logs, or generated reports based on personal data. Public examples must use placeholders or clearly labeled synthetic data.
- Keep the Persian report standalone: embedded assets, no external requests, no LLM call for rendering. Follow the existing report design and escape user-supplied names.
