// Package api wires up HTTP routing for discodrive.
package api

import (
	"io/fs"
	"net/http"

	"discodrive/internal/appleprofile"
	"discodrive/internal/auth"
	"discodrive/internal/bookmarks"
	"discodrive/internal/caldav"
	"discodrive/internal/carddav"
	davpkg "discodrive/internal/dav"
	"discodrive/internal/db"
	"discodrive/internal/ebook"
	"discodrive/internal/music"
	"discodrive/internal/notify"
	"discodrive/internal/quota"
	"discodrive/internal/saved"
	"discodrive/internal/secret"
	"discodrive/internal/storage"
)

// Server holds the dependencies for the HTTP layer.
type Server struct {
	profiles     appleprofile.Tickets
	auth         *auth.Service
	q            *db.Queries
	files        *storage.FileService
	uploads      *storage.Uploads
	storageRoot  string
	cipher       *secret.Cipher
	notify       *notify.Notifier
	dav          *davpkg.Service
	events       *EventHub
	xaccel       bool
	loginLimiter *rateLimiter
	pollLimiter  *rateLimiter
	feedLimiter  *rateLimiter
	tagEditor    *music.TagEditor
	metaEditor   *ebook.MetadataEditor
	saved        *saved.Service
	bookmarks    *bookmarks.Service
}

// quotaChecker is the same checker the storage layer enforces writes with, so the admin
// panel validates quotas against exactly what the write path will allow. nil (no limits
// configured, or no file service in a test) is a valid value: every method is nil-safe.
func (s *Server) quotaChecker() *quota.Checker {
	if s.files == nil {
		return nil
	}
	return s.files.Quota()
}

