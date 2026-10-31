#!/usr/bin/env bash
# docker-cp installer for Debian/Ubuntu.
#   curl -fsSL https://github.com/OWNER/docker-cp/releases/latest/download/install.sh | sudo bash
#   ... | sudo bash -s -- --update
#   ... | sudo bash -s -- --version v1.2.3
set -euo pipefail

REPO="${DCP_REPO:-OWNER/docker-cp}"
BIN=/usr/local/bin/docker-cp
UNIT=/etc/systemd/system/docker-cp.service
DATA_ROOT=/opt/docker-cp

VERSION=""
UPDATE=0

log() { printf '==> %s\n' "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --update) UPDATE=1; shift ;;
    --version)
      [ $# -ge 2 ] || die "--version needs a value (e.g. v1.2.3)"
      VERSION="$2"; shift 2 ;;
    *) die "unknown argument: $1" ;;
  esac
done

# 1. root + OS check
[ "$(id -u)" -eq 0 ] || die "run as root (use sudo)"
[ -r /etc/os-release ] || die "/etc/os-release not found"
# shellcheck disable=SC1091
. /etc/os-release
OS_ID="${ID:-}"
case " ${OS_ID} ${ID_LIKE:-} " in
  *" debian "* | *" ubuntu "*) ;;
  *) die "unsupported OS '${OS_ID}': only Debian/Ubuntu are supported" ;;
esac
# Docker's apt repo is keyed on debian or ubuntu, not derivatives.
case "$OS_ID" in
  debian | ubuntu) DOCKER_OS="$OS_ID" ;;
  *)
    case " ${ID_LIKE:-} " in
      *" ubuntu "*) DOCKER_OS=ubuntu ;;
      *) DOCKER_OS=debian ;;
    esac ;;
esac
CODENAME="${UBUNTU_CODENAME:-${VERSION_CODENAME:-}}"

# 2. architecture
case "$(uname -m)" in
  x86_64) ARCH=amd64 ;;
  aarch64) ARCH=arm64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

export DEBIAN_FRONTEND=noninteractive

install_docker() {
  if command -v docker >/dev/null 2>&1; then
    log "Docker already installed, skipping"
    return
  fi
  [ -n "$CODENAME" ] || die "cannot determine distro codename for the Docker repo"
  log "Installing Docker CE"
  apt-get update -qq
  apt-get install -y -qq ca-certificates curl
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL "https://download.docker.com/linux/${DOCKER_OS}/gpg" -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/%s %s stable\n' \
    "$(dpkg --print-architecture)" "$DOCKER_OS" "$CODENAME" >/etc/apt/sources.list.d/docker.list
  apt-get update -qq
  apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
}

install_caddy() {
  if command -v caddy >/dev/null 2>&1; then
    log "Caddy already installed, skipping"
    return
  fi
  log "Installing Caddy"
  apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl gpg
  curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' |
    gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' >/etc/apt/sources.list.d/caddy-stable.list
  chmod o+r /usr/share/keyrings/caddy-stable-archive-keyring.gpg /etc/apt/sources.list.d/caddy-stable.list
  apt-get update -qq
  apt-get install -y -qq caddy
}

if [ "$UPDATE" -eq 0 ]; then
  install_docker
  install_caddy
fi

# 4. download + verify
command -v curl >/dev/null 2>&1 || { apt-get update -qq; apt-get install -y -qq curl; }
if [ -z "$VERSION" ]; then
  # The archive name embeds the version, so resolve "latest" first.
  final_url="$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest")"
  VERSION="${final_url##*/}"
  case "$VERSION" in v[0-9]*) ;; *) die "could not resolve latest release of ${REPO}" ;; esac
fi
VER_NUM="${VERSION#v}"
BASE="https://github.com/${REPO}/releases/download/${VERSION}"
ARCHIVE="docker-cp_${VER_NUM}_linux_${ARCH}.tar.gz"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

