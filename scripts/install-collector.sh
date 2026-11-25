#!/usr/bin/env bash
# Interactive installer and service setup script for ai-ledger-collector (macOS & Linux).
#
# Usage:
#   ./scripts/install-collector.sh
#   curl -fsSL https://raw.githubusercontent.com/lancewilhelm/ai-ledger/main/scripts/install-collector.sh | bash
#
# Non-interactive example:
#   ./scripts/install-collector.sh --server http://my-server:8484 --token ail_... --service yes --sync yes --yes

set -euo pipefail

REPO="${AI_LEDGER_REPO:-lancewilhelm/ai-ledger}"
LABEL="com.ai-ledger.collector"
SERVICE_NAME="ai-ledger-collector.service"

SERVER_URL=""
DEVICE_TOKEN=""
INSTALL_DIR=""
INTERVAL="3m"
INSTALL_SERVICE=""
RUN_SYNC=""
VERSION="latest"
FROM_SOURCE=0
AUTO_YES=0
UNINSTALL=0
TMP_DIR=""

# Colors when writing to a terminal
if [ -t 1 ]; then
  C_RESET=$'\033[0m'
  C_BOLD=$'\033[1m'
  C_DIM=$'\033[2m'
  C_GREEN=$'\033[32m'
  C_YELLOW=$'\033[33m'
  C_RED=$'\033[31m'
  C_CYAN=$'\033[36m'
else
  C_RESET="" C_BOLD="" C_DIM="" C_GREEN="" C_YELLOW="" C_RED="" C_CYAN=""
fi

info()  { printf '%s==>%s %s\n' "$C_CYAN" "$C_RESET" "$*"; }
ok()    { printf '%s✓%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
warn()  { printf '%s!%s %s\n' "$C_YELLOW" "$C_RESET" "$*" >&2; }
err()   { printf '%s✗%s %s\n' "$C_RED" "$C_RESET" "$*" >&2; }
die()   { err "$*"; exit 1; }

usage() {
  cat <<'EOF'
AI Ledger Collector Setup (macOS & Linux)

Installs ai-ledger-collector, configures it to talk to your AI Ledger server,
optionally runs an initial sync, and lets you choose whether to run it as a
background service (launchd on macOS, systemd user service or cron on Linux).

Usage:
  install-collector.sh [options]

Options:
  --server <url>        AI Ledger server URL (e.g. http://unraid.local:8484)
  --token <token>       Device bearer token (ail_...)
  --install-dir <dir>   Directory for the collector binary (default: ~/.local/bin or /usr/local/bin)
  --interval <dur>      Background sync interval (default: 3m; examples: 60s, 3m, 10m)
  --service <yes|no>    Install as a background service without prompting
  --sync <yes|no>       Run an initial sync now without prompting
  --from-source         Build from source instead of downloading a GitHub Release binary
  --version <tag>       Release tag to download (default: latest)
  --yes, -y             Accept defaults for remaining prompts
  --uninstall           Stop and remove the background service (and optionally binary/config)
  -h, --help            Show this help message
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --server)       SERVER_URL="${2:?--server requires a value}"; shift 2 ;;
    --token)        DEVICE_TOKEN="${2:?--token requires a value}"; shift 2 ;;
    --install-dir)  INSTALL_DIR="${2:?--install-dir requires a value}"; shift 2 ;;
    --interval)     INTERVAL="${2:?--interval requires a value}"; shift 2 ;;
    --service)      INSTALL_SERVICE="${2:?--service requires yes or no}"; shift 2 ;;
    --sync)         RUN_SYNC="${2:?--sync requires yes or no}"; shift 2 ;;
    --version)      VERSION="${2:?--version requires a value}"; shift 2 ;;
    --from-source)  FROM_SOURCE=1; shift ;;
    --yes|-y)       AUTO_YES=1; shift ;;
    --uninstall)    UNINSTALL=1; shift ;;
    -h|--help)      usage; exit 0 ;;
    *)              die "Unknown option: $1 (run with --help for usage)" ;;
  esac
done

has_tty() {
  [ -t 0 ] || [ -r /dev/tty ]
}

prompt_read() {
  local prompt="$1"
  local varname="$2"
  local secret="${3:-0}"
  local input=""

  if ! has_tty; then
    eval "$varname=''"
    return 1
  fi

  if [ "$secret" = "1" ]; then
    printf '%s' "$prompt" >/dev/tty
    IFS= read -rs input </dev/tty || true
    printf '\n' >/dev/tty
  else
    printf '%s' "$prompt" >/dev/tty
    IFS= read -r input </dev/tty || true
  fi
  eval "$varname=\$input"
}

