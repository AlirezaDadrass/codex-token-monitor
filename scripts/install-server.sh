#!/usr/bin/env bash
# Native Linux/systemd hub installer. No container runtime or OpenAI login is used.
set -euo pipefail
set +x

repo="AlirezaDadrass/codex-token-monitor"
version="${TOKEN_MONITOR_VERSION:-v0.1.4}"
binary=""
dry_run=false
stage_dir=""
cleanup() { if [[ -n "$stage_dir" && -d "$stage_dir" ]]; then rm -rf -- "$stage_dir"; fi; }
trap cleanup EXIT
die() { printf '%s\n' "$*" >&2; exit 1; }

while (($#)); do
  case "$1" in
    --binary) [[ $# -ge 2 ]] || die "--binary requires a path"; binary="$2"; shift 2 ;;
    --version) [[ $# -ge 2 ]] || die "--version requires a tag"; version="$2"; shift 2 ;;
    --dry-run) dry_run=true; shift ;;
    --help|-h) printf '%s\n' 'Usage: sudo bash scripts/install-server.sh [--binary /path/token-monitor] [--version v0.1.4] [--dry-run]'; exit 0 ;;
    *) die "Unknown option: $1" ;;
  esac
done

[[ "$(uname -s)" == Linux ]] || die "The server installer supports native Linux with systemd. See docs/SERVER.md for other hosts."
if ! $dry_run; then
  [[ $EUID -eq 0 ]] || die "Run this server installer with sudo."
  command -v systemctl >/dev/null || die "systemd is required."
  [[ -d /run/systemd/system ]] || die "systemd must be running on this host."
fi

ask() {
  local label="$1" default="$2" value
  printf '%s [%s]: ' "$label" "$default" >&2
  IFS= read -r value || die "Required installation input is missing."
  printf '%s' "${value:-$default}"
}

printf '%s\n' 'نصب سرور Codex Token Monitor — سرویس بومی لینوکس' >&2
name="$(ask 'نام مرکز در تنظیمات' 'مرکز مصرف')"
bind="$(ask 'آدرس گوش‌دادن سرور (برای تونل SSH مقدار پیش‌فرض)' '127.0.0.1')"
port="$(ask 'پورت سرور' '8787')"
[[ "$bind" =~ ^[a-zA-Z0-9.:_-]+$ ]] || die "Invalid listen address."
[[ "$port" =~ ^[0-9]{4,5}$ ]] && ((10#$port >= 1024 && 10#$port <= 65535)) || die "Port must be 1024..65535."
default_url="http://$bind:$port"
[[ "$bind" == *:* ]] && default_url="http://[$bind]:$port"
[[ "$bind" == '0.0.0.0' || "$bind" == '::' ]] && default_url=""
public_url="$(ask 'آدرس سرور برای اتصال دستگاه‌ها؛ با http:// یا https://' "$default_url")"
[[ "$public_url" =~ ^https?://[^[:space:]\"\']+$ ]] || die "A valid http(s) server address is required."
[[ "$public_url" != *@* && "$public_url" != *\?* && "$public_url" != *\#* ]] || die "The server address must not contain credentials, query, or fragment."

printf '%s' 'توکن اتصال موجود (مخفی؛ خالی = ساخت توکن تصادفی جدید): ' >&2
hub_token=""
if [[ -t 0 ]]; then IFS= read -r -s hub_token || die "Token input is missing."; printf '\n' >&2
else IFS= read -r hub_token || die "Token input is missing."; fi
if [[ -n "$hub_token" ]]; then
  [[ ${#hub_token} -ge 32 && ! "$hub_token" =~ [[:space:]] ]] || die "Token must be at least 32 characters without whitespace."
fi
tls_cert="$(ask 'مسیر گواهی TLS مستقیم (خالی = تونل SSH یا reverse proxy)' '')"
tls_key=""
if [[ -n "$tls_cert" ]]; then
  tls_key="$(ask 'مسیر کلید خصوصی TLS' '')"
  [[ "$tls_cert" == /* && "$tls_key" == /* ]] || die "TLS paths must be absolute."
fi

config_dir=/etc/codex-token-monitor
state_dir=/var/lib/codex-token-monitor
service_user=codex-token-monitor
unit_path=/etc/systemd/system/codex-token-monitor.service
target_binary=/usr/local/bin/token-monitor

if $dry_run; then
  printf '\n%s\n' 'Dry run: no files, users, services, downloads, or firewall rules were changed.'
  printf 'Name: %s\nListen: %s:%s\nPublic URL: %s\nConfig: %s/config.json\nState: %s\nService: %s\n' "$name" "$bind" "$port" "$public_url" "$config_dir" "$state_dir" "$unit_path"
  if [[ -n "$hub_token" ]]; then printf '%s\n' 'Token: provided (redacted)'; else printf '%s\n' 'Token: a new 256-bit token will be generated'; fi
  exit 0
fi

[[ ! -e "$config_dir/config.json" && ! -e "$unit_path" ]] || die "An installation already exists. Back it up and follow docs/SERVER.md; it will not be overwritten."
if [[ -n "$tls_cert" ]]; then
  [[ -r "$tls_cert" && -r "$tls_key" ]] || die "TLS certificate or key is not readable."
fi

stage_dir="$(mktemp -d)"
if [[ -z "$binary" ]]; then
  for command in curl tar sha256sum; do command -v "$command" >/dev/null || die "$command is required for downloading a release."; done
  case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) die "Unsupported CPU; build from source and pass --binary." ;; esac
  asset="token-monitor_${version#v}_linux_${arch}.tar.gz"
  release_url="https://github.com/$repo/releases/download/$version"
  curl --fail --location --proto '=https' --tlsv1.2 "$release_url/$asset" --output "$stage_dir/$asset"
  curl --fail --location --proto '=https' --tlsv1.2 "$release_url/checksums.txt" --output "$stage_dir/checksums.txt"
  checksum="$(awk -v name="$asset" '$2 == name { print $1 }' "$stage_dir/checksums.txt")"
  [[ "$checksum" =~ ^[a-f0-9]{64}$ ]] || die "Release checksum was not found."
  (cd "$stage_dir" && printf '%s  %s\n' "$checksum" "$asset" | sha256sum --check --status) || die "Release checksum verification failed."
  tar -xzf "$stage_dir/$asset" -C "$stage_dir" token-monitor
  binary="$stage_dir/token-monitor"
fi
[[ -f "$binary" && -x "$binary" ]] || die "The selected binary is not executable."

if ! getent passwd "$service_user" >/dev/null; then
  useradd --system --user-group --home-dir "$state_dir" --no-create-home --shell /usr/sbin/nologin "$service_user"
fi
install -d -m 0750 -o root -g "$service_user" "$config_dir"
install -d -m 0750 -o "$service_user" -g "$service_user" "$state_dir"
install -m 0755 "$binary" "$target_binary"
init_args=(init --non-interactive --name "$name" --config "$config_dir/config.json" --data-dir "$state_dir" --codex-home "$state_dir/unused-codex-home" --public-url "$public_url")
if [[ -z "$hub_token" ]]; then
  env -u TOKEN_MONITOR_HUB_TOKEN "$target_binary" "${init_args[@]}" --create-token
else
  printf '%s\n' "$hub_token" | env -u TOKEN_MONITOR_HUB_TOKEN "$target_binary" "${init_args[@]}" --token-stdin
fi
chown root:"$service_user" "$config_dir/config.json" "$config_dir/hub-token"
chmod 0640 "$config_dir/config.json" "$config_dir/hub-token"

# Copy only the chosen TLS files; the service gets no access to the source key directory.
tls_args=""
if [[ -n "$tls_cert" ]]; then
  install -m 0640 -o root -g "$service_user" "$tls_cert" "$config_dir/server.crt"
  install -m 0640 -o root -g "$service_user" "$tls_key" "$config_dir/server.key"
  tls_args=" --tls-cert $config_dir/server.crt --tls-key $config_dir/server.key"
fi
listen="$bind:$port"
[[ "$bind" == *:* ]] && listen="[$bind]:$port"
cat > "$unit_path" <<UNIT
[Unit]
Description=Codex Token Monitor authenticated MCP hub
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$service_user
Group=$service_user
ExecStart=$target_binary hub --config $config_dir/config.json --listen $listen$tls_args
Restart=on-failure
RestartSec=5
UMask=0077
StateDirectory=codex-token-monitor
StateDirectoryMode=0750
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=$state_dir
RestrictSUIDSGID=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
CapabilityBoundingSet=

[Install]
WantedBy=multi-user.target
UNIT
chmod 0644 "$unit_path"
systemctl daemon-reload
systemctl enable --now codex-token-monitor.service
systemctl is-active --quiet codex-token-monitor.service || die "Service did not start; inspect journalctl -u codex-token-monitor.service."

unset hub_token
printf '\n%s\n' 'سرور نصب شد و سرویس فعال است.'
printf 'Server: %s\nMCP: %s/mcp\nToken file: %s/hub-token\n' "$public_url" "${public_url%/}" "$config_dir"
printf '%s\n' 'توکن در خروجی نمایش داده نشد. فایل آن را از مسیر بالا به‌صورت امن برای دستگاه‌ها فراهم کنید.'
printf '%s\n' 'نام دستگاه، آدرس سرور و توکن را به نصب‌کنندهٔ هر کامپیوتر بدهید. راهنما: docs/AGENT_INSTALL.md'
printf '%s\n' 'دستور بررسی لاگ: sudo journalctl -u codex-token-monitor.service -n 50 --no-pager'
printf '%s\n' 'این نصب‌کننده تنظیمات فایروال یا reverse proxy را تغییر نمی‌دهد.'
