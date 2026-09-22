<p align="center">
  <img src="img/logo.jpg" alt="DiscoDrive" width="100%">
</p>

# DiscoDrive

[English](../README.md) · [Deutsch](README.de.md) · [Українська](README.uk.md) · [**Français**](README.fr.md) · [Español](README.es.md) · [Русский](README.ru.md) · [Српски](README.sr.md)

**Votre propre cloud. Un serveur, un seul identifiant — fichiers, agendas, contacts, tâches, musique et livres.**

DiscoDrive est un remplaçant auto-hébergé du paquet d'abonnements et du zoo d'outils qu'on assemble d'habitude pour quitter iCloud, Google ou Dropbox. Au lieu de faire tourner File Browser, Radicale, un serveur Subsonic *et* Calibre-web — chacun avec ses propres difficultés et particularités — vous ne faites tourner qu'une seule application. Tout ce qui suit vit derrière un seul compte, sur votre matériel, sous votre contrôle.

Vos fichiers sont stockés comme des fichiers ordinaires, dans des dossiers ordinaires sur le disque. Si un jour vous devez quitter DiscoDrive pour un autre stockage, vous ouvrez simplement le dossier et retrouvez vos données exactement là où vous les aviez laissées. Pas de stockage propriétaire par fragments, pas de base de données illisible, pas de vendor lock-in.

Multiplateforme par conception : Apple, Windows, Linux et Android.

**Télécharger les applications :** les versions de bureau (macOS/Windows/Linux) et Android sont sur la [page des releases](https://github.com/discodrive-cloud/discodrive-apps/releases/latest) ; l'application iOS se compile depuis les sources. Tous les clients sur [discodrive-apps](https://github.com/discodrive-cloud/discodrive-apps).

---

## Ce que vous obtenez

### 📁 Fichiers & synchronisation

- Un **gestionnaire de fichiers web** rapide et moderne — envoi, téléchargement, création de dossiers, renommage, déplacement, avec glisser-déposer de fichiers *et* de dossiers.
- **Aperçu en un clic** — images, PDF, Markdown, code avec coloration syntaxique, documents Word et feuilles Excel s'ouvrent directement dans le navigateur, rien à télécharger.
- **Versions** — chaque fichier conserve son historique récent, pour revenir à une version antérieure après une modification malheureuse.
- **Corbeille** — les fichiers supprimés vont à la corbeille et peuvent être restaurés.
- **Partage** — liens publics : avec expiration, protégés par mot de passe, en lecture seule.
- **Montage comme lecteur réseau** — ouvrez votre stockage directement depuis le Finder, l'Explorateur de fichiers Windows, le gestionnaire de fichiers Linux ou une application de fichiers Android via WebDAV. Rien à installer.
- **Synchronisation sur le bureau** — une application légère garde un vrai dossier de votre ordinateur synchronisé avec le serveur, comme Dropbox.
- **Aucune donnée perdue** — si le même fichier change à deux endroits en même temps, le serveur conserve les deux versions comme copies en conflit plutôt que d'en écraser une en silence.

### 🔒 Coffre chiffré

- Un **dossier privé chiffré de bout en bout (E2E)** pour vos fichiers les plus sensibles.
- Les fichiers sont chiffrés **sur votre appareil** avant d'être envoyés — le serveur ne les conserve que sous forme chiffrée.
- S'ouvre directement dans le navigateur ou dans l'application cliente (le déchiffrement se fait localement) ; si vous oubliez votre mot de passe, un code de récupération déverrouille le coffre.
- Bâti sur un **format ouvert, compatible [Cryptomator](https://cryptomator.org)** — si vous devez un jour ouvrir le coffre séparément ou migrer vers un autre service, il peut être déchiffré avec Cryptomator ou d'autres outils tiers.

### 📅 Agenda, contacts & tâches

- Des **agendas, carnets d'adresses et rappels** complets — événements, contacts et tâches au même endroit.
- **Natif sur Apple** — ajoutez le compte dans les réglages système iOS/macOS, et les applications Calendrier, Contacts et Rappels fonctionnent nativement, sans application supplémentaire.
- **Partout ailleurs** — fonctionne avec DAVx⁵ sur Android et avec Thunderbird, Evolution ou eM Client sur le bureau.
- Une **interface web** pratique pour créer et modifier événements, contacts et tâches.
- **Partagez un agenda ou un carnet d'adresses** avec vos proches.

### 🎵 Musique — votre propre service de streaming

- Transforme votre collection musicale en un service de streaming personnel, accessible de partout.
- **Fonctionne avec les applications que vous utilisez déjà** — n'importe quel lecteur compatible OpenSubsonic (Amperfy, Feishin et bien d'autres) se connecte d'emblée, avec paroles, file d'attente et recherche.
- **Radio internet** — ajoutez vos stations préférées et écoutez-les via les mêmes applications.
- **Podcasts** — abonnez-vous aux flux, téléchargez et lisez les épisodes, et reprenez là où vous vous étiez arrêté grâce aux signets.
- **Éditeur de tags intégré** — modifiez titres, artistes, albums, genres et pochettes directement dans le navigateur : piste par piste ou tout un dossier d'un coup.

### ▶️ Lecteur — musique et vidéo directement dans le navigateur

- Cliquez sur un morceau ou un film dans vos fichiers — il se lance aussitôt, sans téléchargement ni logiciel tiers.
- **Discret** — le lecteur vit dans une barre sobre en bas de l'écran : la musique continue pendant que vous triez vos fichiers.
- **Théâtre vidéo** — pour la vidéo, l'interface se déploie en un confortable mode plein écran.
- **Fenêtre flottante** — détachez le lecteur dans une petite fenêtre au-dessus des autres applications (picture-in-picture) ; elle survit même au rechargement de la page.
- **Égaliseur 10 bandes** avec des préréglages prêts à l'emploi — ajustez le son à votre casque ou à vos enceintes.

### 📚 Livres — votre propre bibliothèque

- Une bibliothèque personnelle pour vos livres électroniques et vos bandes dessinées, prête à lire sur n'importe quel appareil.
- **Lisible sur tout appareil** — un catalogue au standard OPDS auquel KOReader, PocketBook, Marvin et d'autres liseuses se connectent directement (et n'importe quel appareil via le navigateur).
- **Tous les formats qui comptent** — EPUB, FB2, PDF, MOBI, CBZ et CBR, avec miniatures de couverture et recherche sur tout le catalogue.
- **Synchronisation de la progression de lecture** — commencez un livre sur un appareil et continuez exactement au même endroit sur un autre.
- **Éditeur de métadonnées** — modifiez titres, auteurs, séries, tags et descriptions dans le navigateur : livre par livre ou en masse sur un dossier.