prompt_yes_no() {
  local question="$1"
  local default="${2:-y}" # y or n
  local suffix="[Y/n]"
  [ "$default" = "n" ] && suffix="[y/N]"

  if [ "$AUTO_YES" = "1" ] || ! has_tty; then
    [ "$default" = "y" ]
    return
  fi

  local ans=""
  while true; do
    prompt_read "${question} ${suffix} " ans 0
    ans="$(printf '%s' "$ans" | tr '[:upper:]' '[:lower:]' | xargs)"
    if [ -z "$ans" ]; then
      [ "$default" = "y" ]
      return
    fi
    case "$ans" in
      y|yes) return 0 ;;
      n|no)  return 1 ;;
      *)     printf 'Please answer y or n.\n' >/dev/tty ;;
    esac
  done
}

detect_platform() {
  local raw_os raw_arch
  raw_os="$(uname -s)"
  raw_arch="$(uname -m)"

  case "$raw_os" in
    Darwin) OS="darwin" ;;
    Linux)  OS="linux" ;;
    *)      die "Unsupported operating system: ${raw_os} (only macOS and Linux are supported)" ;;
  esac

  case "$raw_arch" in
    arm64|aarch64) ARCH="arm64" ;;
    x86_64|amd64)  ARCH="amd64" ;;
    *)             die "Unsupported CPU architecture: ${raw_arch}" ;;
  esac
}

parse_interval_seconds() {
  local raw="$1"
  if [[ "$raw" =~ ^([0-9]+)s$ ]]; then
    echo "${BASH_REMATCH[1]}"
  elif [[ "$raw" =~ ^([0-9]+)m$ ]]; then
    echo $(( BASH_REMATCH[1] * 60 ))
  elif [[ "$raw" =~ ^([0-9]+)h$ ]]; then
    echo $(( BASH_REMATCH[1] * 3600 ))
  elif [[ "$raw" =~ ^[0-9]+$ ]]; then
    echo "$raw"
  else
    die "Invalid --interval '${raw}'. Use Go duration format like 60s, 3m, or 10m."
  fi
}

parse_interval_minutes() {
  local secs
  secs="$(parse_interval_seconds "$1")"
  local mins=$(( (secs + 59) / 60 ))
  [ "$mins" -lt 1 ] && mins=1
  echo "$mins"
}

find_local_repo_root() {
  local candidates=()
  if [ -n "${BASH_SOURCE[0]:-}" ] && [ -f "${BASH_SOURCE[0]}" ]; then
    local script_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    candidates+=("$(cd "$script_dir/.." && pwd)")
  fi
  candidates+=("$PWD")

  local dir
  for dir in "${candidates[@]}"; do
    if [ -f "$dir/go.mod" ] && [ -f "$dir/cmd/ai-ledger-collector/main.go" ] && grep -q "ai-ledger" "$dir/go.mod" 2>/dev/null; then
      echo "$dir"
      return 0
    fi
  done
  return 1
}

read_existing_config_field() {
  local cfg="$HOME/.config/ai-ledger/collector.json"
  local key="$1"
  [ -f "$cfg" ] || return 0
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$cfg" "$key" <<'PY' 2>/dev/null || true
import json, sys
with open(sys.argv[1], "r", encoding="utf-8") as f:
    data = json.load(f)
val = data.get(sys.argv[2], "")
if isinstance(val, str):
    print(val)
PY
  else
    sed -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$cfg" | head -n 1
  fi
}

