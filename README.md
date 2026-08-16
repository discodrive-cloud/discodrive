<p align="center">
  <img src="docs/img/logo.jpg" alt="DiscoDrive" width="100%">
</p>

# DiscoDrive

[**English**](README.md) · [Deutsch](docs/README.de.md) · [Українська](docs/README.uk.md) · [Français](docs/README.fr.md) · [Español](docs/README.es.md) · [Русский](docs/README.ru.md) · [Српски](docs/README.sr.md)

**Your own cloud. One server, one login — files, calendars, contacts, tasks, music and books.**

DiscoDrive is a self-hosted replacement for the bundle of subscriptions and disconnected tools you normally glue together to move off iCloud, Google or Dropbox. Instead of running File Browser, Radicale, a Subsonic server *and* Calibre-web — each with its own quirks and its own login — you run one thing. Everything below lives behind a single account, on your hardware, under your control.

Your files are stored as ordinary files in ordinary folders on disk. If you ever need to move off DiscoDrive to another storage solution, you just open the folder and find your data exactly where you left it. No proprietary chunk store, no database you can't read, no vendor lock-in.

Cross-platform by design: Apple, Windows, Linux and Android.

**Get the apps:** desktop (macOS/Windows/Linux) and Android builds are on the [releases page](https://github.com/discodrive-cloud/discodrive-apps/releases/latest); the iOS app is built from source. See [discodrive-apps](https://github.com/discodrive-cloud/discodrive-apps) for all clients.

---

## What's inside

### 📁 Files & sync

- A fast, modern **web file manager** — upload, download, create folders, rename, move, with drag-and-drop of files *and* folders.
- **One-click previews** — images, PDFs, Markdown, syntax-highlighted code, Word documents and Excel spreadsheets open right in the browser, nothing to download.
- **Versioning** — every file keeps its recent history, so you can roll back a bad edit.
- **Trash** — deleted files go to a recycle bin and can be restored.
- **Sharing** — public links: expiring, password-protected, read-only.
- **Mount it like a drive** — open your storage straight from Finder, Windows Explorer, the Linux file manager or an Android file app over WebDAV. Nothing to install.
- **Desktop sync** — a lightweight app keeps a real folder on your computer in sync with the server, Dropbox-style.
- **Never lose data** — if the same file changes in two places at once, the server keeps both as conflict copies instead of silently overwriting one.

### 🔒 Encrypted vault

- A **private, end-to-end (E2E) encrypted folder** for your most sensitive files.
- Files are encrypted **on your device** before they are sent — the server only ever stores them encrypted.
- Opens right in the browser or in a client app (decryption happens locally); if you forget your password, a recovery code unlocks it.
- Built on an **open, [Cryptomator](https://cryptomator.org)-compatible format** — if you ever need to open the vault separately or move to another service, it can be decrypted with Cryptomator or other independent tools.

### 📅 Calendar, contacts & tasks

- Full **calendars, address books and reminders** — events, contacts and to-dos in one place.
- **Native on Apple** — add the account in iOS/macOS system settings and the built-in Calendar, Contacts and Reminders apps just work, no extra app needed.
- **Everywhere else** — works with DAVx⁵ on Android and Thunderbird, Evolution or eM Client on the desktop.
- A handy **web interface** for creating and editing events, contacts and tasks.
- **Share a calendar or address book** with the people close to you.

### 🎵 Music — your own streaming service

- Turns your music collection into a personal streaming service, available from anywhere.
- **Works with the apps you already use** — any OpenSubsonic-compatible player (Amperfy, Feishin and many more) connects out of the box, with lyrics, queue and search.
- **Internet radio** — add your favourite stations and listen to them through the same apps.
- **Podcasts** — subscribe to feeds, download and play episodes, and pick up where you left off with bookmarks.
- **Built-in tag editor** — edit titles, artists, albums, genres and cover art straight from the web: one track at a time or a whole folder at once.

### ▶️ Player — music and video right in the browser

- Click a track or a movie in your files — and it plays instantly, no downloads, no third-party software.
- **Stays out of your way** — the player lives in a neat bar at the bottom of the screen: the music keeps playing while you keep sorting your files.
- **Video theater** — for video, the interface expands into a comfortable full-screen viewing mode.
- **Floating window** — pop the player out into its own small window on top of other apps (picture-in-picture); it even survives a page reload.
- **10-band equalizer** with ready-made presets — tune the sound to your headphones or speakers.

### 📚 Books — your own library

- A personal library for your e-books and comics, ready to read on any device.
- **Reads on any device** — a standards-based OPDS catalog that KOReader, PocketBook, Marvin and other readers connect to directly (and any device via the browser).
- **Every format that matters** — EPUB, FB2, PDF, MOBI, CBZ and CBR, with cover thumbnails and search across the whole catalog.
- **Reading progress sync** — start a book on one device and continue exactly where you stopped on another.
- **Metadata editor** — edit titles, authors, series, tags and descriptions from the web: per book or in bulk across a folder.

### 🔖 Saved — bookmarks, articles and downloads

A personal corner for everything you've found on the web and don't want to lose.

- **Bookmarks** — your link collection lives in your cloud and syncs through the browser extension, instead of being locked into one browser.
- **Read later** — save whole articles: the server keeps a clean text version without ads and banners, and you read it whenever you find the time.
- **Server-side downloads** — hand the server a link and it fetches the file by itself, straight into your cloud — even while your computer is off.

### 🛡️ Accounts & security

- **Two-factor authentication** with any authenticator app, plus single-use backup codes.
- **Passkeys** — log in with Face ID, Touch ID or a hardware key, with no password at all.
- **Security activity log**.
- **Email notifications** for important security and account events.
- **Brute-force protection** out of the box.
- **Multi-user** with per-user storage quotas.
- **Per-app passwords** for connecting file, music and book clients without exposing your main login.
- **TLS everywhere** by default.

### 🌍 Yours, everywhere, in your language

- Compatible with the Apple, Windows, Linux and Android operating systems.
- The interface is currently available in **7 languages**: English, German, Ukrainian, French, Spanish, Russian and Serbian.

---

## Installation

DiscoDrive is a single server plus PostgreSQL. You can put nginx in front of it for TLS and fast file delivery via X-Accel. Two ways to set everything up are described below. **Option 1** is right for most people.

> **Before you start, generate two secret keys.** The server will refuse to start with the placeholder values from `.env.example`. Generate them once and never change them:
>
> - `JWT_SECRET` — `openssl rand -base64 48`;
> - `SETTINGS_ENCRYPTION_KEY` — `openssl rand -hex 16`.
>
> Keep in mind: Apple apps (CalDAV/CardDAV) require **HTTPS** — they will not work over plain HTTP.

### Option 1. Docker, cloning the repository (recommended)

The simplest path: a single `docker compose up` brings up the server, database and nginx with TLS.

**You need:** git, Docker and Docker Compose.

```sh
git clone https://github.com/discodrive-cloud/discodrive.git
cd discodrive
cp .env.example .env
# edit .env: set JWT_SECRET, SETTINGS_ENCRYPTION_KEY, BASE_DOMAIN,
# POSTGRES_PASSWORD and anything else you need
docker compose up -d
```

Open **https://server_address:8443** (or http://server_address:8080) — on first launch you are taken straight to creating an administrator. Enter an email and a password, and you are ready to go. Note that if you plan to enable outbound email from the service, use real email addresses for all user accounts (including the administrator's), otherwise messages will not be deliverable.

How it works:

- Files are stored in `/data`.
- TLS certificates go in `deploy/nginx/certs/` — you need to place a **real or self-signed certificate** there (see Option 2 for how to generate one) and update the nginx config to redirect port 80 to HTTPS.

To stop: `docker compose down` (data is preserved). To update after `git pull`: `docker compose up -d --build`.

### Option 2. Separate components (build from source)

If you prefer not to use Docker — build the binary yourself and run it alongside a standard PostgreSQL instance. The binary is self-contained: all web assets and database migrations are embedded inside it.

**Required to build:** Go 1.25+, Node.js 22+, PostgreSQL 16+.

**1. Build the web interface** (Nuxt → static files in `web/dist`):

```sh
cd web
npm install
npm run generate
cd ..
```

**2. Build the server** (the binary embeds `web/dist` and migrations):

```sh
CGO_ENABLED=0 go build -trimpath -o discodrive ./cmd/server
```

**3. Start PostgreSQL** and create a database and user:

```sh
createuser disco --pwprompt
createdb discodrive --owner disco
```

**4. Set environment variables** (full list in `.env.example`):

```sh
export DATABASE_URL="postgres://disco:PASSWORD@localhost:5432/discodrive?sslmode=disable"
export JWT_SECRET="$(openssl rand -base64 48)"
export SETTINGS_ENCRYPTION_KEY="$(openssl rand -hex 16)"
export BASE_DOMAIN="example.com"
export STORAGE_ROOT="/var/lib/discodrive/data"   # directory must exist and be writable
export APP_PORT="8080"
export XACCEL_ENABLED="false"                     # without nginx the server serves files itself
```

**5. Run it**:

```sh
./discodrive
```

Open `http://server_address:8080` and create an administrator.

**nginx (optional).** Needed for HTTPS and fast file delivery via X-Accel. Use `deploy/nginx/default.conf` as a starting point, configure your TLS certificates and set `XACCEL_ENABLED=true` — the server will then send an `X-Accel-Redirect` header and nginx will serve the file bodies directly (location `/data/`, matching the value of `STORAGE_ROOT`).

For TLS certificates, use Let's Encrypt (`certbot`) or generate self-signed ones:

```sh
openssl req -x509 -newkey rsa:2048 -nodes -days 825 \
  -keyout deploy/nginx/certs/dev-key.pem \
  -out deploy/nginx/certs/dev.pem -subj "/CN=localhost"
```

**Auto-start with systemd (optional).** Example unit at `/etc/systemd/system/discodrive.service`:

```ini
[Unit]
Description=DiscoDrive
After=network.target postgresql.service

[Service]
ExecStart=/usr/local/bin/discodrive
EnvironmentFile=/etc/discodrive.env
Restart=on-failure
User=discodrive

[Install]
WantedBy=multi-user.target
```

Put the variables from step 4 into `/etc/discodrive.env` (one `KEY=value` per line), then run:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now discodrive
```

---

## License & commercial use

DiscoDrive is **source-available** under the [PolyForm Noncommercial License 1.0.0](LICENSE).

- ✅ **Free for any non-commercial use** — self-host it for yourself, your family, hobby, study or experiments. That's the whole point.
- ✅ **Modify it however you like** — as long as you keep the required attribution notice.
- ❌ **Commercial use is not allowed.**

Need commercial use? A separate commercial license is available — write to [info@discodrive.app](mailto:info@discodrive.app).

---

## ❤️ Support the project

DiscoDrive is developed independently, with no outside investment. Feedback and suggestions are welcome — write to [info@discodrive.app](mailto:info@discodrive.app).

And if you enjoy DiscoDrive and want to see it keep growing — support the project:

- **[Star it on GitHub](https://github.com/discodrive-cloud/discodrive)** ⭐
- **[Ko-fi](https://ko-fi.com/kosmosoid)**
- **[Boosty](https://boosty.to/kosmosoid/donate)**
- **[Cryptocurrency](https://nowpayments.io/donation?api_key=7P893T0-48B4KZH-Q6PXBBT-D79X9TK)** — via NOWPayments

Thank you! Every contribution helps the project move forward.
