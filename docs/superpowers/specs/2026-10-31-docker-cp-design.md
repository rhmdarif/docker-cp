# docker-cp — Desain Control Panel Docker + Caddy + Terminal

Tanggal: 2026-10-31
Status: draf, menunggu persetujuan

## 1. Maksud dan alasannya

Control panel web untuk **satu VPS kecil (RAM 1–2 GB)** yang dipakai **satu admin** untuk:

1. Mengelola Docker lewat UI: container, stack compose, image, volume, network.
2. Mengelola Docker (dan server) lewat CLI: terminal di browser.
3. Mendaftarkan domain ke container/port lewat Caddy sebagai reverse proxy dengan HTTPS otomatis.

Kasus utamanya: menjalankan **beberapa container dari image yang sama di port berbeda** (misalnya satu per klien), lalu memasang domain yang berbeda ke tiap container.

Alasan membuat panel sendiri: panel yang ada terlalu berat atau terlalu banyak fitur untuk VPS kecil. **Syarat nomor satu adalah ringan.** Hampir semua RAM harus tetap tersedia untuk container milik user.

## 2. Tujuan, non-tujuan, anti-tujuan

### Tujuan
- Berjalan nyaman di VPS 1 GB RAM bersama Docker, Caddy, dan beberapa container.
- UI per jenis resource (gaya "A", mirip Portainer): Dashboard, Containers, Stacks, Images, Volumes, Networks, Sites, Terminal.
- Membuat container dari image lewat form **dan** dari `docker-compose.yml`.
- Duplikat container dengan cepat ke port berikutnya.
- Sites: reverse proxy ke container/port, plus direktif Caddyfile custom per site.
- Instalasi pada VPS kosong dengan satu skrip.

### Non-tujuan (tidak dibuat di versi ini)
- Multi-server atau agent jarak jauh.
- Multi-user, role, atau tim. Hanya satu akun admin.
- Marketplace atau template aplikasi.
- Build/deploy dari Git, CI, atau webhook.
- Grafik histori metrik dan alerting.
- 2FA. Boleh ditambahkan nanti, jadi desain auth jangan sampai menutup kemungkinan itu.
- Backup otomatis.

### Anti-tujuan (dianggap gagal meskipun fitur berfungsi)
- **Panel berat atau tidak bisa jalan di VPS 1 GB.** Ini kegagalan utama menurut user.
- Satu salah ketik di direktif custom sebuah site membuat semua site lain mati.
- Panel crash atau restart lalu container ikut mati. Container harus independen dari proses panel.
- Panel memakai CPU atau RAM saat tidak ada yang membukanya (polling latar belakang yang terus berjalan).

## 3. Batasan dan anggaran resource

| Ukuran | Target | Batas keras |
|---|---|---|
| RAM panel (RSS) saat idle, tanpa browser terbuka | ≤ 20 MB | 50 MB |
| RAM panel dengan 1 tab UI + 1 terminal terbuka | ≤ 40 MB | 80 MB |
| CPU saat idle | ~0% (tanpa polling berkala) | — |
| Ukuran binary | ≤ 25 MB | 40 MB |
| Dependensi runtime di server | Docker Engine + compose plugin, Caddy | tidak ada Node, PHP, Python, atau database server |

- OS target: Debian 12 / Ubuntu 22.04+ (keputusan saya). Arsitektur: amd64 dan arm64.
- Semua aset frontend (termasuk xterm.js) di-*vendor* dan di-*embed* di binary. Tidak memakai CDN.
- Data live (stats, logs) hanya dikirim saat ada klien yang membuka halaman terkait. Begitu klien pergi, stream dihentikan.
- Anggaran RAM di atas **hanya untuk proses panel itu sendiri** *(keputusan saya)*. Proses anak yang berumur pendek (`docker compose` saat deploy) dan shell PTY tidak dihitung, tetapi harus selesai atau ditutup begitu tugasnya selesai.

## 4. Keputusan yang sudah diambil

Sebagian diputuskan oleh user. Yang ditandai *(keputusan saya)* dipilih oleh perancang setelah user menyerahkan pilihan stack ("pilih yang paling ringan").

### 4.1 Stack *(keputusan saya, disetujui user secara umum)*
- **Backend:** Go, satu binary statis (`CGO_ENABLED=0`).
- **Docker:** Docker Engine API lewat `/var/run/docker.sock`, memakai client resmi atau HTTP langsung. Tidak memanggil `docker` CLI untuk operasi container biasa.
- **Compose:** memanggil `docker compose` (plugin CLI) sebagai subprocess, karena mengimplementasi ulang compose tidak sepadan. Plugin ini ikut terpasang bersama Docker CE.
- **Caddy:** dipasang di host (paket resmi), bukan di container. Admin API hanya di `localhost:2019`.
- **Terminal:** xterm.js di browser, WebSocket ke PTY di server.
- **Frontend:** HTML server-rendered dengan sedikit JS (misalnya htmx atau vanilla JS). Tidak memakai framework SPA dan tidak perlu langkah build Node di server.