log "Downloading docker-cp ${VERSION} (${ARCH})"
curl -fsSL "${BASE}/${ARCHIVE}" -o "${TMP}/${ARCHIVE}"
curl -fsSL "${BASE}/checksums.txt" -o "${TMP}/checksums.txt"
CHECK="$(cd "$TMP" && sha256sum -c --ignore-missing checksums.txt)" || die "checksum verification failed"
printf '%s\n' "$CHECK"
printf '%s\n' "$CHECK" | grep -q "^${ARCHIVE}: OK$" || die "checksums.txt has no entry for ${ARCHIVE}"

tar -xzf "${TMP}/${ARCHIVE}" -C "$TMP" docker-cp
[ -f "${TMP}/docker-cp" ] || die "binary not found in archive"

# 5. install binary, dirs, unit
install -m 0755 "${TMP}/docker-cp" "$BIN"
install -d -m 0700 "${DATA_ROOT}/data" "${DATA_ROOT}/stacks"
cat >"$UNIT" <<'UNITEOF'
[Unit]
Description=docker-cp control panel
After=network-online.target docker.service caddy.service
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/docker-cp serve
Restart=always
RestartSec=2
Environment=GOMEMLIMIT=40MiB
Environment=GOGC=50

[Install]
WantedBy=multi-user.target
UNITEOF
chmod 0644 "$UNIT"
systemctl daemon-reload

PANEL_DOMAIN=""
if [ "$UPDATE" -eq 0 ]; then
  # 6. admin setup; stdin is the curl pipe, so prompt via /dev/tty
  [ -r /dev/tty ] && [ -w /dev/tty ] || die "no terminal available for prompts; run from an interactive shell"
  ADMIN_USER="admin"
  while :; do
    printf 'Admin password (min 8 chars, hidden): ' >/dev/tty
    IFS= read -rs PW </dev/tty
    printf '\n' >/dev/tty
    printf 'Repeat password: ' >/dev/tty
    IFS= read -rs PW2 </dev/tty
    printf '\n' >/dev/tty
    if [ -z "$PW" ]; then
      echo "Password must not be empty." >/dev/tty
    elif [ "$PW" != "$PW2" ]; then
      echo "Passwords do not match, try again." >/dev/tty
    else
      break
    fi
  done
  printf 'Panel domain (optional, e.g. panel.example.com): ' >/dev/tty
  IFS= read -r PANEL_DOMAIN </dev/tty || true
  printf 'ACME email (optional): ' >/dev/tty
  IFS= read -r ACME_EMAIL </dev/tty || true

  log "Creating admin account"
  printf '%s\n' "$PW" | "$BIN" setup --username "$ADMIN_USER" --panel-domain "$PANEL_DOMAIN" --acme-email "$ACME_EMAIL"
  unset PW PW2

  printf 'Configure ufw firewall (allow SSH, 80, 443, 8800)? [y/N]: ' >/dev/tty
  IFS= read -r ANS </dev/tty || true
  case "$ANS" in
    y | Y | yes | YES)
      if ! command -v ufw >/dev/null 2>&1; then
        apt-get install -y -qq ufw
      fi
      SSH_PORT="$(sshd -T 2>/dev/null | awk '/^port /{print $2; exit}')"
      SSH_PORT="${SSH_PORT:-22}"
      log "Allowing SSH on port ${SSH_PORT}, then 80, 443, 8800"
      ufw allow "${SSH_PORT}/tcp"
      ufw allow 80/tcp
      ufw allow 443/tcp
      ufw allow 8800/tcp
      ufw --force enable
      ;;
    *) log "Skipping ufw" ;;
  esac
fi

# 7. start services
if [ "$UPDATE" -eq 1 ]; then
  systemctl restart docker-cp
else
  systemctl enable --now caddy docker docker-cp
fi

IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
echo
log "docker-cp ${VERSION} is running"
echo "    URL:    http://${IP:-<server-ip>}:8800"
if [ -n "$PANEL_DOMAIN" ]; then
  echo "    Domain: https://${PANEL_DOMAIN}"
fi
