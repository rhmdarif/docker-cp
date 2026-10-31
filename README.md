# docker-cp

Panel kontrol ringan (satu binary Go) untuk mengelola Docker, Caddy, dan terminal web
di VPS kecil (1-2 GB RAM). Target OS: Debian dan Ubuntu (amd64 / arm64).

## Instal

Di VPS Debian/Ubuntu yang masih kosong, jalankan satu baris:

```bash
curl -fsSL https://github.com/OWNER/docker-cp/releases/latest/download/install.sh | sudo bash
```

Installer akan:

1. memasang Docker CE (+ compose plugin) dan Caddy dari repo apt resmi, bila belum ada;
2. mengunduh rilis terbaru, memverifikasi sha256 terhadap `checksums.txt`, lalu memasang
   `/usr/local/bin/docker-cp` dan unit systemd `docker-cp.service`;
3. menanyakan password admin (dibaca dari terminal, tidak ditampilkan), domain panel, dan
   email ACME (keduanya opsional);
4. menawarkan konfigurasi ufw (default: tidak);
5. menjalankan layanan. Panel dapat dibuka di `http://<IP-server>:8800`.

Versi tertentu: `... | sudo bash -s -- --version v1.2.3`

Data tersimpan di `/opt/docker-cp/data`, stack compose di `/opt/docker-cp/stacks`.

## Update

```bash
curl -fsSL https://github.com/OWNER/docker-cp/releases/latest/download/install.sh | sudo bash -s -- --update
```

Mode `--update` hanya mengganti binary dan unit, lalu me-restart `docker-cp`; data dan
password tidak disentuh.

## Reset password

```bash
sudo docker-cp passwd
```

Password baru dibaca dari baris pertama stdin, contoh: `printf '%s\n' 'passwordbaru' | sudo docker-cp passwd`.

## Catatan

- **docker login**: login ke registry privat dilakukan lewat terminal (`docker login`),
  bukan lewat panel. Kredensial tersimpan di konfigurasi Docker dan dipakai saat pull image.
- **Port dan firewall**: port yang dipublikasikan container Docker (`-p 8080:80`, binding
  `0.0.0.0`) melewati aturan ufw karena Docker menulis aturan iptables sendiri. Ufw tidak
  akan memblokirnya. Bind ke `127.0.0.1` (`-p 127.0.0.1:8080:80`) dan akses lewat Caddy
  (menu Sites) bila port tidak boleh terbuka ke publik.
- Port panel `8800` sebaiknya diakses lewat domain panel (HTTPS via Caddy) bila memakai
  internet publik.

## Penggunaan resource

Diukur lokal (linux/amd64, build snapshot GoReleaser, Docker dan Caddy tidak berjalan):

| Kondisi | RSS |
|---|---|
| Idle, 10 detik setelah start | 9,8 MB |
| Setelah 30 request (login, dashboard, fragmen sysinfo, daftar container) | 12,4 MB (puncak 12,7 MB) |
| CPU setelah 60 detik idle | ~0% (0,2% rata-rata sejak start) |

Ukuran binary: amd64 10,9 MB, arm64 10,2 MB. Unit systemd membatasi `GOMEMLIMIT=40MiB`
dan `GOGC=50`. Sesi terminal web belum diukur dalam pengujian ini.

## Build dari source

Butuh Go (versi sesuai `go.mod`).

```bash
go test ./...
go build -ldflags "-s -w -X main.version=dev" -o docker-cp ./cmd/docker-cp
./docker-cp serve   # env: DCP_LISTEN, DCP_DATA_DIR, DCP_STACKS_DIR, DCP_CADDY_ADMIN, DCP_CADDYFILE, DCP_DOCKER_SOCK, DCP_DOCKER_CONFIG
```

Build rilis lokal: `goreleaser build --snapshot --clean`. Rilis dibuat otomatis oleh
GitHub Actions saat tag `v*` di-push.