### 4.2 Akses dan keamanan (diputuskan user: "lewat domain / ip:port dengan login")
- Panel bisa diakses lewat **`IP:port`** dan, kalau diatur, lewat **domain panel** (misalnya `panel.domain.com`) yang di-proxy Caddy dengan HTTPS.
- Akses lewat `IP:port` adalah HTTP biasa. User menerima kompromi ini. UI menampilkan peringatan kecil saat diakses tanpa HTTPS.
- Satu akun admin. Password disimpan sebagai hash (bcrypt atau argon2id).
- Sesi pakai cookie `HttpOnly` dan `SameSite=Strict`, ditambah proteksi CSRF untuk aksi yang mengubah state. Origin WebSocket harus dicek.
- Rate-limit login per IP.
- Password admin diatur saat instalasi dan bisa direset lewat CLI binary (misalnya `docker-cp passwd`).
- Panel berjalan sebagai **root** lewat systemd *(keputusan saya)*, karena terminal host dan akses Docker socket memang setara root. Admin = root server.

### 4.3 Struktur UI: per jenis (user memilih opsi A)
Sidebar: **Dashboard · Containers · Stacks · Images · Volumes · Networks · Sites · Terminal · Pengaturan**.

**Dashboard**
- CPU, RAM, disk, dan uptime host (dari `/proc`, hanya saat halaman dibuka).
- Jumlah container yang running dan stopped, serta jumlah site.

**Containers**
- Daftar **semua** container di host, termasuk yang dibuat di luar panel lewat CLI. Kolom: nama, image, port, status, dan stack asal (kalau ada).
- Aksi per container: start, stop, restart, logs (live, follow + tail N), terminal ke dalam container (`docker exec` dengan TTY, shell `sh` atau `bash`), stats on-demand, update image (pull lalu recreate dengan konfigurasi sama), hapus (opsional beserta volume anonim).
- **+ Container** (form): image:tag, nama, port mapping, env (key/value atau tempel `.env`), volume (named atau bind), restart policy (default `unless-stopped`), network, command override (opsional).
- **Registry privat** *(keputusan saya)*: panel tidak menyimpan kredensial registry. Admin menjalankan `docker login` di terminal, lalu panel membaca `/root/.docker/config.json` untuk header `X-Registry-Auth` saat pull atau update. Compose otomatis memakai file yang sama.
- **Port host yang disarankan:** panel mengusulkan port kosong berikutnya mulai dari 8001, setelah mengecek port yang dipakai container dan port yang sedang listen di host.
- **Port publish default ke `127.0.0.1`** *(keputusan saya)*, sehingga akses dari luar lewat Caddy saja. Ada opsi di form untuk publish ke `0.0.0.0`.
- **Duplikat** *(keputusan saya)*: membuka form + Container yang sudah terisi konfigurasi container sumber, dengan nama baru (`nama-2`) dan port berikutnya. Data tidak ikut disalin:
  - Named volume diberi nama baru, misalnya `vol` menjadi `vol-<namabaru>`.
  - Bind mount ditampilkan untuk diubah user.

**Stacks**
- Daftar stack yang dikelola panel, plus project compose lain yang terdeteksi lewat label `com.docker.compose.project` (read-only, tetap bisa start/stop).
- Buat atau edit stack: editor teks `docker-compose.yml` dan `.env` opsional, lalu tombol Deploy (`docker compose up -d`), Stop, Down, Pull & redeploy, dan Hapus.
- Output perintah compose di-stream ke UI.
- Lokasi file: `/opt/docker-cp/stacks/<nama>/` *(keputusan saya)*.

**Images / Volumes / Networks**
- Lihat, pull image, hapus, dan prune (hapus yang tidak terpakai) dengan konfirmasi di UI. Pakai modal HTML, bukan `confirm()` browser.

**Sites (Caddy)**
- Daftar site: domain (satu atau lebih per site), target, dan status sertifikat jika tersedia.
- **Target:** pilih container dari daftar (panel memakai port host yang di-publish container itu) **atau** isi `host:port` manual.
- **Custom** (diputuskan user): tiap site punya kotak teks untuk direktif Caddyfile tambahan di dalam blok site, misalnya `header`, `basicauth`, `redir`, `encode`, opsi `reverse_proxy`, atau `file_server`. Ada juga mode "blok manual penuh", di mana user menulis seluruh blok site sendiri.
- Batasan mode blok manual *(keputusan saya)*:
  - Isinya harus tepat **satu** blok site, tanpa global options dan tanpa snippet.
  - Alamatnya tidak boleh berupa catch-all (`:443`, `:80`, `http://`, wildcard yang menutupi domain site lain) dan tidak boleh tumpang-tindih dengan domain site lain.
  - Panel mengeceknya lewat endpoint `/adapt` Caddy (hasil JSON) sebelum `/load`, jadi tidak perlu parser Caddyfile sendiri di Go.
- Domain panel sendiri juga dikelola sebagai site khusus yang tidak bisa dihapus dari UI.