uninstall_collector() {
  detect_platform
  info "Removing ai-ledger-collector background service..."

  if [ "$OS" = "darwin" ]; then
    local plist="$HOME/Library/LaunchAgents/${LABEL}.plist"
    if [ -f "$plist" ]; then
      launchctl bootout "gui/$(id -u)" "$plist" 2>/dev/null || true
      rm -f "$plist"
      ok "Removed macOS LaunchAgent (${plist})"
    else
      info "No macOS LaunchAgent found at ${plist}"
    fi
  else
    local unit="$HOME/.config/systemd/user/${SERVICE_NAME}"
    if command -v systemctl >/dev/null 2>&1 && systemctl --user list-unit-files "$SERVICE_NAME" >/dev/null 2>&1; then
      systemctl --user disable --now "$SERVICE_NAME" 2>/dev/null || true
    fi
    if [ -f "$unit" ]; then
      rm -f "$unit"
      command -v systemctl >/dev/null 2>&1 && systemctl --user daemon-reload 2>/dev/null || true
      ok "Removed systemd user service (${unit})"
    fi
    if command -v crontab >/dev/null 2>&1; then
      local existing
      existing="$(crontab -l 2>/dev/null || true)"
      if printf '%s\n' "$existing" | grep -q "ai-ledger-collector"; then
        printf '%s\n' "$existing" | grep -v "ai-ledger-collector" | crontab -
        ok "Removed ai-ledger-collector crontab entry"
      fi
    fi
  fi

  local bin_candidates=()
  [ -n "$INSTALL_DIR" ] && bin_candidates+=("${INSTALL_DIR/#\~/$HOME}/ai-ledger-collector")
  bin_candidates+=("$HOME/.local/bin/ai-ledger-collector" "/usr/local/bin/ai-ledger-collector")

  local seen="|"
  local bin_candidate
  for bin_candidate in "${bin_candidates[@]}"; do
    case "$seen" in
      *"|${bin_candidate}|"*) continue ;;
    esac
    seen="${seen}${bin_candidate}|"
    if [ -f "$bin_candidate" ]; then
      if prompt_yes_no "Remove binary ${bin_candidate}?" "n"; then
        if [ -w "$(dirname "$bin_candidate")" ]; then
          rm -f "$bin_candidate"
        else
          sudo rm -f "$bin_candidate"
        fi
        ok "Removed ${bin_candidate}"
      fi
    fi
  done

  if [ -f "$HOME/.config/ai-ledger/collector.json" ] || [ -f "$HOME/.local/state/ai-ledger/collector-state.json" ]; then
    if prompt_yes_no "Remove local collector config and sync state (~/.config/ai-ledger, ~/.local/state/ai-ledger)?" "n"; then
      rm -rf "$HOME/.config/ai-ledger" "$HOME/.local/state/ai-ledger"
      ok "Removed local config and sync state"
    else
      info "Kept local config and sync state (your archived server data is untouched)"
    fi
  fi

  ok "Uninstall complete."
  exit 0
}

install_binary() {
  local tmp_dir="$1"
  local tmp_bin="${tmp_dir}/ai-ledger-collector"
  local repo_root=""
  repo_root="$(find_local_repo_root || true)"

  # 1. Local clone build or prebuilt bin/
  if [ -n "$repo_root" ]; then
    if command -v go >/dev/null 2>&1; then
      info "Building ai-ledger-collector from local repository (${repo_root})..."
      local ver="dev"
      ver="$(git -C "$repo_root" rev-parse --short=7 HEAD 2>/dev/null || echo dev)"
      (cd "$repo_root" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${ver}" -o "$tmp_bin" ./cmd/ai-ledger-collector)
      ok "Built ai-ledger-collector (${ver})"
    elif [ -x "$repo_root/bin/ai-ledger-collector" ] && [ "$FROM_SOURCE" = "0" ]; then
      info "Using existing binary at ${repo_root}/bin/ai-ledger-collector"
      cp "$repo_root/bin/ai-ledger-collector" "$tmp_bin"
    fi
  fi

  # 2. Try GitHub Release download if we don't have a binary yet and --from-source wasn't forced
  if [ ! -f "$tmp_bin" ] && [ "$FROM_SOURCE" = "0" ]; then
    local asset="ai-ledger-collector-${OS}-${ARCH}"
    local url
    if [ "$VERSION" = "latest" ]; then
      url="https://github.com/${REPO}/releases/latest/download/${asset}"
    else
      url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}"
    fi
    info "Checking GitHub Releases for ${asset}..."
    if curl -fsSL "$url" -o "$tmp_bin" 2>/dev/null && [ -s "$tmp_bin" ]; then
      chmod +x "$tmp_bin"
      ok "Downloaded ${asset}"
    else
      rm -f "$tmp_bin"
      warn "No pre-built release binary found at ${url}"
    fi
  fi

  # 3. Fallback: clone and build from source with Go
  if [ ! -f "$tmp_bin" ]; then
    if ! command -v go >/dev/null 2>&1; then
      die "No pre-built release binary is available yet and 'go' is not installed. Install Go 1.27+ (https://go.dev/dl/) or publish a GitHub Release first."
    fi
    if ! command -v git >/dev/null 2>&1; then
      die "Cannot build from source because 'git' is not installed."
    fi
    info "Cloning https://github.com/${REPO}.git and building from source..."
    git clone --depth 1 "https://github.com/${REPO}.git" "${tmp_dir}/src" >/dev/null 2>&1
    local ver
    ver="$(git -C "${tmp_dir}/src" rev-parse --short=7 HEAD 2>/dev/null || echo dev)"
    (cd "${tmp_dir}/src" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${ver}" -o "$tmp_bin" ./cmd/ai-ledger-collector)
    ok "Built ai-ledger-collector (${ver}) from source"
  fi

  chmod 0755 "$tmp_bin"

  # Choose install directory
  if [ -z "$INSTALL_DIR" ]; then
    local default_dir="$HOME/.local/bin"
    if [ -w "/usr/local/bin" ]; then
      default_dir="/usr/local/bin"
    fi
    if [ "$AUTO_YES" = "1" ] || ! has_tty; then
      INSTALL_DIR="$default_dir"
    else
      local chosen=""
      prompt_read "Install directory [${default_dir}]: " chosen 0
      INSTALL_DIR="${chosen:-$default_dir}"
    fi
  fi
  INSTALL_DIR="${INSTALL_DIR/#\~/$HOME}"
  BIN_PATH="${INSTALL_DIR}/ai-ledger-collector"

  if [ -d "$INSTALL_DIR" ] && [ -w "$INSTALL_DIR" ]; then
    cp "$tmp_bin" "$BIN_PATH"
    chmod 0755 "$BIN_PATH"
  elif mkdir -p "$INSTALL_DIR" 2>/dev/null && [ -w "$INSTALL_DIR" ]; then
    cp "$tmp_bin" "$BIN_PATH"
    chmod 0755 "$BIN_PATH"
  else
    info "Installing to ${BIN_PATH} (requires sudo)..."
    sudo mkdir -p "$INSTALL_DIR"
    sudo cp "$tmp_bin" "$BIN_PATH"
    sudo chmod 0755 "$BIN_PATH"
  fi
  ok "Installed binary to ${BIN_PATH}"

  case ":$PATH:" in
    *":${INSTALL_DIR}:"*) ;;
    *)
      warn "${INSTALL_DIR} is not currently in your PATH."
      warn "Add this line to your shell profile (~/.zshrc or ~/.bashrc):"
      warn "  export PATH=\"${INSTALL_DIR}:\$PATH\""
      ;;
  esac
}