### 🔖 Enregistrés — signets, articles et téléchargements

Un coin personnel pour tout ce que vous avez trouvé sur le web et ne voulez pas perdre.

- **Signets** — votre collection de liens vit dans votre cloud et se synchronise via l'extension de navigateur, au lieu d'être enfermée dans un seul navigateur.
- **Lire plus tard** — enregistrez des articles entiers : le serveur en garde une version texte propre, sans publicités ni bannières, à lire quand vous avez le temps.
- **Téléchargements côté serveur** — donnez un lien au serveur et il récupère le fichier tout seul, directement dans votre cloud — même quand votre ordinateur est éteint.

### 🛡️ Comptes & sécurité

- **Authentification à deux facteurs** TOTP via n'importe quelle application d'authentification, plus des codes de secours à usage unique.
- **Passkeys** — connexion par Face ID, Touch ID ou clé matérielle, sans aucun mot de passe.
- **Journal de sécurité**.
- **Notifications par e-mail** pour les événements importants de sécurité et de compte.
- **Protection intégrée contre les attaques par force brute**.
- **Multi-utilisateur** avec des quotas de stockage personnalisés par utilisateur.
- **Mots de passe par application** — pour connecter les clients de fichiers, de musique et de livres sans exposer votre identifiant principal.
- **TLS partout** par défaut.

### 🌍 À vous, partout, dans votre langue

- Compatible avec les systèmes d'exploitation Apple, Windows, Linux et Android.
- L'interface est actuellement disponible en **7 langues** : anglais, allemand, ukrainien, français, espagnol, russe et serbe.

---

## Installation

DiscoDrive, c'est un seul serveur plus PostgreSQL. Vous pouvez placer nginx devant lui pour gérer le TLS et la distribution rapide des fichiers via X-Accel. Voici deux façons de tout déployer. La plupart des utilisateurs choisiront l'**Option 1**.

> **Avant de démarrer, vous devez préparer deux clés secrètes.** Le serveur refusera de démarrer avec les valeurs temporaires du fichier `.env.example`. Générez-les une bonne fois et ne les modifiez plus :
>
> - `JWT_SECRET` — `openssl rand -base64 48` ;
> - `SETTINGS_ENCRYPTION_KEY` — `openssl rand -hex 16`.
>
> Gardez à l'esprit que les applications Apple (CalDAV/CardDAV) exigent **HTTPS** — elles ne fonctionnent pas en HTTP non chiffré.

### Option 1. Docker, avec clonage du dépôt (recommandé)

La méthode la plus simple : une seule commande `docker compose up` lance le serveur, la base de données et nginx avec TLS.

**Prérequis :** git, Docker et Docker Compose.

```sh
git clone https://github.com/discodrive-cloud/discodrive.git
cd discodrive
cp .env.example .env
# modifiez .env : renseignez JWT_SECRET, SETTINGS_ENCRYPTION_KEY, BASE_DOMAIN,
# POSTGRES_PASSWORD et, si vous le souhaitez, le reste
docker compose up -d
```

Ouvrez **https://server_address:8443** et saisissez le jeton à usage unique, l’adresse e-mail et le mot de passe. Récupérez le jeton dans la console du serveur avec `docker compose exec app cat /data/.bootstrap/setup-token`. La configuration nécessite HTTPS. Le fichier du jeton est supprimé après la configuration ; supprimer le dernier administrateur ne la rouvre pas.