// NewRouter registers public and authenticated routes.
func NewRouter(authSvc *auth.Service, q *db.Queries, files *storage.FileService, uploads *storage.Uploads, storageRoot string, cipher *secret.Cipher, notifier *notify.Notifier, ui fs.FS, davHandler http.Handler, caldavHandler http.Handler, carddavHandler http.Handler, davSvc *davpkg.Service, xaccel bool, events *EventHub, subsonicHandler http.Handler, opdsHandler http.Handler, kosyncHandler http.Handler, tagEditor *music.TagEditor, metaEditor *ebook.MetadataEditor, savedSvc *saved.Service, bookmarksSvc *bookmarks.Service) http.Handler {
	s := &Server{auth: authSvc, q: q, files: files, uploads: uploads, storageRoot: storageRoot, cipher: cipher, notify: notifier, dav: davSvc, xaccel: xaccel, events: events, loginLimiter: newLoginLimiter(), pollLimiter: newPollLimiter(), feedLimiter: newLoginLimiter(), tagEditor: tagEditor, metaEditor: metaEditor, saved: savedSvc, bookmarks: bookmarksSvc}
	mux := http.NewServeMux()

	// public
	mux.HandleFunc("GET /apple-profile/{ticket}/DiscoDrive.mobileconfig", s.handleAppleProfileDownload)
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /setup/status", s.handleSetupStatus)
	mux.HandleFunc("POST /setup/admin", s.rateLimited(s.handleSetupAdmin))
	mux.HandleFunc("POST /auth/register", s.rateLimited(s.handleRegister))
	mux.HandleFunc("POST /auth/login", s.rateLimited(s.handleLogin))
	mux.HandleFunc("POST /auth/mfa/totp", s.rateLimited(s.handleMFATOTP))                        // finish 2FA login (carries the MFA-pending token)
	mux.HandleFunc("POST /auth/webauthn/login/begin", s.rateLimited(s.handleWebAuthnLoginBegin)) // passwordless passkey sign-in
	mux.HandleFunc("POST /auth/webauthn/login/finish", s.rateLimited(s.handleWebAuthnLoginFinish))
	mux.HandleFunc("POST /auth/device/token", s.authLimited(s.handleDeviceToken))
	mux.HandleFunc("GET /s/{token}", s.handleLinkDownload)   // public share link
	mux.HandleFunc("GET /cal/{file}", s.handleCalendarFeed)  // public ICS feed
	mux.HandleFunc("GET /files/{id}/stream", s.handleStream) // media stream: auth = purpose=stream token in ?t=
	mux.HandleFunc("POST /pair/init", s.rateLimited(s.handlePairInit))
	mux.HandleFunc("POST /pair/token", s.pollLimited(s.handlePairToken))

	// authenticated (Bearer JWT, scoped by user_id/tenant_id)
	prot := authSvc.Middleware
	mux.Handle("GET /me", prot(http.HandlerFunc(s.handleMe)))
	mux.Handle("POST /me/apple-profile", prot(http.HandlerFunc(s.handleAppleProfile)))
	mux.Handle("GET /me/storage", prot(http.HandlerFunc(s.handleMeStorage)))
	mux.Handle("PUT /me/password", prot(http.HandlerFunc(s.handleChangePassword)))
	mux.Handle("GET /me/totp", prot(http.HandlerFunc(s.handleTOTPStatus)))
	mux.Handle("POST /me/totp/setup", prot(http.HandlerFunc(s.handleTOTPSetup)))
	mux.Handle("POST /me/totp/confirm", prot(http.HandlerFunc(s.rateLimited(s.handleTOTPConfirm))))
	mux.Handle("DELETE /me/totp", prot(http.HandlerFunc(s.rateLimited(s.handleTOTPDisable))))
	mux.Handle("POST /me/totp/backup-codes", prot(http.HandlerFunc(s.rateLimited(s.handleRegenerateBackupCodes))))
	mux.Handle("GET /me/audit", prot(http.HandlerFunc(s.handleAuditList)))
	mux.Handle("POST /me/webauthn/approval/password", prot(http.HandlerFunc(s.rateLimited(s.handlePasskeyApprovalPassword))))
	mux.Handle("POST /me/webauthn/approval/begin", prot(http.HandlerFunc(s.rateLimited(s.handlePasskeyApprovalBegin))))
	mux.Handle("POST /me/webauthn/approval/finish", prot(http.HandlerFunc(s.rateLimited(s.handlePasskeyApprovalFinish))))
	mux.Handle("GET /me/webauthn", prot(http.HandlerFunc(s.handleWebAuthnList)))
	mux.Handle("POST /me/webauthn/register/begin", prot(http.HandlerFunc(s.handleWebAuthnRegisterBegin)))
	mux.Handle("POST /me/webauthn/register/finish", prot(http.HandlerFunc(s.handleWebAuthnRegisterFinish)))
	mux.Handle("PATCH /me/webauthn/{id}", prot(http.HandlerFunc(s.handleWebAuthnRename)))
	mux.Handle("DELETE /me/webauthn/{id}", prot(http.HandlerFunc(s.handleWebAuthnDelete)))
	mux.Handle("GET /files", prot(http.HandlerFunc(s.handleListFiles)))
	mux.Handle("GET /files/{id}", prot(http.HandlerFunc(s.handleGetFile)))
	mux.Handle("GET /files/{id}/content", prot(http.HandlerFunc(s.handleDownload)))
	mux.Handle("GET /files/media", prot(http.HandlerFunc(s.handleMediaListing))) // root variant: no node id
	mux.Handle("GET /files/{id}/media", prot(http.HandlerFunc(s.handleMediaListing)))
	mux.Handle("GET /files/{id}/media-cover", prot(http.HandlerFunc(s.handleMediaCover)))
	mux.Handle("POST /files/folder", prot(http.HandlerFunc(s.handleCreateFolder)))
	mux.Handle("POST /files/upload", prot(http.HandlerFunc(s.handleUpload)))
	mux.Handle("PATCH /files/{id}/rename", prot(http.HandlerFunc(s.handleRename)))
	mux.Handle("PATCH /files/{id}/move", prot(http.HandlerFunc(s.handleMove)))
	mux.Handle("DELETE /files/{id}", prot(http.HandlerFunc(s.handleDelete)))
	mux.Handle("GET /files/{id}/versions", prot(http.HandlerFunc(s.handleListVersions)))
	mux.Handle("POST /files/{id}/restore", prot(http.HandlerFunc(s.handleRestore)))

	// resumable chunked upload
	mux.Handle("POST /upload/init", prot(http.HandlerFunc(s.handleUploadInit)))
	mux.Handle("PUT /upload/{id}/chunk/{n}", prot(http.HandlerFunc(s.handleUploadChunk)))
	mux.Handle("GET /upload/{id}", prot(http.HandlerFunc(s.handleUploadStatus)))
	mux.Handle("POST /upload/{id}/complete", prot(http.HandlerFunc(s.handleUploadComplete)))
	mux.Handle("DELETE /upload/{id}", prot(http.HandlerFunc(s.handleUploadAbort)))

	// trash bin
	mux.Handle("GET /files/trash", prot(http.HandlerFunc(s.handleTrash)))
	mux.Handle("DELETE /files/trash", prot(http.HandlerFunc(s.handlePurgeAll)))
	mux.Handle("POST /files/{id}/undelete", prot(http.HandlerFunc(s.handleUndelete)))
	mux.Handle("DELETE /files/{id}/purge", prot(http.HandlerFunc(s.handlePurge)))

	// shares
	mux.Handle("GET /files/{id}/shares", prot(http.HandlerFunc(s.handleListNodeShares)))
	mux.Handle("POST /files/{id}/share", prot(http.HandlerFunc(s.handleShare)))
	mux.Handle("DELETE /shares/{id}", prot(http.HandlerFunc(s.handleRevokeShare)))
	mux.Handle("GET /shared", prot(http.HandlerFunc(s.handleSharedWithMe)))
	mux.Handle("DELETE /shared/{id}", prot(http.HandlerFunc(s.handleLeaveShare)))

	// device pairing
	mux.Handle("GET /pair/{code}", prot(http.HandlerFunc(s.handlePairInfo)))
	mux.Handle("POST /pair/{code}/approve", prot(http.HandlerFunc(s.handlePairApprove)))

	// user preferences
	mux.Handle("GET /me/language", prot(http.HandlerFunc(s.handleGetLanguage)))
	mux.Handle("PUT /me/language", prot(http.HandlerFunc(s.handleSetLanguage)))

	// session lifetime (how long a session may stay idle before re-login)
	mux.Handle("GET /me/session", prot(http.HandlerFunc(s.handleGetSession)))
	mux.Handle("PUT /me/session", prot(http.HandlerFunc(s.handleSetSession)))

	// external-access toggles (webdav/caldav/carddav enable flags)
	mux.Handle("GET /me/access", prot(http.HandlerFunc(s.handleGetAccess)))
	mux.Handle("PUT /me/access", prot(authSvc.RequireAdmin(http.HandlerFunc(s.handlePutAccess))))

	// ebook settings (OPDS)
	mux.Handle("GET /me/ebooks", prot(http.HandlerFunc(s.handleGetEbookSettings)))
	mux.Handle("PUT /me/ebooks", prot(http.HandlerFunc(s.handlePutEbookSettings)))
	mux.Handle("POST /me/ebooks/password", prot(http.HandlerFunc(s.handlePostEbookPassword)))
	mux.Handle("DELETE /me/ebooks/password", prot(http.HandlerFunc(s.handleDeleteEbookPassword)))

	// ebook metadata editor
	mux.Handle("GET /me/ebooks/meta/{id}", prot(http.HandlerFunc(s.handleGetEbookMeta)))
	mux.Handle("PUT /me/ebooks/meta/{id}", prot(http.HandlerFunc(s.handlePutEbookMeta)))
	mux.Handle("POST /me/ebooks/meta/{id}/reset", prot(http.HandlerFunc(s.handleResetEbookMeta)))
	// NB: a distinct /bulk/ prefix (not /meta/folder/) — "/me/ebooks/meta/folder/{id}"
	// collides with "/me/ebooks/meta/{id}/reset" in net/http's ServeMux (the path
	// "/me/ebooks/meta/folder/reset" matches both) and panics at registration.
	mux.Handle("POST /me/ebooks/bulk/{id}/count", prot(http.HandlerFunc(s.handleMetaFolderCount)))
	mux.Handle("POST /me/ebooks/bulk/{id}", prot(http.HandlerFunc(s.handlePostEbookMetaFolder)))
	mux.Handle("POST /me/ebooks/scan", prot(http.HandlerFunc(s.handlePostEbookScan)))

	// ebook library (session-authed bookshelf)
	mux.Handle("GET /me/ebooks/library", prot(http.HandlerFunc(s.handleListEbooks)))
	mux.Handle("GET /me/ebooks/library/facets", prot(http.HandlerFunc(s.handleEbookFacets)))
	mux.Handle("GET /me/ebooks/library/{id}/cover", prot(http.HandlerFunc(s.handleGetEbookCover)))
	mux.Handle("GET /me/ebooks/library/{id}/download", prot(http.HandlerFunc(s.handleDownloadEbook)))

	// music settings (OpenSubsonic)
	mux.Handle("GET /me/music", prot(http.HandlerFunc(s.handleGetMusicSettings)))
	mux.Handle("PUT /me/music", prot(http.HandlerFunc(s.handlePutMusicSettings)))
	mux.Handle("POST /me/music/password", prot(http.HandlerFunc(s.handlePostMusicPassword)))
	mux.Handle("DELETE /me/music/password", prot(http.HandlerFunc(s.handleDeleteMusicPassword)))
	mux.Handle("GET /me/music/radio", prot(http.HandlerFunc(s.handleListRadio)))
	mux.Handle("POST /me/music/radio", prot(http.HandlerFunc(s.handleCreateRadio)))
	mux.Handle("PUT /me/music/radio/{id}", prot(http.HandlerFunc(s.handleUpdateRadio)))
	mux.Handle("DELETE /me/music/radio/{id}", prot(http.HandlerFunc(s.handleDeleteRadio)))
	mux.Handle("GET /me/music/podcasts", prot(http.HandlerFunc(s.handleListPodcasts)))
	mux.Handle("POST /me/music/podcasts", prot(http.HandlerFunc(s.handleCreatePodcast)))
	mux.Handle("DELETE /me/music/podcasts/{id}", prot(http.HandlerFunc(s.handleDeletePodcast)))
	mux.Handle("GET /me/music/podcasts/{id}/cover", prot(http.HandlerFunc(s.handleGetPodcastCover)))

	// music scan (JWT-authenticated; fires background indexer)
	mux.Handle("POST /me/music/scan", prot(http.HandlerFunc(s.handlePostMusicScan)))

	// music tag editor
	mux.Handle("GET /me/music/tags/{id}", prot(http.HandlerFunc(s.handleGetMusicTags)))
	mux.Handle("GET /me/music/tags/{id}/cover", prot(http.HandlerFunc(s.handleGetMusicTagsCover)))
	mux.Handle("PUT /me/music/tags/{id}", prot(http.HandlerFunc(s.handlePutMusicTags)))
	mux.Handle("POST /me/music/tags/folder/{id}/count", prot(http.HandlerFunc(s.handleMusicTagsFolderCount)))
	mux.Handle("POST /me/music/tags/folder/{id}", prot(http.HandlerFunc(s.handlePostMusicTagsFolder)))

	// sync scope settings (daemon-only; gated by X-Discodrive-Scope header)
	mux.Handle("GET /me/sync", prot(http.HandlerFunc(s.handleGetSyncSettings)))
	mux.Handle("PUT /me/sync", prot(http.HandlerFunc(s.handlePutSyncSettings)))

	// devices (own)
	mux.Handle("GET /devices", prot(http.HandlerFunc(s.handleListDevices)))
	mux.Handle("DELETE /devices/{id}", prot(http.HandlerFunc(s.handleDeleteDevice)))
	mux.Handle("POST /devices/webdav", prot(http.HandlerFunc(s.handleCreateWebdavPassword)))

	// delta sync
	mux.Handle("GET /sync/changes", prot(http.HandlerFunc(s.handleSyncChanges)))
	mux.Handle("GET /sync/events", prot(http.HandlerFunc(s.handleSyncEvents)))
	mux.Handle("PUT /sync/file", prot(http.HandlerFunc(s.handleSyncPutFile)))
	mux.Handle("DELETE /sync/file", prot(http.HandlerFunc(s.handleSyncDelete)))
	mux.Handle("POST /sync/dir", prot(http.HandlerFunc(s.handleSyncMkdir)))
	mux.Handle("GET /sync/meta", prot(http.HandlerFunc(s.handleSyncMeta)))

	// contacts
	mux.Handle("GET /me/contacts", prot(http.HandlerFunc(s.handleListContacts)))
	mux.Handle("POST /me/contacts", prot(http.HandlerFunc(s.handleCreateContact)))
	mux.Handle("GET /me/contacts/{uid}", prot(http.HandlerFunc(s.handleGetContact)))
	mux.Handle("PUT /me/contacts/{uid}", prot(http.HandlerFunc(s.handleUpdateContact)))
	mux.Handle("DELETE /me/contacts/{uid}", prot(http.HandlerFunc(s.handleDeleteContact)))
	mux.Handle("POST /me/contacts/share", prot(http.HandlerFunc(s.handleShareContacts)))
	mux.Handle("GET /me/contacts/shares", prot(http.HandlerFunc(s.handleListContactsShares)))
	mux.Handle("DELETE /me/contacts/shares/{shareId}", prot(http.HandlerFunc(s.handleDeleteContactsShare)))
	mux.Handle("POST /me/saved", prot(http.HandlerFunc(s.handleSavedCreate)))
	mux.Handle("GET /me/saved", prot(http.HandlerFunc(s.handleSavedList)))
	mux.Handle("GET /me/saved/{id}", prot(http.HandlerFunc(s.handleSavedGet)))
	mux.Handle("POST /me/saved/{id}/retry", prot(http.HandlerFunc(s.handleSavedRetry)))
	mux.Handle("DELETE /me/saved/{id}", prot(http.HandlerFunc(s.handleSavedDelete)))
	mux.Handle("GET /me/saved/{id}/content", prot(http.HandlerFunc(s.handleSavedContent)))
	mux.Handle("GET /me/bookmarks", prot(http.HandlerFunc(s.handleBookmarksList)))
	mux.Handle("POST /me/bookmarks", prot(http.HandlerFunc(s.handleBookmarkCreate)))
	mux.Handle("GET /me/bookmarks/changes", prot(http.HandlerFunc(s.handleBookmarksChanges)))
	mux.Handle("POST /me/bookmarks/bulk", prot(http.HandlerFunc(s.handleBookmarksBulk)))
	mux.Handle("PATCH /me/bookmarks/{id}", prot(http.HandlerFunc(s.handleBookmarkUpdate)))
	mux.Handle("DELETE /me/bookmarks/{id}", prot(http.HandlerFunc(s.handleBookmarkDelete)))
	mux.Handle("GET /me/bookmarks/{id}/favicon", prot(http.HandlerFunc(s.handleBookmarkFavicon)))
	mux.Handle("POST /me/contacts/import", prot(http.HandlerFunc(s.handleImportContacts)))
	mux.Handle("GET /me/contacts/export", prot(http.HandlerFunc(s.handleExportContacts)))

	// calendars (collection management)
	mux.Handle("GET /me/calendars", prot(http.HandlerFunc(s.handleListCalendars)))
	mux.Handle("POST /me/calendars", prot(http.HandlerFunc(s.handleCreateCalendar)))
	mux.Handle("PATCH /me/calendars/{id}", prot(http.HandlerFunc(s.handleUpdateCalendar)))
	mux.Handle("DELETE /me/calendars/{id}", prot(http.HandlerFunc(s.handleDeleteCalendar)))
	mux.Handle("POST /me/calendars/{id}/share", prot(http.HandlerFunc(s.handleShareCalendar)))
	mux.Handle("GET /me/calendars/{id}/shares", prot(http.HandlerFunc(s.handleListCalendarShares)))
	mux.Handle("DELETE /me/calendars/{id}/shares/{shareId}", prot(http.HandlerFunc(s.handleDeleteCalendarShare)))
	mux.Handle("POST /me/calendars/{id}/feed", prot(http.HandlerFunc(s.handleCreateFeed)))
	mux.Handle("GET /me/calendars/{id}/feed", prot(http.HandlerFunc(s.handleListFeeds)))
	mux.Handle("DELETE /me/calendars/{id}/feed/{shareId}", prot(http.HandlerFunc(s.handleDeleteFeed)))

	// calendar events
	mux.Handle("GET /me/calendar/events", prot(http.HandlerFunc(s.handleListEvents)))
	mux.Handle("POST /me/calendar/events", prot(http.HandlerFunc(s.handleCreateEvent)))
	mux.Handle("GET /me/calendar/events/{uid}", prot(http.HandlerFunc(s.handleGetEvent)))
	mux.Handle("PUT /me/calendar/events/{uid}", prot(http.HandlerFunc(s.handleUpdateEvent)))
	mux.Handle("DELETE /me/calendar/events/{uid}", prot(http.HandlerFunc(s.handleDeleteEvent)))

	// tasks (VTODO)
	mux.Handle("GET /me/tasks", prot(http.HandlerFunc(s.handleListTasks)))
	mux.Handle("POST /me/tasks", prot(http.HandlerFunc(s.handleCreateTask)))
	mux.Handle("GET /me/tasks/{uid}", prot(http.HandlerFunc(s.handleGetTask)))
	mux.Handle("PUT /me/tasks/{uid}", prot(http.HandlerFunc(s.handleUpdateTask)))
	mux.Handle("PUT /me/tasks/{uid}/done", prot(http.HandlerFunc(s.handleToggleTask)))
	mux.Handle("DELETE /me/tasks/{uid}", prot(http.HandlerFunc(s.handleDeleteTask)))

	// notification preferences
	mux.Handle("GET /me/notifications", prot(http.HandlerFunc(s.handleGetMyNotifications)))
	mux.Handle("PUT /me/notifications", prot(http.HandlerFunc(s.handlePutMyNotification)))

	// admin (role=admin)
	admin := func(h http.HandlerFunc) http.Handler { return prot(authSvc.RequireAdmin(h)) }
	mux.Handle("GET /admin/overview", admin(s.handleAdminOverview))
	mux.Handle("POST /admin/users", admin(s.handleAdminCreateUser))
	mux.Handle("PATCH /admin/users/{id}", admin(s.handleAdminUpdateUser))
	mux.Handle("DELETE /admin/users/{id}", admin(s.handleAdminDeleteUser))
	mux.Handle("GET /admin/settings", admin(s.handleAdminListSettings))
	mux.Handle("PUT /admin/settings", admin(s.handleAdminPutSetting))
	mux.Handle("GET /admin/smtp", admin(s.handleAdminGetSmtp))
	mux.Handle("POST /admin/smtp/test", admin(s.handleAdminSmtpTest))

	// WebDAV (enabled by flag; auth is handled inside davHandler)
	if davHandler != nil {
		mux.Handle("/dav/", davHandler)
	}

	if caldavHandler != nil {
		mux.Handle("/caldav/", caldavHandler)
		mux.Handle("/.well-known/caldav", caldav.WellKnown())
	}

	if carddavHandler != nil {
		mux.Handle("/carddav/", carddavHandler)
		mux.Handle("/.well-known/carddav", carddav.WellKnown())
	}

	if subsonicHandler != nil {
		mux.Handle("/rest/", subsonicHandler)
	}

	if opdsHandler != nil {
		mux.Handle("/opds/", opdsHandler)
		mux.Handle("/opds", opdsHandler)
	}

	if kosyncHandler != nil {
		mux.Handle("/users/", kosyncHandler)
		mux.Handle("/syncs/", kosyncHandler)
	}

	// UI is served under /app/ (API lives at the root, so page paths like
	// /app/files do not conflict with API routes like GET /files). Root redirects.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/app/", http.StatusFound)
	})
	mux.Handle("/app/", http.StripPrefix("/app", spaHandler(ui)))

	return mux
}
