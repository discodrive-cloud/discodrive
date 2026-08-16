<p align="center">
  <img src="img/logo.jpg" alt="DiscoDrive" width="100%">
</p>

# DiscoDrive

[English](../README.md) · [Deutsch](README.de.md) · [Українська](README.uk.md) · [Français](README.fr.md) · [**Español**](README.es.md) · [Русский](README.ru.md) · [Српски](README.sr.md)

**Tu propia nube. Un servidor, un solo inicio de sesión — archivos, calendarios, contactos, tareas, música y libros.**

DiscoDrive es un reemplazo autoalojado del montón de suscripciones y del zoo de herramientas que normalmente hay que pegar entre sí para dejar iCloud, Google o Dropbox. En lugar de mantener File Browser, Radicale, un servidor Subsonic *y* Calibre-web — cada uno con sus propias complicaciones y peculiaridades — ejecutas una sola cosa. Todo lo de abajo vive tras una única cuenta, en tu hardware, bajo tu control.

Tus archivos se guardan como archivos normales, en carpetas normales del disco. Si alguna vez necesitas dejar DiscoDrive por otro almacenamiento, basta con abrir la carpeta y encontrar tus datos justo donde los dejaste. Sin almacenamiento propietario por bloques, sin una base de datos que no se pueda leer, sin bloqueo de proveedor (vendor lock-in).

Multiplataforma desde el diseño: Apple, Windows, Linux y Android.

