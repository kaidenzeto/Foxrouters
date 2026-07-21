#!/usr/bin/env bash
# tunnel.sh — Cloudflare Tunnel management for FoxRouters
#
# Exposes the local FoxRouters gateway (foxrouters:20130 inside the
# foxrouters-net Docker network) via a Cloudflare Tunnel. Two modes:
#
#   quick — random *.trycloudflare.com URL, no Cloudflare account needed.
#           URL changes on every restart (not persistent).
#   named — custom domain via a persistent tunnel. Requires a Cloudflare
#           account, a zone, and a pre-existing cert.pem + tunnel credentials
#           JSON at /etc/foxrouters/cloudflared/ (see NAMED SETUP below).
#
# Usage:
#   ./tunnel.sh enable [--quick|--named|--hybrid]  Start tunnel(s).
#                                                  quick: random URL only
#                                                  named: custom domain only
#                                                  hybrid: BOTH quick + named
#   ./tunnel.sh disable                            Stop + remove all tunnel containers.
#   ./tunnel.sh status                             Show all tunnel states + URLs.
#   ./tunnel.sh url                                Print all running tunnel URLs.
#   ./tunnel.sh restart                            Restart (keeps the same mode).
#   ./tunnel.sh logs [quick|named] [-f]            Tail cloudflared logs.
#
# NAMED SETUP (once, on host):
#   1. cloudflared tunnel login
#        → writes ~/.cloudflared/cert.pem
#   2. cloudflared tunnel create foxrouters
#        → writes ~/.cloudflared/<tunnel-id>.json
#   3. Copy both to the shared config dir:
#        sudo mkdir -p /etc/foxrouters/cloudflared
#        sudo cp ~/.cloudflared/cert.pem            /etc/foxrouters/cloudflared/
#        sudo cp ~/.cloudflared/<tunnel-id>.json    /etc/foxrouters/cloudflared/
#   4. Write /etc/foxrouters/cloudflared/config.yml:
#        tunnel: <tunnel-id>
#        credentials-file: /etc/cloudflared/<tunnel-id>.json
#        ingress:
#          - hostname: gateway.example.com
#            service: http://foxrouters:20130
#          - service: http_status:404
#   5. cloudflared tunnel route dns foxrouters gateway.example.com
#   6. ./tunnel.sh enable --named
#
set -euo pipefail

# ── Config ──────────────────────────────────────────────────────────────────
CONTAINER_QUICK="foxrouters-tunnel-quick"
CONTAINER_NAMED="foxrouters-tunnel-named"
IMAGE="cloudflare/cloudflared:latest"
NETWORK="foxrouters-net"
UPSTREAM="http://foxrouters:20130"
CONFIG_DIR="/etc/foxrouters/cloudflared"
STATE_FILE="${CONFIG_DIR}/mode"   # remembers last-used mode for `restart`

# ── Colors ──────────────────────────────────────────────────────────────────
red()    { printf '\033[31m%s\033[0m\n' "$*"; }
green()  { printf '\033[32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }
bold()   { printf '\033[1m%s\033[0m\n' "$*"; }
info()   { printf '\033[36m[i]\033[0m %s\n' "$*"; }
ok()     { printf '\033[32m[✓]\033[0m %s\n' "$*"; }
err()    { printf '\033[31m[✗]\033[0m %s\n' "$*"; }

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

# Extract the *.trycloudflare.com URL from cloudflared logs. Quick tunnels
# print a banner like "https://<slug>.trycloudflare.com" once the tunnel is
# established — polls up to ~30s.
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

start_named() {
    info "Starting named tunnel from ${CONFIG_DIR}"
    if [[ ! -d "${CONFIG_DIR}" ]]; then
        err "Config dir ${CONFIG_DIR} missing. See NAMED SETUP in tunnel.sh."
        return 1
    fi
    if [[ ! -f "${CONFIG_DIR}/config.yml" ]]; then
        err "${CONFIG_DIR}/config.yml missing. See NAMED SETUP in tunnel.sh."
        return 1
    fi
    if ! ls "${CONFIG_DIR}"/*.json &>/dev/null; then
        err "No <tunnel-id>.json credentials found in ${CONFIG_DIR}."
        err "Run 'cloudflared tunnel create foxrouters' and copy the JSON here."
        return 1
    fi

    docker rm -f "${CONTAINER_NAMED}" 2>/dev/null || true
    docker run -d \
        --name "${CONTAINER_NAMED}" \
        --network "${NETWORK}" \
        --restart unless-stopped \
        -v "${CONFIG_DIR}:/etc/cloudflared:ro" \
        "${IMAGE}" \
        tunnel --no-autoupdate --config /etc/cloudflared/config.yml run >/dev/null
    ok "Container '${CONTAINER_NAMED}' started (named mode)"

    # Best-effort: pull the hostname out of config.yml so the user sees the URL
    # without hunting through cloudflared logs.
    HOSTNAME=$(grep -E '^\s*-?\s*hostname:' "${CONFIG_DIR}/config.yml" \
               | head -1 | awk -F: '{print $2}' | tr -d ' ' || true)
    if [[ -n "${HOSTNAME}" ]]; then
        echo ""
        bold "  Named Tunnel URL: https://${HOSTNAME}"
        echo ""
    else
        info "Named tunnel started. Check the hostname(s) in ${CONFIG_DIR}/config.yml"
    fi
}

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
            if [[ -f "${CONFIG_DIR}/config.yml" ]]; then
                local host
                host=$(grep -E '^\s*-?\s*hostname:' "${CONFIG_DIR}/config.yml" \
                       | head -1 | awk -F: '{print $2}' | tr -d ' ')
                if [[ -n "${host}" ]]; then
                    echo "  URL: https://${host}"
                fi
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
        if [[ -f "${CONFIG_DIR}/config.yml" ]]; then
            local host
            host=$(grep -E '^\s*-?\s*hostname:' "${CONFIG_DIR}/config.yml" \
                   | head -1 | awk -F: '{print $2}' | tr -d ' ')
            if [[ -n "${host}" ]]; then
                echo "named: https://${host}"
                found=1
            fi
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
            # Show both if they exist
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
    sed -n '2,28p' "$0"
    exit 1
}

# ── Dispatch ────────────────────────────────────────────────────────────────
case "${1:-}" in
    enable)  shift; cmd_enable  "${1:-}" ;;
    disable) cmd_disable ;;
    status)  cmd_status ;;
    url)     cmd_url ;;
    restart) cmd_restart ;;
    logs)    shift; cmd_logs "${1:-}" "${2:-}" ;;
    -h|--help|help|"") usage ;;
    *) err "Unknown command: $1"; usage ;;
esac