**Terminal**
- Shell host (root) di browser. Bisa membuka lebih dari satu tab terminal.
- Sesi berakhir saat tab ditutup. Tidak ada sesi persisten di latar belakang *(keputusan saya)*.

**Pengaturan**
- Ganti password, domain panel, port panel, dan email ACME untuk Caddy.

### 4.4 Cara panel mengelola Caddy (disetujui user)
- **Panel adalah pemilik tunggal konfigurasi Caddy**, karena VPS-nya kosong dan tidak ada Caddyfile lama yang perlu dijaga.
- Alurnya:
  1. Panel menyusun **Caddyfile** utuh dari data site.
  2. Panel mengirimnya ke `POST localhost:2019/load` dengan `Content-Type: text/caddyfile`.
  3. Caddy mengadaptasi dan memvalidasinya.
  4. Kalau ditolak, config lama tetap aktif. Panel menampilkan error dari Caddy dan menandai site yang bermasalah. Perubahan tidak disimpan sebagai "aktif".
- Setelah load berhasil, Caddyfile yang sama ditulis ke `/etc/caddy/Caddyfile` supaya bertahan saat Caddy atau server di-restart.

### 4.5 Penyimpanan
- Konfigurasi panel (admin, settings, site, metadata container dan stack yang dibuat panel) disimpan di `/opt/docker-cp/data/`.
- Format diserahkan ke builder, dengan syarat: tanpa CGO, tanpa server database, aman dari korupsi saat crash (tulis atomik). Pilihannya SQLite pure-Go atau satu file JSON dengan penulisan atomik. Ukuran binary dan RAM ikut menentukan.
- **Sumber kebenaran untuk container adalah Docker itu sendiri.** Panel tidak menyimpan state container yang bisa basi. Panel membaca langsung dari Docker API.

### 4.6 Instalasi
- Satu skrip `install.sh` untuk VPS kosong:
  1. Memasang Docker CE + compose plugin (repo resmi Docker).
  2. Memasang Caddy (repo resmi Caddy).
  3. Menyalin binary panel ke `/usr/local/bin/docker-cp`.
  4. Membuat unit systemd `docker-cp.service`.
  5. Meminta password admin (dan domain panel kalau ada).
  6. Mengaktifkan semuanya.
- **Sumber binary (diputuskan user): GitHub Releases.**
  - Instalasi cukup satu baris: `curl -fsSL <url>/install.sh | bash`.
  - Skrip mendeteksi arsitektur (amd64/arm64), mengunduh binary rilis terbaru (atau versi yang diminta), lalu memverifikasi checksum SHA-256 sebelum memasang.
  - Butuh pipeline rilis (misalnya GitHub Actions + GoReleaser) yang menghasilkan binary amd64/arm64, `checksums.txt`, dan URL berversi.
  - Nama repo/owner GitHub belum ditentukan, jadi skrip memakai variabel yang mudah diganti.
  - Update: menjalankan ulang `install.sh` (atau `docker-cp update`) akan mengunduh rilis terbaru, mengganti binary, lalu me-restart service. Data dan konfigurasi tidak disentuh.
- Port default panel: **8800** *(keputusan saya)*, bisa diubah.
- **Firewall** *(keputusan saya)*: installer tidak mengaktifkan firewall secara otomatis, untuk menghindari risiko SSH terkunci. Installer menawarkan ufw secara opsional (izinkan port SSH yang terdeteksi, 80, 443, dan port panel).
  - UI menampilkan peringatan bahwa port yang di-publish Docker ke `0.0.0.0` melewati aturan ufw.
  - Inilah alasan default publish ke `127.0.0.1`.
- Update panel: ganti binary lalu restart service. Container tidak terpengaruh.

## 5. Yang diserahkan ke builder
- Struktur kode Go, router HTTP, library WebSocket dan PTY, serta library Docker client (pilih yang paling ringan).
- Format penyimpanan (lihat 4.5).
- Detail tampilan visual dan styling, asalkan sederhana, ringan, dan nyaman di desktop. Mobile cukup bisa dipakai.
- Pilihan htmx atau vanilla JS.
- Mekanisme live update (SSE atau WebSocket) untuk logs, stats, dan output compose.
- Strategi pengujian.

## 6. Kriteria sukses
- Di VPS 1 GB yang baru diinstal skrip, panel + Docker + Caddy berjalan dan RSS panel saat idle tetap dalam anggaran (bagian 3).
- Admin bisa:
  1. Menjalankan 3 container dari image yang sama di port 8001–8003 lewat form dan tombol Duplikat.
  2. Memasang 3 domain ke container tersebut lewat Sites dengan HTTPS aktif.
  3. Membuka terminal host dan terminal di dalam container.
  4. Men-deploy satu stack dari `docker-compose.yml`.
- Direktif custom yang salah ditolak dengan pesan jelas, dan site lain tetap berjalan.
- Restart service panel tidak memengaruhi container maupun site yang sedang berjalan.