**Descargar las apps:** las versiones de escritorio (macOS/Windows/Linux) y Android están en la [página de releases](https://github.com/discodrive-cloud/discodrive-apps/releases/latest); la app de iOS se compila desde el código fuente. Todos los clientes en [discodrive-apps](https://github.com/discodrive-cloud/discodrive-apps).

---

## Qué obtienes

### 📁 Archivos y sincronización

- Un **gestor de archivos web** rápido y moderno — subir, descargar, crear carpetas, renombrar, mover, con arrastrar y soltar archivos *y* carpetas.
- **Vista previa con un clic** — imágenes, PDF, Markdown, código con resaltado de sintaxis, documentos de Word y hojas de Excel se abren directamente en el navegador, sin descargar nada.
- **Versiones** — cada archivo conserva su historial reciente, para deshacer una edición desafortunada.
- **Papelera** — los archivos eliminados van a la papelera y se pueden restaurar.
- **Compartir** — enlaces públicos: con caducidad, protegidos con contraseña, de solo lectura.
- **Móntalo como una unidad** — abre tu almacenamiento directamente desde el Finder, el Explorador de archivos de Windows, el gestor de archivos de Linux o una app de archivos de Android por WebDAV. Nada que instalar.
- **Sincronización en el escritorio** — una app ligera mantiene una carpeta real de tu ordenador sincronizada con el servidor, al estilo Dropbox.
- **No se pierden datos** — si el mismo archivo cambia en dos sitios a la vez, el servidor conserva ambas versiones como copias de conflicto en lugar de sobrescribir una en silencio.

### 🔒 Caja fuerte cifrada

- Una **carpeta privada cifrada de extremo a extremo (E2E)** para tus archivos más sensibles.
- Los archivos se cifran **en tu dispositivo** antes de enviarse — el servidor solo los guarda cifrados.
- Se abre directamente en el navegador o en un cliente (el descifrado ocurre localmente); si olvidas la contraseña, un código de recuperación la desbloquea.
- Construida sobre un **formato abierto, compatible con [Cryptomator](https://cryptomator.org)** — si alguna vez necesitas abrir la caja por separado o pasarte a otro servicio, se puede descifrar con Cryptomator u otras herramientas independientes.

### 📅 Calendario, contactos y tareas

- **Calendarios, libretas de direcciones y recordatorios** completos — eventos, contactos y tareas en un solo lugar.
- **Nativo en Apple** — añade la cuenta en los ajustes del sistema de iOS/macOS, y las apps Calendario, Contactos y Recordatorios simplemente funcionan, sin app adicional.
- **En todo lo demás** — funciona con DAVx⁵ en Android y Thunderbird, Evolution o eM Client en el escritorio.
- Una **interfaz web** práctica para crear y editar eventos, contactos y tareas.
- **Comparte un calendario o una libreta de direcciones** con tus seres queridos.

### 🎵 Música — tu propio servicio de streaming

- Convierte tu colección de música en un servicio de streaming personal, disponible desde cualquier lugar.
- **Funciona con las apps que ya usas** — cualquier reproductor compatible con OpenSubsonic (Amperfy, Feishin y muchos más) se conecta sin más, con letras, cola y búsqueda.
- **Radio por internet** — añade tus emisoras favoritas y escúchalas a través de las mismas apps.
- **Podcasts** — suscríbete a los feeds, descarga y reproduce episodios, y retoma justo donde lo dejaste con los marcadores.
- **Editor de etiquetas integrado** — edita títulos, artistas, álbumes, géneros y carátulas directamente desde la web: pista a pista o una carpeta entera de una vez.

### ▶️ Reproductor — música y vídeo directamente en el navegador

- Haz clic en una pista o una película en tus archivos — y empieza a sonar al instante, sin descargas ni programas de terceros.
- **No estorba** — el reproductor vive en una barra discreta en la parte inferior de la pantalla: la música sigue sonando mientras sigues ordenando tus archivos.
- **Teatro de vídeo** — para el vídeo, la interfaz se despliega en un cómodo modo de visualización a pantalla completa.
- **Ventana flotante** — separa el reproductor en una pequeña ventana sobre las demás aplicaciones (picture-in-picture); sobrevive incluso a una recarga de la página.
- **Ecualizador de 10 bandas** con preajustes listos — ajusta el sonido a tus auriculares o altavoces.

### 📚 Libros — tu propia biblioteca

- Una biblioteca personal para tus libros electrónicos y cómics, lista para leer en cualquier dispositivo.
- **Se lee en cualquier dispositivo** — un catálogo basado en el estándar OPDS al que KOReader, PocketBook, Marvin y otros lectores se conectan directamente (y cualquier lector a través del navegador).
- **Todos los formatos que importan** — EPUB, FB2, PDF, MOBI, CBZ y CBR, con miniaturas de portada y búsqueda en todo el catálogo.
- **Sincronización del progreso de lectura** — empieza un libro en un dispositivo y continúa exactamente en el mismo punto en otro.
- **Editor de metadatos** — edita títulos, autores, series, etiquetas y descripciones desde la web: libro a libro o en bloque por carpeta.

### 🔖 Guardado — marcadores, artículos y descargas

Un rincón personal para todo lo que encontraste en la web y no quieres perder.

- **Marcadores** — tu colección de enlaces vive en tu nube y se sincroniza mediante la extensión del navegador, en lugar de quedar encerrada en un solo navegador.
- **Leer más tarde** — guarda artículos completos: el servidor conserva una versión de texto limpia, sin anuncios ni banners, para que la leas cuando tengas tiempo.
- **Descargas en el servidor** — pásale un enlace y el servidor descarga el archivo por sí mismo, directamente a tu nube — incluso con tu ordenador apagado.

### 🛡️ Cuentas y seguridad

- **Autenticación de dos factores** TOTP con cualquier app de autenticación, más códigos de respaldo de un solo uso.
- **Passkeys** — inicia sesión con Face ID, Touch ID o una llave de hardware, sin contraseña alguna.
- **Registro de seguridad**.
- **Notificaciones por correo** sobre eventos importantes de seguridad y de la cuenta.
- **Protección contra fuerza bruta** de fábrica.
- **Multiusuario** con cuotas de almacenamiento por usuario.
- **Contraseñas por aplicación** para conectar clientes de archivos, música y libros sin exponer tu inicio de sesión principal.
- **TLS en todas partes** por defecto.

### 🌍 Tuyo, en todas partes, en tu idioma

- Compatible con los sistemas operativos Apple, Windows, Linux y Android.
- La interfaz está disponible actualmente en **7 idiomas**: inglés, alemán, ucraniano, francés, español, ruso y serbio.

---

## Instalación

DiscoDrive es un único servidor más PostgreSQL. Delante de él puedes poner nginx para TLS y entrega rápida de archivos mediante X-Accel. A continuación se describen dos formas de desplegarlo todo. La mayoría de los usuarios optará por la **Opción 1**.

> **Antes de empezar hay que preparar dos claves secretas.** El servidor no arrancará con los valores de ejemplo de `.env.example`. Genéralas una sola vez y no las cambies:
>
> - `JWT_SECRET` — `openssl rand -base64 48`;
> - `SETTINGS_ENCRYPTION_KEY` — `openssl rand -hex 16`.
>
> Recuerda también que las apps de Apple (CalDAV/CardDAV) requieren **HTTPS** — no funcionan sobre HTTP sin cifrar.

### Opción 1. Docker con clonado del repositorio (recomendado)

La forma más sencilla: un único `docker compose up` levanta el servidor, la base de datos y nginx con TLS.

**Necesitas:** git, Docker y Docker Compose.

```sh
git clone https://github.com/discodrive-cloud/discodrive.git
cd discodrive
cp .env.example .env
# edita .env: define JWT_SECRET, SETTINGS_ENCRYPTION_KEY, BASE_DOMAIN,
# POSTGRES_PASSWORD y, si quieres, el resto de variables
docker compose up -d
```

Abre **https://server_address:8443** (o http://server_address:8080) — en el primer arranque verás el formulario de creación del administrador. Introduce un correo y una contraseña, y ya está listo. Ten en cuenta que si planeas configurar el envío de correos desde el servicio, debes usar direcciones de correo reales para los nombres de usuario (incluido el administrador); de lo contrario, los mensajes no podrán entregarse.

Cómo funciona por dentro:

- Los archivos se guardan en `/data`.
- Los certificados TLS van en `deploy/nginx/certs/` — debes crear y colocar ahí un **certificado real o autofirmado** (cómo generarlo se describe en la Opción 2) y configurar nginx para redirigir el puerto 80 a HTTPS.

Para detener: `docker compose down` (los datos se conservan). Para actualizar tras un `git pull`: `docker compose up -d --build`.

### Opción 2. Componentes por separado (compilación desde el código fuente)

Si no quieres usar Docker, compila el ejecutable tú mismo y ejecútalo junto a un PostgreSQL estándar. El binario es autocontenido: lleva integrados la interfaz web y las migraciones de base de datos.

**Para compilar necesitas:** Go 1.25+, Node.js 22+, PostgreSQL 16+.

**1. Compila la interfaz web** (Nuxt → archivos estáticos en `web/dist`):

```sh
cd web
npm install
npm run generate
cd ..
```

**2. Compila el servidor** (el binario incluirá `web/dist` y las migraciones):

```sh
CGO_ENABLED=0 go build -trimpath -o discodrive ./cmd/server
```

**3. Levanta PostgreSQL** y crea la base de datos con su usuario:

```sh
createuser disco --pwprompt
createdb discodrive --owner disco
```

**4. Define las variables de entorno** (lista completa en `.env.example`):

```sh
export DATABASE_URL="postgres://disco:CONTRASEÑA@localhost:5432/discodrive?sslmode=disable"
export JWT_SECRET="$(openssl rand -base64 48)"
export SETTINGS_ENCRYPTION_KEY="$(openssl rand -hex 16)"
export BASE_DOMAIN="example.com"
export STORAGE_ROOT="/var/lib/discodrive/data"   # el directorio debe existir y tener permisos de escritura
export APP_PORT="8080"
export XACCEL_ENABLED="false"                     # sin nginx, el servidor sirve los archivos directamente
```

**5. Arranca**:

```sh
./discodrive
```

Abre `http://server_address:8080` y crea el administrador.

**nginx (opcional).** Necesario para HTTPS y para servir archivos rápidamente mediante X-Accel. Toma como base `deploy/nginx/default.conf`, añade tus certificados TLS y establece `XACCEL_ENABLED=true` — así el servidor responde con la cabecera `X-Accel-Redirect` y nginx se encarga de servir el cuerpo del archivo (ruta `/data/`, valor del parámetro `STORAGE_ROOT`).

Para los certificados TLS puedes usar Let's Encrypt (`certbot`) o generar unos autofirmados:

```sh
openssl req -x509 -newkey rsa:2048 -nodes -days 825 \
  -keyout deploy/nginx/certs/dev-key.pem \
  -out deploy/nginx/certs/dev.pem -subj "/CN=localhost"
```

**Arranque automático con systemd (opcional).** Ejemplo de unidad `/etc/systemd/system/discodrive.service`:

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

Pon las variables del paso 4 en `/etc/discodrive.env` (formato `KEY=value`, una por línea) y luego ejecuta:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now discodrive
```

---

## Licencia y uso comercial

DiscoDrive es **source-available** bajo la licencia [PolyForm Noncommercial License 1.0.0](../LICENSE).

- ✅ **Gratis para cualquier uso no comercial** — autoalójalo para ti, tu familia, aficiones, estudios o experimentos. Para eso está hecho.
- ✅ **Modifícalo como quieras** — siempre que conserves el aviso de atribución obligatorio.
- ❌ **El uso comercial no está permitido.**

¿Necesitas uso comercial? Hay disponible una licencia comercial aparte — escribe a [info@discodrive.app](mailto:info@discodrive.app).

---

## ❤️ Apoya el proyecto

DiscoDrive se desarrolla por sus propios medios, sin inversión externa. Los comentarios y sugerencias son bienvenidos — escribe a [info@discodrive.app](mailto:info@discodrive.app).

Y si te gusta DiscoDrive y quieres que siga creciendo — apoya el proyecto:

- **[Dale una estrella en GitHub](https://github.com/discodrive-cloud/discodrive)** ⭐
- **[Ko-fi](https://ko-fi.com/kosmosoid)**
- **[Boosty](https://boosty.to/kosmosoid/donate)**
- **[Criptomonedas](https://nowpayments.io/donation?api_key=7P893T0-48B4KZH-Q6PXBBT-D79X9TK)** — a través de NOWPayments

¡Gracias! Cada aportación ayuda al proyecto a seguir adelante.