Fonctionnement :

- Les fichiers sont stockés dans `/data`.
- Les certificats TLS se placent dans `deploy/nginx/certs/` — vous devez y déposer un **vrai certificat ou un certificat auto-signé** (la commande de génération se trouve dans l'Option 2). Compose expose uniquement HTTPS ; définissez `NGINX_HTTPS_PORT=443` pour le port standard.

Pour arrêter : `docker compose down` (les données sont conservées). Pour mettre à jour après un `git pull` : `docker compose up -d --build`.

### Option 2. Composants séparés (compilation depuis les sources)

Si vous ne souhaitez pas utiliser Docker, compilez l'exécutable vous-même et lancez-le aux côtés d'un PostgreSQL standard. L'exécutable est autonome — tous les composants y sont intégrés.

**Prérequis de compilation :** Go 1.25+, Node.js 22+, PostgreSQL 16+.

**1. Compilez l'interface web** (Nuxt → fichiers statiques dans `web/dist`) :

```sh
cd web
npm install
npm run generate
cd ..
```

**2. Compilez le serveur** (l'exécutable embarquera `web/dist` et les migrations) :

```sh
CGO_ENABLED=0 go build -trimpath -o discodrive ./cmd/server
```

**3. Démarrez PostgreSQL** et créez la base de données avec son utilisateur :

```sh
createuser disco --pwprompt
createdb discodrive --owner disco
```

**4. Définissez les variables d'environnement** (liste complète dans `.env.example`) :

```sh
export DATABASE_URL="postgres://disco:MOT_DE_PASSE@localhost:5432/discodrive?sslmode=disable"
export JWT_SECRET="$(openssl rand -base64 48)"
export SETTINGS_ENCRYPTION_KEY="$(openssl rand -hex 16)"
export BASE_DOMAIN="example.com"
export STORAGE_ROOT="/var/lib/discodrive/data"   # le répertoire doit exister et être accessible en écriture
export APP_PORT="8080"
export XACCEL_ENABLED="false"                     # sans nginx, le serveur distribue les fichiers lui-même
```

**5. Lancez** :

```sh
./discodrive
```

Configurez un reverse proxy HTTPS avant la configuration initiale. Lisez le jeton dans la console du serveur à l’emplacement `<STORAGE_ROOT>/.bootstrap/setup-token` (ou `SETUP_TOKEN_FILE`) et ouvrez l’adresse HTTPS.

**nginx (facultatif).** Nécessaire pour HTTPS et pour la distribution rapide des fichiers via X-Accel. Prenez `deploy/nginx/default.conf.template` comme base, renseignez vos certificats TLS et activez `XACCEL_ENABLED=true` — le serveur répondra alors avec l'en-tête `X-Accel-Redirect` et nginx se chargera d'envoyer le contenu des fichiers (emplacement `/data/`, valeur du paramètre `STORAGE_ROOT`).

Pour les certificats TLS, utilisez Let's Encrypt (`certbot`) ou générez des certificats auto-signés :

```sh
openssl req -x509 -newkey rsa:2048 -nodes -days 825 \
  -keyout deploy/nginx/certs/dev-key.pem \
  -out deploy/nginx/certs/dev.pem -subj "/CN=localhost"
```

**Démarrage automatique via systemd (facultatif).** Exemple d'unité `/etc/systemd/system/discodrive.service` :

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

Placez les variables de l'étape 4 dans `/etc/discodrive.env` (format `KEY=value`, une par ligne), puis exécutez :

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now discodrive
```

---

## Licence & usage commercial

DiscoDrive est **source-available** sous la licence [PolyForm Noncommercial License 1.0.0](../LICENSE).

- ✅ **Gratuit pour tout usage non commercial** — déployez-le pour vous-même, votre famille, vos loisirs, vos études ou vos expériences. C'est tout l'intérêt.
- ✅ **Modifiez-le comme bon vous semble** — à condition de conserver la mention d'attribution obligatoire.
- ❌ **L'usage commercial n'est pas autorisé.**

Besoin d'un usage commercial ? Une licence commerciale distincte est disponible — écrivez à [info@discodrive.app](mailto:info@discodrive.app).

---

## ❤️ Soutenir le projet

DiscoDrive est développé par ses propres moyens, sans investisseurs. Les retours et suggestions sont les bienvenus — écrivez à [info@discodrive.app](mailto:info@discodrive.app).

Et si DiscoDrive vous plaît et que vous souhaitez le voir grandir — soutenez le projet :

- **[Mettez une étoile sur GitHub](https://github.com/discodrive-cloud/discodrive)** ⭐
- **[Ko-fi](https://ko-fi.com/kosmosoid)**
- **[Boosty](https://boosty.to/kosmosoid/donate)**
- **[Cryptomonnaie](https://nowpayments.io/donation?api_key=7P893T0-48B4KZH-Q6PXBBT-D79X9TK)** — via NOWPayments

Merci ! Chaque contribution fait avancer le projet.
