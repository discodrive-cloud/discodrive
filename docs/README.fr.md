<p align="center">
  <img src="img/logo.jpg" alt="DiscoDrive" width="100%">
</p>

# DiscoDrive

[English](../README.md) · [Deutsch](README.de.md) · [Українська](README.uk.md) · [**Français**](README.fr.md) · [Español](README.es.md) · [Русский](README.ru.md) · [Српски](README.sr.md)

**Votre propre cloud. Un serveur, un seul identifiant — fichiers, photos, agendas, contacts, tâches, musique et livres.**

DiscoDrive est un remplaçant auto-hébergé du paquet d'abonnements et du zoo d'outils qu'on assemble d'habitude pour quitter iCloud, Google ou Dropbox. Au lieu de faire tourner File Browser, Immich, Radicale, un serveur Subsonic *et* Calibre-web — chacun avec ses propres difficultés et particularités — vous ne faites tourner qu'une seule application. Tout ce qui suit vit derrière un seul compte, sur votre matériel, sous votre contrôle.

Vos fichiers sont stockés comme des fichiers ordinaires, dans des dossiers ordinaires sur le disque. Si un jour vous devez quitter DiscoDrive pour un autre stockage, vous ouvrez simplement le dossier et retrouvez vos données exactement là où vous les aviez laissées. Pas de stockage propriétaire par fragments, pas de base de données illisible, pas de vendor lock-in.

Multiplateforme par conception : Apple, Windows, Linux et Android.

**Télécharger les applications :** les versions de bureau (macOS/Windows/Linux) et Android sont sur la [page des releases](https://github.com/discodrive-cloud/discodrive-apps/releases/latest) ; l'application iOS est disponible en [bêta TestFlight](https://testflight.apple.com/join/Mzy8NRsP). Tous les clients sur [discodrive-apps](https://github.com/discodrive-cloud/discodrive-apps).

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

### 📷 Photos — votre propre galerie

- **Toute votre histoire en photos sur une seule frise** — photos et vidéos des dossiers que vous choisissez : envoi automatique depuis le téléphone, archive importée, dossiers partagés avec vous.
- **Les photos d'iPhone s'affichent correctement** — HEIC, HDR et Apple ProRAW rendus comme sur le téléphone, Live Photos animées, vidéos lisibles dans n'importe quel navigateur.
- **Les retouches du téléphone sont gardées avec l'original** — l'app iOS envoie l'original et la version retouchée, et la galerie permet de voir l'une ou l'autre.
- **Ce jour-là** — les photos prises à cette date les années précédentes.
- **Favoris et filtres** — marquez vos meilleures photos et n'affichez que les favoris, que les vidéos, que les Live Photos ou que les RAW.
- **Carte** — chaque photo localisée sur une carte ; zoomez sur une zone pour ne voir que ses photos.
- **Corriger la date et le lieu** — pour une photo ou toute une sélection, ou décaler une série de quelques heures quand l'horloge de l'appareil se trompait. La modification reste avec le fichier.
- **Masquées** — retirez vos photos privées de la frise principale vers une section à part, qui ne s'ouvre qu'après confirmation de votre identité. Les photos supprimées vont à la corbeille et peuvent être restaurées.
- **Partager** — ouvrez toute votre galerie à votre partenaire (qui peut la mêler à sa propre frise), créez des albums partagés avec d'autres utilisateurs ou envoyez un lien public vers un album, avec mot de passe, date d'expiration et téléchargement des originaux en option.

La galerie demande l'image serveur par défaut ; l'image `-lite` (`latest-lite`) est une version plus légère sans galerie.

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
- **Changer de mot de passe déconnecte tous les appareils** et révoque les mots de passe d’application.

### 🌍 À vous, partout, dans votre langue

- Compatible avec les systèmes d'exploitation Apple, Windows, Linux et Android.
- L'interface est actuellement disponible en **7 langues** : anglais, allemand, ukrainien, français, espagnol, russe et serbe.

---

## Installation

Il vous faut un serveur avec Docker et Docker Compose. L’image du serveur est déjà construite pour amd64 et arm64.

```sh
git clone https://github.com/discodrive-cloud/discodrive.git
cd discodrive
cp .env.example .env
```

Dans `.env`, renseignez `POSTGRES_PASSWORD` (et le même mot de passe dans `DATABASE_URL`), `BASE_DOMAIN` et deux clés — le serveur ne démarre pas avec les valeurs d’exemple :

```sh
openssl rand -base64 48   # JWT_SECRET
openssl rand -hex 16      # SETTINGS_ENCRYPTION_KEY
```

Lancez-le et lisez le code de configuration à usage unique :

```sh
docker compose up -d
sudo cat data/.bootstrap/setup-token
```

Ouvrez `https://votre-domaine:8443` (le port 8443 correspond à `NGINX_HTTPS_PORT` dans `.env` ; avec 443 ou derrière votre propre proxy, l’adresse devient simplement `https://votre-domaine`), saisissez le code et créez le compte administrateur. Un certificat auto-signé est généré au premier démarrage : les navigateurs afficheront un avertissement tant que vous ne l’aurez pas remplacé par un vrai.

**[Guide d’installation complet (en anglais) →](https://discodrive.app/docs/install)** Droits sur le dossier de données sous Linux, certificat Let’s Encrypt gratuit, installation sans Docker, mises à jour, sauvegardes et dépannage.

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