configure_collector() {
  local existing_server existing_token
  existing_server="$(read_existing_config_field "server_url")"
  existing_token="$(read_existing_config_field "device_token")"

  while true; do
    if [ -z "$SERVER_URL" ]; then
      local default_server="${existing_server:-http://127.0.0.1:8484}"
      if [ "$AUTO_YES" = "1" ] || ! has_tty; then
        SERVER_URL="$default_server"
      else
        local input_server=""
        prompt_read "AI Ledger server URL [${default_server}]: " input_server 0
        SERVER_URL="${input_server:-$default_server}"
      fi
    fi

    # Add http:// if user entered bare host:port
    if [[ ! "$SERVER_URL" =~ ^https?:// ]]; then
      SERVER_URL="http://${SERVER_URL}"
    fi
    SERVER_URL="${SERVER_URL%/}"

    info "Checking server connectivity (${SERVER_URL}/api/health)..."
    if curl -fsS --connect-timeout 5 --max-time 8 "${SERVER_URL}/api/health" >/dev/null 2>&1; then
      ok "Connected to AI Ledger server at ${SERVER_URL}"
      break
    else
      warn "Could not reach ${SERVER_URL}/api/health"
      if [ "$AUTO_YES" = "1" ] || ! has_tty; then
        warn "Continuing anyway (--yes / non-interactive mode)"
        break
      fi
      if prompt_yes_no "Re-enter server URL?" "y"; then
        SERVER_URL=""
        continue
      fi
      break
    fi
  done

  if [ -z "$DEVICE_TOKEN" ]; then
    if [ -n "$existing_token" ]; then
      if prompt_yes_no "Keep existing device token from ~/.config/ai-ledger/collector.json?" "y"; then
        DEVICE_TOKEN="$existing_token"
      fi
    fi
  fi

  if [ -z "$DEVICE_TOKEN" ]; then
    printf '\n%sCreate a device token in the AI Ledger web UI (Devices & Archive -> Add Device)%s\n' "$C_DIM" "$C_RESET"
    printf '%sor on your server with: docker exec ai-ledger ai-ledger-server token create --name %s --data /data%s\n\n' "$C_DIM" "$(hostname -s 2>/dev/null || echo my-device)" "$C_RESET"
    while [ -z "$DEVICE_TOKEN" ]; do
      if ! has_tty; then
        die "No device token provided. Pass --token ail_... when running non-interactively."
      fi
      prompt_read "Device token (ail_..., hidden input): " DEVICE_TOKEN 1
      DEVICE_TOKEN="$(printf '%s' "$DEVICE_TOKEN" | xargs)"
      [ -z "$DEVICE_TOKEN" ] && warn "Device token cannot be empty."
    done
  fi

  "$BIN_PATH" init --server "$SERVER_URL" --token "$DEVICE_TOKEN"
  ok "Saved config to ~/.config/ai-ledger/collector.json (permissions 0600)"

  printf '\n'
  info "Scanning local AI session directories..."
  "$BIN_PATH" status || true
}

maybe_offer_claude_retention() {
  local settings="$HOME/.claude/settings.json"
  [ -d "$HOME/.claude" ] || return 0
  if [ -f "$settings" ] && grep -q "cleanupPeriodDays" "$settings" 2>/dev/null; then
    return 0
  fi

  printf '\n'
  warn "Claude Code deletes local transcripts after 30 days by default."
  if [ "$AUTO_YES" = "1" ] || ! has_tty; then
    info "Tip: add \"cleanupPeriodDays\": 99999 to ${settings} to prevent pruning."
    return 0
  fi

  if command -v python3 >/dev/null 2>&1; then
    if prompt_yes_no "Set \"cleanupPeriodDays\": 99999 in ${settings} so older Claude sessions are not pruned?" "n"; then
      python3 - "$settings" <<'PY'
import json, os, sys
path = sys.argv[1]
data = {}
if os.path.exists(path):
    with open(path, "r", encoding="utf-8") as f:
        text = f.read().strip()
        if text:
            data = json.loads(text)
data["cleanupPeriodDays"] = 99999
with open(path, "w", encoding="utf-8") as f:
    json.dump(data, f, indent=2)
    f.write("\n")
PY
      ok "Updated ${settings} with cleanupPeriodDays=99999"
    fi
  else
    info "Tip: add \"cleanupPeriodDays\": 99999 to ${settings} to prevent pruning."
  fi
}

install_macos_service() {
  local interval_sec="$1"
  local agents_dir="$HOME/Library/LaunchAgents"
  local logs_dir="$HOME/Library/Logs"
  local plist="${agents_dir}/${LABEL}.plist"
  local out_log="${logs_dir}/ai-ledger-collector.out.log"
  local err_log="${logs_dir}/ai-ledger-collector.err.log"

  mkdir -p "$agents_dir" "$logs_dir"

  cat >"$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${LABEL}</string>
    <key>ProgramArguments</key>
    <array>
        <string>${BIN_PATH}</string>
        <string>sync</string>
    </array>
    <key>StartInterval</key>
    <integer>${interval_sec}</integer>
    <key>RunAtLoad</key>
    <true/>
    <key>StandardOutPath</key>
    <string>${out_log}</string>
    <key>StandardErrorPath</key>
    <string>${err_log}</string>
</dict>
</plist>
EOF

  local domain="gui/$(id -u)"
  launchctl bootout "$domain" "$plist" 2>/dev/null || true
  launchctl bootstrap "$domain" "$plist"

  ok "Installed and started macOS LaunchAgent (syncs every ${interval_sec}s)"
  printf '  Plist: %s\n' "$plist"
  printf '  Logs:  tail -f %s %s\n' "$out_log" "$err_log"
  printf '  Stop:  launchctl bootout %s %s\n' "$domain" "$plist"
}

install_linux_service() {
  local interval_dur="$1"

  if command -v systemctl >/dev/null 2>&1 && systemctl --user status >/dev/null 2>&1; then
    local unit_dir="$HOME/.config/systemd/user"
    local unit="${unit_dir}/${SERVICE_NAME}"
    mkdir -p "$unit_dir"

    cat >"$unit" <<EOF
[Unit]
Description=AI Ledger Collector
After=network-online.target tailscaled.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=${BIN_PATH} daemon --interval ${interval_dur}
Restart=on-failure
RestartSec=30

[Install]
WantedBy=default.target
EOF

    systemctl --user daemon-reload
    systemctl --user enable --now "$SERVICE_NAME"
    ok "Installed and started systemd user service (interval ${interval_dur})"
    printf '  Status: systemctl --user status %s\n' "$SERVICE_NAME"
    printf '  Logs:   journalctl --user -u %s -f\n' "$SERVICE_NAME"

    if command -v loginctl >/dev/null 2>&1; then
      local linger
      linger="$(loginctl show-user "$USER" -p Linger --value 2>/dev/null || echo no)"
      if [ "$linger" != "yes" ]; then
        warn "User lingering is currently disabled (systemd user services pause when logged out)."
        warn "To keep syncing while logged out of SSH, run: sudo loginctl enable-linger ${USER}"
      fi
    fi
    return 0
  fi

  warn "systemd user session is not active on this machine."
  if command -v crontab >/dev/null 2>&1; then
    local mins
    mins="$(parse_interval_minutes "$interval_dur")"
    if prompt_yes_no "Install a user crontab job every ${mins} minute(s) instead?" "y"; then
      mkdir -p "$HOME/.local/state/ai-ledger"
      local log_file="$HOME/.local/state/ai-ledger/collector.log"
      local cron_line="*/${mins} * * * * ${BIN_PATH} sync >> ${log_file} 2>&1"
      local existing
      existing="$(crontab -l 2>/dev/null | grep -v "ai-ledger-collector" || true)"
      {
        [ -n "$existing" ] && printf '%s\n' "$existing"
        printf '%s\n' "$cron_line"
      } | crontab -
      ok "Installed user crontab entry (every ${mins}m)"
      printf '  Logs: %s\n' "$log_file"
      return 0
    fi
  fi

  warn "Skipped background service setup. Run '${BIN_PATH} daemon --interval ${interval_dur}' manually."
}

main() {
  if [ "$UNINSTALL" = "1" ]; then
    uninstall_collector
  fi

  detect_platform

  printf '\n%sAI Ledger Collector Setup%s (%s/%s)\n\n' "$C_BOLD" "$C_RESET" "$OS" "$ARCH"

  TMP_DIR="$(mktemp -d)"
  trap 'rm -rf "${TMP_DIR:-}"' EXIT

  install_binary "$TMP_DIR"
  printf '\n'
  configure_collector
  maybe_offer_claude_retention

  # Optional initial sync
  printf '\n'
  local do_sync=0
  case "$(printf '%s' "$RUN_SYNC" | tr '[:upper:]' '[:lower:]')" in
    y|yes|true|1) do_sync=1 ;;
    n|no|false|0) do_sync=0 ;;
    "")
      if prompt_yes_no "Run an initial sync now?" "y"; then
        do_sync=1
      fi
      ;;
    *) die "Invalid --sync value: ${RUN_SYNC} (use yes or no)" ;;
  esac

  if [ "$do_sync" = "1" ]; then
    info "Running initial sync..."
    if "$BIN_PATH" sync; then
      ok "Initial sync complete!"
    else
      warn "Initial sync encountered an error. Check the output above and your token/server URL."
    fi
  fi

  # Optional background service
  printf '\n'
  local do_service=0
  case "$(printf '%s' "$INSTALL_SERVICE" | tr '[:upper:]' '[:lower:]')" in
    y|yes|true|1) do_service=1 ;;
    n|no|false|0) do_service=0 ;;
    "")
      local svc_label="background service"
      [ "$OS" = "darwin" ] && svc_label="macOS LaunchAgent (syncs automatically in the background)"
      [ "$OS" = "linux" ] && svc_label="Linux systemd user service (syncs automatically in the background)"
      if prompt_yes_no "Install as a ${svc_label}?" "y"; then
        do_service=1
      fi
      ;;
    *) die "Invalid --service value: ${INSTALL_SERVICE} (use yes or no)" ;;
  esac

  if [ "$do_service" = "1" ]; then
    if [ "$AUTO_YES" = "0" ] && has_tty && [ -z "$INSTALL_SERVICE" ]; then
      local chosen_interval=""
      prompt_read "Sync interval [${INTERVAL}]: " chosen_interval 0
      INTERVAL="${chosen_interval:-$INTERVAL}"
    fi
    local interval_sec
    interval_sec="$(parse_interval_seconds "$INTERVAL")"

    if [ "$OS" = "darwin" ]; then
      install_macos_service "$interval_sec"
    else
      install_linux_service "$INTERVAL"
    fi
  else
    info "Background service not installed. You can sync manually anytime with:"
    printf '  %s sync\n' "$BIN_PATH"
    printf '  %s daemon --interval %s\n' "$BIN_PATH" "$INTERVAL"
  fi

  printf '\n%sAll set!%s Open %s to view your synced sessions.\n' "$C_GREEN$C_BOLD" "$C_RESET" "$SERVER_URL"
}

main "$@"
