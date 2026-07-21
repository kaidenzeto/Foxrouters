#!/usr/bin/env bash
# tunnel.sh — Cloudflare Tunnel management for FoxRouters
#
# Exposes the local FoxRouters gateway (foxrouters:20130 inside the
# foxrouters-net Docker network) via a Cloudflare Tunnel.
#
# Three modes:
#   quick  — random *.trycloudflare.com URL, no account needed.
#            URL changes on every restart (not persistent).
#   named  — custom domain via Cloudflare API (auto-configured).
#            Needs CLOUDFLARE_API_TOKEN + CLOUDFLARE_ACCOUNT_ID +
#            CLOUDFLARE_ZONE_ID + TUNNEL_DOMAIN.
#            Persistent URL, 1 container, no cert.pem/config.yml needed.
#   hybrid — BOTH quick + named running simultaneously (2 containers).
#
# Usage:
#   ./tunnel.sh enable [--quick|--named|--hybrid]  Start tunnel(s).
#   ./tunnel.sh disable                            Stop + remove all tunnel containers.
#   ./tunnel.sh status                             Show all tunnel states + URLs.
#   ./tunnel.sh url                                Print all running tunnel URLs.
#   ./tunnel.sh restart                            Restart (keeps the same mode).
#   ./tunnel.sh logs [quick|named] [-f]            Tail cloudflared logs.
#   ./tunnel.sh setup-named                        Interactive named tunnel setup
#                                                  (prompts for CF creds, saves to env).
#
# Named tunnel via API (no manual cloudflared login needed):
#   Requires env vars (or /etc/foxrouters/.env):
#     CLOUDFLARE_API_TOKEN  — API token with Tunnel:Edit + DNS:Edit perms
#     CLOUDFLARE_ACCOUNT_ID — account ID (dashboard URL or API)
#     CLOUDFLARE_ZONE_ID    — zone ID for the domain
#     TUNNEL_DOMAIN         — e.g. gateway.example.com
#   The script auto-creates the tunnel, configures ingress, routes DNS,
#   and starts cloudflared with --token (no cert.pem/credentials JSON).

set -euo pipefail

# ── Config ──────────────────────────────────────────────────────────────────
CONTAINER_QUICK="foxrouters-tunnel-quick"
CONTAINER_NAMED="foxrouters-tunnel-named"
IMAGE="cloudflare/cloudflared:latest"
NETWORK="foxrouters-net"
UPSTREAM="http://foxrouters:20130"
CONFIG_DIR="/etc/foxrouters/cloudflared"
STATE_FILE="${CONFIG_DIR}/mode"
ENV_FILE="/etc/foxrouters/.env"

