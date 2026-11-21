# Changelog

## 0.1.1

- Removed the assumed device count from configuration, report metadata, MCP status, and the HTML report. New devices join automatically without a fixed maximum or minimum fleet size.
- Report coverage now reflects the freshness and history of registered devices; a healthy one-device report and a healthy report with more than 100 devices are both supported.
- Existing configurations with `expected_machines` still load; that legacy field is ignored.
- Existing hubs already accept any device count. Upgrade the hub for the new report metadata/coverage rules, and upgrade clients for the updated local HTML renderer.

## 0.1.0

- Initial public Go MCP release with authenticated multi-device collection, standalone Persian HTML reports, interactive client/server installers, and agent installation documentation.
