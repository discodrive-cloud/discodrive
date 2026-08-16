<p align="center">
  <img src="img/logo.jpg" alt="DiscoDrive" width="100%">
</p>

# DiscoDrive

[English](../README.md) · [**Deutsch**](README.de.md) · [Українська](README.uk.md) · [Français](README.fr.md) · [Español](README.es.md) · [Русский](README.ru.md) · [Српски](README.sr.md)

**Deine eigene Cloud. Ein Server, ein Login — Dateien, Kalender, Kontakte, Aufgaben, Musik und Bücher.**

DiscoDrive ist ein selbstgehosteter Ersatz für das Bündel an Abos und den Zoo an Tools, die man sonst zusammenstückeln muss, um von iCloud, Google oder Dropbox wegzukommen. Statt File Browser, Radicale, einen Subsonic-Server *und* Calibre-web zu betreiben — jedes mit seinen eigenen Tücken und Eigenheiten — betreibst du eine einzige Anwendung. Alles, was unten beschrieben wird, steckt hinter einem einzigen Konto, auf deiner Hardware, unter deiner Kontrolle.

Dateien werden als ganz normale Dateien in ganz normalen Ordnern auf der Festplatte gespeichert. Falls du jemals von DiscoDrive auf einen anderen Speicher umziehen musst, öffnest du einfach den Ordner und findest deine Daten genau dort, wo du sie gelassen hast. Kein proprietärer Chunk-Speicher, keine Datenbank, die man nicht lesen kann, kein Vendor-Lock-in.

Plattformübergreifend von Grund auf: Apple, Windows, Linux und Android.