# ── Colors ──────────────────────────────────────────────────────────────────
red()    { printf '\033[31m%s\033[0m\n' "$*"; }
green()  { printf '\033[32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }
bold()   { printf '\033[1m%s\033[0m\n' "$*"; }
info()   { printf '\033[36m[i]\033[0m %s\n' "$*"; }
ok()     { printf '\033[32m[✓]\033[0m %s\n' "$*"; }
err()    { printf '\033[31m[✗]\033[0m %s\n' "$*"; }

# ── Helpers ─────────────────────────────────────────────────────────────────
need_docker() {
    if ! command -v docker &>/dev/null; then
        err "Docker not found. Install it first."
        exit 1
    fi
    if ! docker info &>/dev/null; then
        err "Docker daemon not running. systemctl start docker"
        exit 1
    fi
}

ensure_network() {
    if ! docker network inspect "${NETWORK}" &>/dev/null; then
        err "Docker network '${NETWORK}' not found. Run install.sh first."
        exit 1
    fi
}

is_running() {
    local c="$1"
    [[ "$(docker inspect -f '{{.State.Running}}' "$c" 2>/dev/null || echo false)" == "true" ]]
}

exists() {
    local c="$1"
    docker inspect "$c" &>/dev/null
}

save_mode() {
    mkdir -p "${CONFIG_DIR}"
    echo "$1" > "${STATE_FILE}"
}

load_mode() {
    if [[ -r "${STATE_FILE}" ]]; then
        cat "${STATE_FILE}"
    else
        echo "quick"
    fi
}

# Load env vars from .env file if present
load_env() {
    if [[ -f "${ENV_FILE}" ]]; then
        set -a
        # shellcheck disable=SC1090
        source "${ENV_FILE}" 2>/dev/null || true
        set +a
    fi
}

# Extract the *.trycloudflare.com URL from cloudflared logs.
capture_quick_url() {
    local c="$1"
    local url=""
    for _ in $(seq 1 30); do
        url=$(docker logs "$c" 2>&1 \
              | grep -oE 'https://[a-zA-Z0-9.-]+\.trycloudflare\.com' \
              | head -1 || true)
        if [[ -n "${url}" ]]; then
            echo "${url}"
            return 0
        fi
        sleep 1
    done
    return 1
}

# ── Quick tunnel ────────────────────────────────────────────────────────────
start_quick() {
    info "Starting quick tunnel → ${UPSTREAM}"
    docker rm -f "${CONTAINER_QUICK}" 2>/dev/null || true
    docker run -d \
        --name "${CONTAINER_QUICK}" \
        --network "${NETWORK}" \
        --restart unless-stopped \
        "${IMAGE}" \
        tunnel --no-autoupdate --url "${UPSTREAM}" >/dev/null
    ok "Container '${CONTAINER_QUICK}' started (quick mode)"

    info "Waiting for quick tunnel URL (up to 30s)..."
    if URL=$(capture_quick_url "${CONTAINER_QUICK}"); then
        echo ""
        bold "  Quick Tunnel URL: ${URL}"
        echo ""
        yellow "  ⚠  Quick tunnels are ephemeral — the URL changes on every restart."
        yellow "     Use named tunnel for a persistent custom domain."
    else
        err "Could not capture quick tunnel URL. Check: docker logs ${CONTAINER_QUICK}"
        return 1
    fi
}

# ── Named tunnel via Cloudflare API ─────────────────────────────────────────
# Creates tunnel via API, configures ingress, routes DNS, starts container
# with --token. No cert.pem, credentials JSON, or config.yml needed.

check_cf_creds() {
    load_env
    local missing=()
    [[ -z "${CLOUDFLARE_API_TOKEN:-}" ]]  && missing+=("CLOUDFLARE_API_TOKEN")
    [[ -z "${CLOUDFLARE_ACCOUNT_ID:-}" ]] && missing+=("CLOUDFLARE_ACCOUNT_ID")
    [[ -z "${CLOUDFLARE_ZONE_ID:-}" ]]    && missing+=("CLOUDFLARE_ZONE_ID")
    [[ -z "${TUNNEL_DOMAIN:-}" ]]         && missing+=("TUNNEL_DOMAIN")
    if [[ ${#missing[@]} -gt 0 ]]; then
        err "Missing Cloudflare credentials: ${missing[*]}"
        echo ""
        echo "  Set these in ${ENV_FILE} or as env vars:"
        echo "    CLOUDFLARE_API_TOKEN  — https://dash.cloudflare.com/profile/api-tokens"
        echo "                           (create token with Tunnel:Edit + DNS:Edit)"
        echo "    CLOUDFLARE_ACCOUNT_ID — found in dashboard URL or API"
        echo "    CLOUDFLARE_ZONE_ID    — domain zone ID (dashboard > Overview > right sidebar)"
        echo "    TUNNEL_DOMAIN         — e.g. gateway.example.com"
        echo ""
        echo "  Or run: ./tunnel.sh setup-named  (interactive setup)"
        return 1
    fi
}

# Create tunnel via API, return connector token
create_tunnel_via_api() {
    local tunnel_name="foxrouters"
    local resp
    resp=$(curl -s -X POST \
        "https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID}/cfd_tunnel" \
        -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" \
        -H "Content-Type: application/json" \
        -d "{\"name\":\"${tunnel_name}\",\"tunnel_secret\":\"$(openssl rand -hex 32)\"}" 2>&1)

    if ! echo "$resp" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d.get('success')" 2>/dev/null; then
        # Check if tunnel already exists
        local existing
        existing=$(curl -s "https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID}/cfd_tunnel?name=${tunnel_name}" \
            -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" 2>&1)
        local tid
        tid=$(echo "$existing" | python3 -c "
import sys,json
d=json.load(sys.stdin)
if d.get('success') and d.get('result'):
    print(d['result'][0]['id'])
" 2>/dev/null || true)
        if [[ -n "$tid" ]]; then
            info "Tunnel '${tunnel_name}' already exists (id: ${tid}), reusing."
            # Get token for existing tunnel
            local token_resp
            token_resp=$(curl -s "https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID}/cfd_tunnel/${tid}/token" \
                -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" 2>&1)
            echo "$token_resp" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['result'])" 2>/dev/null
            echo "TUNNEL_ID=${tid}"
            return 0
        fi
        err "Failed to create tunnel: $(echo "$resp" | head -c 200)"
        return 1
    fi

    # Extract tunnel ID + token
    local tid token
    tid=$(echo "$resp" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['result']['id'])" 2>/dev/null)
    token=$(echo "$resp" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['result'].get('token',''))" 2>/dev/null)

    if [[ -z "$tid" ]]; then
        err "Could not parse tunnel ID from API response"
        return 1
    fi

    # If no token in create response, fetch it
    if [[ -z "$token" ]]; then
        token=$(curl -s "https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID}/cfd_tunnel/${tid}/token" \
            -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" 2>&1 \
            | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['result'])" 2>/dev/null)
    fi

    echo "$token"
    echo "TUNNEL_ID=${tid}"
}

# Configure ingress rules for tunnel
configure_tunnel_ingress() {
    local tid="$1"
    local domain="$2"
    local resp
    resp=$(curl -s -X PUT \
        "https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID}/cfd_tunnel/${tid}/configurations" \
        -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" \
        -H "Content-Type: application/json" \
        -d "{
            \"config\": {
                \"ingress\": [
                    {\"hostname\": \"${domain}\", \"service\": \"${UPSTREAM}\"},
                    {\"service\": \"http_status:404\"}
                ]
            }
        }" 2>&1)

    if ! echo "$resp" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d.get('success')" 2>/dev/null; then
        err "Failed to configure ingress: $(echo "$resp" | head -c 200)"
        return 1
    fi
    ok "Ingress configured: ${domain} → ${UPSTREAM}"
}

# Route DNS CNAME to tunnel
route_dns() {
    local tid="$1"
    local domain="$2"
    # Check if DNS record already exists
    local existing
    existing=$(curl -s "https://api.cloudflare.com/client/v4/zones/${CLOUDFLARE_ZONE_ID}/dns_records?name=${domain}" \
        -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" 2>&1)

    local record_id
    record_id=$(echo "$existing" | python3 -c "
import sys,json
d=json.load(sys.stdin)
if d.get('success') and d.get('result'):
    print(d['result'][0]['id'])
" 2>/dev/null || true)

    local cname_target="${tid}.cfargotunnel.com"
    if [[ -n "$record_id" ]]; then
        # Update existing
        curl -s -X PUT \
            "https://api.cloudflare.com/client/v4/zones/${CLOUDFLARE_ZONE_ID}/dns_records/${record_id}" \
            -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" \
            -H "Content-Type: application/json" \
            -d "{\"type\":\"CNAME\",\"name\":\"${domain}\",\"content\":\"${cname_target}\",\"proxied\":true}" >/dev/null 2>&1
        ok "DNS updated: ${domain} → ${cname_target}"
    else
        # Create new
        curl -s -X POST \
            "https://api.cloudflare.com/client/v4/zones/${CLOUDFLARE_ZONE_ID}/dns_records" \
            -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" \
            -H "Content-Type: application/json" \
            -d "{\"type\":\"CNAME\",\"name\":\"${domain}\",\"content\":\"${cname_target}\",\"proxied\":true}" >/dev/null 2>&1
        ok "DNS created: ${domain} → ${cname_target}"
    fi
}

start_named() {
    load_env
    check_cf_creds || return 1

    info "Setting up named tunnel via Cloudflare API..."

    # Create tunnel (or reuse existing)
    local api_output
    api_output=$(create_tunnel_via_api) || return 1
    local token tid
    token=$(echo "$api_output" | head -1)
    tid=$(echo "$api_output" | grep '^TUNNEL_ID=' | cut -d= -f2)

    if [[ -z "$token" || -z "$tid" ]]; then
        err "Failed to get tunnel token or ID"
        return 1
    fi
    ok "Tunnel ready (id: ${tid})"

    # Configure ingress
    configure_tunnel_ingress "$tid" "${TUNNEL_DOMAIN}" || return 1

    # Route DNS
    route_dns "$tid" "${TUNNEL_DOMAIN}" || return 1

    # Save token for restarts
    echo "$token" > "${CONFIG_DIR}/named_token" 2>/dev/null || true
    chmod 600 "${CONFIG_DIR}/named_token" 2>/dev/null || true

    # Start container with --token (1 container, no cert.pem/config.yml)
    info "Starting named tunnel container..."
    docker rm -f "${CONTAINER_NAMED}" 2>/dev/null || true
    docker run -d \
        --name "${CONTAINER_NAMED}" \
        --network "${NETWORK}" \
        --restart unless-stopped \
        "${IMAGE}" \
        tunnel --no-autoupdate --token "${token}" >/dev/null
    ok "Container '${CONTAINER_NAMED}' started (named mode)"

    echo ""
    bold "  Named Tunnel URL: https://${TUNNEL_DOMAIN}"
    echo ""
    green "  ✓ Persistent URL — survives restarts"
    green "  ✓ TLS automatic — Cloudflare handles certs"
    echo ""
}

# ── Interactive named setup ─────────────────────────────────────────────────
cmd_setup_named() {
    echo ""
    bold "Cloudflare Named Tunnel Setup"
    echo "  This will auto-create a tunnel via Cloudflare API."
    echo "  You need: API token, Account ID, Zone ID, and domain."
    echo ""

    load_env

    # Prompt for missing values
    local cf_token cf_account cf_zone cf_domain
    cf_token="${CLOUDFLARE_API_TOKEN:-}"
    cf_account="${CLOUDFLARE_ACCOUNT_ID:-}"
    cf_zone="${CLOUDFLARE_ZONE_ID:-}"
    cf_domain="${TUNNEL_DOMAIN:-}"

    [[ -z "$cf_token" ]]  && read -r -p "Cloudflare API Token: " cf_token
    [[ -z "$cf_account" ]] && read -r -p "Account ID: " cf_account
    [[ -z "$cf_zone" ]]    && read -r -p "Zone ID: " cf_zone
    [[ -z "$cf_domain" ]]  && read -r -p "Domain (e.g. gateway.example.com): " cf_domain

    # Save to .env
    info "Saving credentials to ${ENV_FILE}..."
    # Remove old entries
    sed -i '/^CLOUDFLARE_API_TOKEN=/d; /^CLOUDFLARE_ACCOUNT_ID=/d; /^CLOUDFLARE_ZONE_ID=/d; /^TUNNEL_DOMAIN=/d' "${ENV_FILE}" 2>/dev/null || true
    cat >> "${ENV_FILE}" << EOF
CLOUDFLARE_API_TOKEN=${cf_token}
CLOUDFLARE_ACCOUNT_ID=${cf_account}
CLOUDFLARE_ZONE_ID=${cf_zone}
TUNNEL_DOMAIN=${cf_domain}
EOF
    chmod 600 "${ENV_FILE}"
    ok "Credentials saved."

    echo ""
    info "Now run: ./tunnel.sh enable --named"
    echo ""
}

# ── Commands ────────────────────────────────────────────────────────────────
cmd_enable() {
    need_docker
    ensure_network

    local mode="quick"
    case "${1:-}" in
        --quick|quick|"") mode="quick" ;;
        --named|named)    mode="named" ;;
        --hybrid|hybrid)  mode="hybrid" ;;
        *) err "Unknown mode: $1 (use --quick, --named, or --hybrid)"; exit 1 ;;
    esac

    save_mode "${mode}"

    case "${mode}" in
        quick)  start_quick ;;
        named)  start_named ;;
        hybrid)
            info "Hybrid mode: starting BOTH quick + named tunnels..."
            start_quick || true
            echo ""
            start_named || true
            echo ""
            ok "Hybrid mode active — both tunnels running."
            ;;
    esac
}

cmd_disable() {
    need_docker
    local stopped=0
    for c in "${CONTAINER_QUICK}" "${CONTAINER_NAMED}"; do
        if docker inspect "$c" &>/dev/null; then
            info "Stopping ${c}..."
            docker rm -f "$c" >/dev/null
            stopped=1
        fi
    done
    if [[ "${stopped}" == "0" ]]; then
        info "No tunnel containers present."
    else
        ok "All tunnels disabled."
    fi
}

cmd_status() {
    need_docker
    local any=0

    # Quick tunnel
    if exists "${CONTAINER_QUICK}"; then
        any=1
        if is_running "${CONTAINER_QUICK}"; then
            green "Quick Tunnel: RUNNING"
            docker ps --filter "name=^/${CONTAINER_QUICK}$" \
                --format '  container: {{.Names}}   status: {{.Status}}'
            local qurl
            qurl=$(docker logs "${CONTAINER_QUICK}" 2>&1 \
                   | grep -oE 'https://[a-zA-Z0-9.-]+\.trycloudflare\.com' \
                   | tail -1 || true)
            if [[ -n "${qurl}" ]]; then
                echo "  URL: ${qurl}"
            fi
        else
            yellow "Quick Tunnel: STOPPED"
        fi
        echo ""
    fi

    # Named tunnel
    if exists "${CONTAINER_NAMED}"; then
        any=1
        if is_running "${CONTAINER_NAMED}"; then
            green "Named Tunnel: RUNNING"
            docker ps --filter "name=^/${CONTAINER_NAMED}$" \
                --format '  container: {{.Names}}   status: {{.Status}}'
            load_env
            if [[ -n "${TUNNEL_DOMAIN:-}" ]]; then
                echo "  URL: https://${TUNNEL_DOMAIN}"
            fi
        else
            yellow "Named Tunnel: STOPPED"
        fi
    fi

    if [[ "${any}" == "0" ]]; then
        yellow "Tunnels: none installed"
        echo "  Run: ./tunnel.sh enable [--quick|--named|--hybrid]"
    fi
}

cmd_url() {
    need_docker
    local found=0

    # Quick tunnel URL
    if exists "${CONTAINER_QUICK}" && is_running "${CONTAINER_QUICK}"; then
        local qurl
        qurl=$(docker logs "${CONTAINER_QUICK}" 2>&1 \
               | grep -oE 'https://[a-zA-Z0-9.-]+\.trycloudflare\.com' \
               | tail -1 || true)
        if [[ -n "${qurl}" ]]; then
            echo "quick: ${qurl}"
            found=1
        fi
    fi

    # Named tunnel URL
    if exists "${CONTAINER_NAMED}" && is_running "${CONTAINER_NAMED}"; then
        load_env
        if [[ -n "${TUNNEL_DOMAIN:-}" ]]; then
            echo "named: https://${TUNNEL_DOMAIN}"
            found=1
        fi
    fi

    if [[ "${found}" == "0" ]]; then
        err "No running tunnels. Run: ./tunnel.sh enable"
        return 1
    fi
}

cmd_restart() {
    need_docker
    ensure_network
    local mode
    mode=$(load_mode)
    info "Restarting tunnels in '${mode}' mode..."
    cmd_disable 2>/dev/null || true
    sleep 1
    cmd_enable "--${mode}"
}

cmd_logs() {
    need_docker
    local target="${1:-}"
    shift || true
    local follow=""
    [[ "${1:-}" == "-f" || "${1:-}" == "--follow" ]] && follow="-f"

    case "${target}" in
        quick)
            if ! exists "${CONTAINER_QUICK}"; then
                err "No quick tunnel container. Run: ./tunnel.sh enable --quick"
                exit 1
            fi
            docker logs ${follow} --tail 100 "${CONTAINER_QUICK}"
            ;;
        named)
            if ! exists "${CONTAINER_NAMED}"; then
                err "No named tunnel container. Run: ./tunnel.sh enable --named"
                exit 1
            fi
            docker logs ${follow} --tail 100 "${CONTAINER_NAMED}"
            ;;
        "")
            local shown=0
            for c in "${CONTAINER_QUICK}" "${CONTAINER_NAMED}"; do
                if exists "$c"; then
                    echo "=== ${c} ==="
                    docker logs --tail 50 "$c" 2>&1 | tail -20
                    echo ""
                    shown=1
                fi
            done
            [[ "${shown}" == "0" ]] && err "No tunnel containers found."
            ;;
        *)
            err "Unknown target: ${target} (use 'quick', 'named', or omit for both)"
            exit 1
            ;;
    esac
}

usage() {
    sed -n '2,33p' "$0"
    exit 1
}

# ── Dispatch ────────────────────────────────────────────────────────────────
case "${1:-}" in
    enable)      shift; cmd_enable  "${1:-}" ;;
    disable)     cmd_disable ;;
    status)      cmd_status ;;
    url)         cmd_url ;;
    restart)     cmd_restart ;;
    logs)        shift; cmd_logs "${1:-}" "${2:-}" ;;
    setup-named) cmd_setup_named ;;
    -h|--help|help|"") usage ;;
    *) err "Unknown command: $1"; usage ;;
esac
