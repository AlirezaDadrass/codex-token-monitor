# Changelog

## 0.1.4

- Connected client installs now send the initial usage snapshot before MCP registration, including devices without logs. A successful health check followed by an upload failure no longer reports installation success; the saved profile remains retryable.
- Windows installation starts a collector task immediately and at the current user's logon. Usage continues to upload while that user is logged in, even with Codex closed, without administrator runtime privileges or credentials in task arguments.
- Reinstallation preserves device identity and credentials and updates only the collector task owned by the same user and profile, restoring the previous task on failure.
- Added native initial-upload checks and Windows integration coverage for automatic new-session uploads without a manual collect call or an active Codex process.

## 0.1.3

- Added a token-login dashboard on the hub's root URL, using expiring HttpOnly sessions, logout, and the existing standalone Persian report design.
- Reports show all registered devices in the configured group, plus per-session counters, device filters and session quota-share estimates when supported by the observations.
- Differing account identities in retained logs no longer abort token reports. Shared quota and quota estimates are withheld when account identity is ambiguous; configured account groups remain isolated.
- HTML rendering now happens only on the hub. Local MCP/CLI requests return signed hub links, with no local HTML generation or local-counter fallback during hub failures.

## 0.1.2

- Added a standalone PowerShell installer for Windows clients and servers, with verified prebuilt x64/ARM64 downloads and offline installation.
- Windows server installation uses a startup task under LOCAL SERVICE with private ACLs; it does not require a Go toolchain.
- Added native Windows installer integration checks and automatic GitHub release builds.
- Fixed Windows Codex registration through npm-style codex.cmd launchers.

## 0.1.1

- Removed the assumed device count from configuration, report metadata, MCP status, and the HTML report. New devices join automatically without a fixed maximum or minimum fleet size.
- Report coverage now reflects the freshness and history of registered devices; a healthy one-device report and a healthy report with more than 100 devices are both supported.
- Existing configurations with `expected_machines` still load; that legacy field is ignored.
- Existing hubs already accept any device count. Upgrade the hub for the new report metadata/coverage rules, and upgrade clients for the updated local HTML renderer.

## 0.1.0

- Initial public Go MCP release with authenticated multi-device collection, standalone Persian HTML reports, interactive client/server installers, and agent installation documentation.