**Apps herunterladen:** Desktop-Builds (macOS/Windows/Linux) und Android stehen auf der [Releases-Seite](https://github.com/discodrive-cloud/discodrive-apps/releases/latest); die iOS-App wird aus dem Quellcode gebaut. Alle Clients unter [discodrive-apps](https://github.com/discodrive-cloud/discodrive-apps).

---

## Was du bekommst

### 📁 Dateien & Sync

- Ein schneller, moderner **Web-Dateimanager** — Hoch- und Herunterladen, Ordner anlegen, Umbenennen, Verschieben, mit Drag-and-drop von Dateien *und* Ordnern.
- **Vorschau mit einem Klick** — Bilder, PDFs, Markdown, Code mit Syntaxhervorhebung, Word-Dokumente und Excel-Tabellen öffnen sich direkt im Browser, ohne dass du etwas herunterladen musst.
- **Versionierung** — jede Datei behält ihren jüngsten Verlauf, sodass du nach einer missglückten Änderung zur früheren Version zurückkehren kannst.
- **Papierkorb** — gelöschte Dateien landen im Papierkorb und lassen sich wiederherstellen.
- **Teilen** — öffentliche Links: ablaufend, passwortgeschützt, nur-lesen.
- **Als Laufwerk einbinden** — öffne deinen Speicher direkt aus dem Finder, dem Windows-Explorer, dem Linux-Dateimanager oder einer Android-Datei-App über WebDAV. Nichts zu installieren.
- **Desktop-Sync** — eine schlanke App hält einen echten Ordner auf deinem Rechner mit dem Server synchron, ganz wie Dropbox.
- **Keine Daten gehen verloren** — ändert sich dieselbe Datei gleichzeitig an zwei Stellen, speichert der Server beide Versionen als Konfliktkopien.

### 🔒 Verschlüsselter Tresor

- Ein **privater, Ende-zu-Ende (E2E) verschlüsselter Ordner** für deine sensibelsten Dateien.
- Dateien werden **auf deinem Gerät** verschlüsselt, bevor sie gesendet werden — der Server hält sie ausschließlich verschlüsselt.
- Öffnet direkt im Browser oder in der Client-App (die Entschlüsselung passiert lokal); hast du dein Passwort vergessen, entsperrt ein Wiederherstellungscode den Tresor.
- Aufgebaut auf einem **offenen, zu [Cryptomator](https://cryptomator.org) kompatiblen Format** — falls du den Tresor separat öffnen oder zu einem anderen Dienst wechseln möchtest, lässt er sich mit Cryptomator oder anderen unabhängigen Tools entschlüsseln.

### 📅 Kalender, Kontakte & Aufgaben

- Vollwertige **Kalender, Adressbücher und Erinnerungen** — Termine, Kontakte und To-dos an einem Ort.
- **Nativ auf Apple** — füge das Konto in den iOS/macOS-Systemeinstellungen hinzu, und die eingebauten Apps Kalender, Kontakte und Erinnerungen funktionieren einfach, ohne zusätzliche App.
- **Überall sonst** — funktioniert mit DAVx⁵ auf Android sowie Thunderbird, Evolution oder eM Client auf dem Desktop.
- Eine praktische **Web-Oberfläche** zum Erstellen und Bearbeiten von Terminen, Kontakten und Aufgaben.
- **Teile einen Kalender oder ein Adressbuch** mit Menschen, die dir nahestehen.

### 🎵 Musik — dein eigener Streaming-Dienst

- Macht aus deiner Musiksammlung einen persönlichen Streaming-Dienst, von überall erreichbar.
- **Funktioniert mit den Apps, die du schon nutzt** — jeder OpenSubsonic-kompatible Player (Amperfy, Feishin und viele mehr) verbindet sich sofort, mit Songtexten, Warteschlange und Suche.
- **Internetradio** — füge deine Lieblingssender hinzu und höre sie über dieselben Apps.
- **Podcasts** — abonniere Feeds, lade Episoden herunter, spiele sie ab und fahre mit Lesezeichen genau dort fort, wo du aufgehört hast.
- **Eingebauter Tag-Editor** — bearbeite Titel, Interpreten, Alben, Genres und Cover direkt im Browser: einzeln pro Track oder für einen ganzen Ordner auf einmal.

### ▶️ Player — Musik und Video direkt im Browser

- Klicke in deinen Dateien auf einen Track oder Film — und er läuft sofort, ohne Download und ohne Zusatzsoftware.
- **Steht dir nicht im Weg** — der Player lebt in einer dezenten Leiste am unteren Bildschirmrand: die Musik läuft weiter, während du deine Dateien sortierst.
- **Video-Theater** — bei Videos wächst die Oberfläche zu einem komfortablen Vollbild-Wiedergabemodus heran.
- **Schwebendes Fenster** — der Player lässt sich in ein eigenes kleines Fenster über anderen Programmen ausdocken (Bild-in-Bild); es übersteht sogar ein Neuladen der Seite.
- **10-Band-Equalizer** mit fertigen Presets — stimme den Klang auf deine Kopfhörer oder Lautsprecher ab.

### 📚 Bücher — deine eigene Bibliothek

- Eine persönliche Bibliothek für E-Books und Comics, lesefertig auf jedem Gerät.
- **Lesbar auf jedem Gerät** — ein standardbasierter OPDS-Katalog, mit dem sich KOReader, PocketBook, Marvin und andere Reader-Apps direkt verbinden (und über den Browser ohnehin jedes Gerät).
- **Alle wichtigen Formate** — EPUB, FB2, PDF, MOBI, CBZ und CBR, mit Cover-Vorschaubildern und Suche über den gesamten Katalog.
- **Sync des Lesefortschritts** — beginne ein Buch auf einem Gerät und fahre auf einem anderen genau an derselben Stelle fort.
- **Metadaten-Editor** — bearbeite Titel, Autoren, Reihen, Tags und Beschreibungen im Browser: pro Buch oder gesammelt für einen ganzen Ordner.

### 🔖 Gespeichert — Lesezeichen, Artikel und Downloads

Eine persönliche Ecke für alles, was du im Netz gefunden hast und nicht verlieren willst.

- **Lesezeichen** — deine Linksammlung liegt in deiner Cloud und synchronisiert sich über die Browser-Erweiterung, statt in einem einzigen Browser eingesperrt zu sein.
- **Später lesen** — speichere ganze Artikel: der Server bewahrt eine saubere Textfassung ohne Werbung und Banner auf, und du liest sie, wann immer du Zeit hast.
- **Downloads auf dem Server** — gib dem Server einen Link, und er lädt die Datei selbst herunter, direkt in deine Cloud — sogar wenn dein Rechner aus ist.

### 🛡️ Konten & Sicherheit

- **Zwei-Faktor-Authentifizierung** (TOTP) mit jeder Authenticator-App, dazu Einmal-Backup-Codes.
- **Passkeys** — Anmeldung per Face ID, Touch ID oder Hardware-Schlüssel, ganz ohne Passwort.
- **Sicherheitsprotokoll**.
- **E-Mail-Benachrichtigungen** zu wichtigen Sicherheits- und Konto-Ereignissen.
- **Brute-Force-Schutz** von Haus aus.
- **Mehrbenutzer** mit individuellen Speicherkontingenten.
- **App-Passwörter** zum Verbinden von Datei-, Musik- und Buch-Clients, ohne dein Haupt-Passwort preiszugeben.
- **TLS überall** standardmäßig.

### 🌍 Dein, überall, in deiner Sprache

- Kompatibel mit den Betriebssystemen Apple, Windows, Linux und Android.
- Die Oberfläche ist derzeit in **7 Sprachen** verfügbar: Englisch, Deutsch, Ukrainisch, Französisch, Spanisch, Russisch und Serbisch.

---

## Installation

DiscoDrive besteht aus einem einzelnen Server plus PostgreSQL. Davor kann nginx gesetzt werden — für TLS und schnelle Dateiauslieferung über X-Accel. Unten sind zwei Wege beschrieben, alles einzurichten. Für die meisten empfiehlt sich **Variante 1**.

> **Vor dem Start müssen zwei geheime Schlüssel vorbereitet werden.** Der Server startet nicht mit den Platzhalterwerten aus `.env.example`. Generiere sie einmalig und lass sie unverändert:
>
> - `JWT_SECRET` — `openssl rand -base64 48`;
> - `SETTINGS_ENCRYPTION_KEY` — `openssl rand -hex 16`.
>
> Außerdem gilt: Apple-Apps (CalDAV/CardDAV) benötigen zwingend **HTTPS** — über unverschlüsseltes HTTP funktionieren sie nicht.

### Variante 1. Docker mit Repository-Klon (empfohlen)

Der einfachste Weg: ein einziges `docker compose up` startet Server, Datenbank und nginx mit TLS.

**Benötigt:** git, Docker und Docker Compose.

```sh
git clone https://github.com/discodrive-cloud/discodrive.git
cd discodrive
cp .env.example .env
# .env bearbeiten: JWT_SECRET, SETTINGS_ENCRYPTION_KEY, BASE_DOMAIN,
# POSTGRES_PASSWORD setzen und bei Bedarf den Rest anpassen
docker compose up -d
```

Öffne **https://server_address:8443** (oder http://server_address:8080) — beim ersten Start erscheint das Formular zum Anlegen des Administrators. Gib E-Mail und Passwort ein, und es kann losgehen. Beachte: Falls du den E-Mail-Versand des Dienstes nutzen möchtest, müssen die Benutzernamen (einschließlich des Administrators) gültige E-Mail-Adressen sein, da Nachrichten sonst nicht zugestellt werden können.

So ist es aufgebaut:

- Dateien werden in `/data` gespeichert.
- TLS-Zertifikate liegen in `deploy/nginx/certs/` — du musst ein **echtes oder selbstsigniertes Zertifikat** dort ablegen (wie man eines generiert, ist in Variante 2 beschrieben) und in der nginx-Konfiguration Port 80 auf eine HTTPS-Weiterleitung umstellen.

Zum Stoppen: `docker compose down` (Daten bleiben erhalten). Nach einem `git pull` aktualisieren: `docker compose up -d --build`.

### Variante 2. Einzelne Komponenten (aus dem Quellcode bauen)

Wer Docker nicht verwenden möchte, baut die ausführbare Datei selbst und betreibt sie neben einem regulären PostgreSQL. Die ausführbare Datei ist vollständig eigenständig — alle Komponenten sind darin enthalten.

**Zum Bauen benötigt:** Go 1.25+, Node.js 22+, PostgreSQL 16+.

**1. Web-Oberfläche bauen** (Nuxt → statische Dateien in `web/dist`):

```sh
cd web
npm install
npm run generate
cd ..
```

**2. Server bauen** (die ausführbare Datei bindet `web/dist` und die Migrationen ein):

```sh
CGO_ENABLED=0 go build -trimpath -o discodrive ./cmd/server
```

**3. PostgreSQL aufsetzen** und Datenbank mit Benutzer anlegen:

```sh
createuser disco --pwprompt
createdb discodrive --owner disco
```

**4. Umgebungsvariablen setzen** (vollständige Liste in `.env.example`):

```sh
export DATABASE_URL="postgres://disco:PASSWORT@localhost:5432/discodrive?sslmode=disable"
export JWT_SECRET="$(openssl rand -base64 48)"
export SETTINGS_ENCRYPTION_KEY="$(openssl rand -hex 16)"
export BASE_DOMAIN="example.com"
export STORAGE_ROOT="/var/lib/discodrive/data"   # Verzeichnis muss existieren und beschreibbar sein
export APP_PORT="8080"
export XACCEL_ENABLED="false"                     # ohne nginx liefert der Server Dateien selbst aus
```

**5. Starten**:

```sh
./discodrive
```

Öffne `http://server_address:8080` und lege den Administrator an.

**nginx (optional).** Wird für HTTPS und schnelle Dateiauslieferung über X-Accel benötigt. Nimm `deploy/nginx/default.conf` als Ausgangspunkt, trage deine TLS-Zertifikate ein und setze `XACCEL_ENABLED=true` — dann antwortet der Server mit dem Header `X-Accel-Redirect`, und nginx liefert die Dateiinhalte selbst aus (Pfad `/data/`, entspricht `STORAGE_ROOT`).

TLS-Zertifikate erhältst du bei Let's Encrypt (`certbot`), oder du generierst selbstsignierte:

```sh
openssl req -x509 -newkey rsa:2048 -nodes -days 825 \
  -keyout deploy/nginx/certs/dev-key.pem \
  -out deploy/nginx/certs/dev.pem -subj "/CN=localhost"
```

**Autostart via systemd (optional).** Beispiel-Unit `/etc/systemd/system/discodrive.service`:

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

Lege die Variablen aus Schritt 4 in `/etc/discodrive.env` ab (Format `KEY=value`, eine pro Zeile), dann führe aus:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now discodrive
```

---

## Lizenz & kommerzielle Nutzung

DiscoDrive ist **source-available** unter der [PolyForm Noncommercial License 1.0.0](../LICENSE).

- ✅ **Kostenlos für jede nicht-kommerzielle Nutzung** — hoste es für dich selbst, deine Familie, als Hobby, zum Lernen oder für Experimente. Genau dafür ist es gemacht.
- ✅ **Ändern, wie du willst** — solange du den vorgeschriebenen Urheberrechtshinweis beibehältst.
- ❌ **Kommerzielle Nutzung ist nicht erlaubt.**

Du brauchst kommerzielle Nutzung? Eine separate kommerzielle Lizenz ist verfügbar — schreib an [info@discodrive.app](mailto:info@discodrive.app).

---

## ❤️ Das Projekt unterstützen

DiscoDrive wird aus eigener Kraft entwickelt, ohne Investitionen. Rückmeldungen und Vorschläge sind willkommen — schreib an [info@discodrive.app](mailto:info@discodrive.app).

Und wenn dir DiscoDrive gefällt und du möchtest, dass es weiter wächst — unterstütze das Projekt:

- **[Gib ihm einen Stern auf GitHub](https://github.com/discodrive-cloud/discodrive)** ⭐
- **[Ko-fi](https://ko-fi.com/kosmosoid)**
- **[Boosty](https://boosty.to/kosmosoid/donate)**
- **[Kryptowährung](https://nowpayments.io/donation?api_key=7P893T0-48B4KZH-Q6PXBBT-D79X9TK)** — über NOWPayments

Danke! Jeder Beitrag bringt das Projekt voran.
