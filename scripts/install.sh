#!/usr/bin/env bash
# docker-cp installer for Debian/Ubuntu.
#   curl -fsSL https://github.com/OWNER/docker-cp/releases/latest/download/install.sh | sudo bash
#   ... | sudo bash -s -- --update
#   ... | sudo bash -s -- --version v1.2.3
#
# Everything lives in functions and main() is invoked on the last line, so a
# truncated download never executes a partial script.
set -euo pipefail

REPO="${DCP_REPO:-OWNER/docker-cp}"
BIN=/usr/local/bin/docker-cp
UNIT=/etc/systemd/system/docker-cp.service
DATA_ROOT=/opt/docker-cp
STATE="${DATA_ROOT}/data/state.json"

log() { printf '==> %s\n' "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

tty_ok() { (: </dev/tty) 2>/dev/null; }

detect_os() {
  [ -r /etc/os-release ] || die "/etc/os-release tidak ditemukan"
  # shellcheck disable=SC1091
  . /etc/os-release
  OS_ID="${ID:-}"
  case " ${OS_ID} ${ID_LIKE:-} " in
    *" debian "* | *" ubuntu "*) ;;
    *) die "OS '${OS_ID}' tidak didukung: hanya Debian/Ubuntu" ;;
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
}

detect_arch() {
  case "$(uname -m)" in
    x86_64) ARCH=amd64 ;;
    aarch64) ARCH=arm64 ;;
    *) die "arsitektur tidak didukung: $(uname -m)" ;;
  esac
}

install_docker() {
  if command -v docker >/dev/null 2>&1; then
    log "Docker sudah terpasang, dilewati"
    return
  fi
  [ -n "$CODENAME" ] || die "codename distro untuk repo Docker tidak terdeteksi"
  log "Memasang Docker CE"
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
    log "Caddy sudah terpasang, dilewati"
    return
  fi
  log "Memasang Caddy"
  apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl gpg
  curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' |
    gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' >/etc/apt/sources.list.d/caddy-stable.list
  chmod o+r /usr/share/keyrings/caddy-stable-archive-keyring.gpg /etc/apt/sources.list.d/caddy-stable.list
  apt-get update -qq
  apt-get install -y -qq caddy
}

# download_and_install: needs VERSION (may be empty), ARCH, UPDATE; sets VERSION.
download_and_install() {
  if ! command -v curl >/dev/null 2>&1; then
    apt-get update -qq
    apt-get install -y -qq curl
  fi
  if [ -z "$VERSION" ]; then
    # The archive name embeds the version, so resolve "latest" first.
    local final_url
    final_url="$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest")"
    VERSION="${final_url##*/}"
    case "$VERSION" in v[0-9]*) ;; *) die "rilis terbaru ${REPO} tidak dapat ditentukan" ;; esac
  fi
  local ver_num="${VERSION#v}"
  local base="https://github.com/${REPO}/releases/download/${VERSION}"
  local archive="docker-cp_${ver_num}_linux_${ARCH}.tar.gz"

  local tmp
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT

  log "Mengunduh docker-cp ${VERSION} (${ARCH})"
  curl -fsSL "${base}/${archive}" -o "${tmp}/${archive}"
  curl -fsSL "${base}/checksums.txt" -o "${tmp}/checksums.txt"
  local check
  check="$(cd "$tmp" && sha256sum -c --ignore-missing checksums.txt)" || die "verifikasi checksum gagal"
  printf '%s\n' "$check"
  printf '%s\n' "$check" | grep -q "^${archive}: OK$" || die "checksums.txt tidak memuat ${archive}"

  tar -xzf "${tmp}/${archive}" -C "$tmp" docker-cp
  [ -f "${tmp}/docker-cp" ] || die "binary tidak ditemukan di dalam arsip"

  install -m 0755 "${tmp}/docker-cp" "$BIN"
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
}

# read_password: prompts on /dev/tty until >= 8 chars and both entries match; sets PW.
read_password() {
  local pw2
  while :; do
    printf 'Password admin (min. 8 karakter, tidak ditampilkan): ' >/dev/tty
    IFS= read -rs PW </dev/tty
    printf '\n' >/dev/tty
    printf 'Ulangi password: ' >/dev/tty
    IFS= read -rs pw2 </dev/tty
    printf '\n' >/dev/tty
    if [ "${#PW}" -lt 8 ]; then
      echo "Password minimal 8 karakter." >/dev/tty
    elif [ "$PW" != "$pw2" ]; then
      echo "Password tidak cocok, coba lagi." >/dev/tty
    else
      return
    fi
  done
}

configure_ufw() {
  local ans="" port ports
  printf 'Atur firewall ufw (izinkan SSH, 80, 443, 8800)? [y/N]: ' >/dev/tty
  IFS= read -r ans </dev/tty || true
  case "$ans" in
    y | Y | yes | YES) ;;
    *) log "Melewati ufw"; return ;;
  esac
  command -v ufw >/dev/null 2>&1 || apt-get install -y -qq ufw
  ports="$( (sshd -T 2>/dev/null || true) | awk '/^port /{print $2}' | sort -un)"
  [ -n "$ports" ] || ports=22
  for port in $ports; do
    log "Mengizinkan SSH di port ${port}"
    ufw allow "${port}/tcp"
  done
  ufw allow 80/tcp
  ufw allow 443/tcp
  ufw allow 8800/tcp
  ufw --force enable
}

main() {
  local UPDATE=0 VERSION="" PANEL_DOMAIN="" ACME_EMAIL="" ans=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --update) UPDATE=1; shift ;;
      --version)
        [ $# -ge 2 ] || die "--version membutuhkan nilai (mis. v1.2.3)"
        VERSION="$2"; shift 2 ;;
      *) die "argumen tidak dikenal: $1" ;;
    esac
  done

  [ "$(id -u)" -eq 0 ] || die "jalankan sebagai root (gunakan sudo)"
  detect_os
  detect_arch
  export DEBIAN_FRONTEND=noninteractive

  if [ "$UPDATE" -eq 1 ]; then
    { [ -x "$BIN" ] && [ -f "$UNIT" ]; } ||
      die "docker-cp belum terpasang; jalankan tanpa --update"
  else
    tty_ok || die "terminal (/dev/tty) tidak tersedia untuk input; jalankan dari shell interaktif"
    if [ -e "$BIN" ] || [ -e "$STATE" ]; then
      printf 'docker-cp sudah terpasang. Jalankan setup ulang dan timpa password admin? [y/N]: ' >/dev/tty
      IFS= read -r ans </dev/tty || true
      case "$ans" in
        y | Y | yes | YES) ;;
        *) log "Konfigurasi lama dipertahankan (diperlakukan sebagai --update)"; UPDATE=1 ;;
      esac
    fi
  fi

  if [ "$UPDATE" -eq 0 ]; then
    apt-get update -qq
    install_docker
    install_caddy
  fi

  download_and_install

  if [ "$UPDATE" -eq 0 ]; then
    local PW=""
    read_password
    printf 'Domain panel (opsional, mis. panel.example.com): ' >/dev/tty
    IFS= read -r PANEL_DOMAIN </dev/tty || true
    printf 'Email ACME (opsional): ' >/dev/tty
    IFS= read -r ACME_EMAIL </dev/tty || true

    log "Membuat akun admin"
    printf '%s\n' "$PW" | "$BIN" setup --username admin --panel-domain "$PANEL_DOMAIN" --acme-email "$ACME_EMAIL"
    PW=""
    configure_ufw
    systemctl enable --now caddy docker docker-cp
  else
    systemctl restart docker-cp
  fi

  local ip
  ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
  echo
  log "docker-cp ${VERSION} berjalan"
  echo "    URL:    http://${ip:-<ip-server>}:8800"
  if [ -n "$PANEL_DOMAIN" ]; then
    echo "    Domain: https://${PANEL_DOMAIN}"
  fi
}

main "$@"
