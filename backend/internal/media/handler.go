package media

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// AvatarWriter persists the stored avatar URL against the developer. Satisfied by the
// developer-profile repo.
//
// The URL is written by the SERVER, from a value the server constructed. That is the
// point: the client never gets to choose what its avatar_url says, so the existing
// "must be https://" guard on the client-supplied path cannot be talked around by
// uploading, and a platform-hosted URL does not have to be allow-listed anywhere.
type AvatarWriter interface {
	SetAvatarURL(ctx context.Context, userPublicID, avatarURL string) error
}

// Handler serves avatar upload + object read-back.
type Handler struct {
	store  *Store
	repo   AvatarWriter
	authn  *auth.Authenticator
	log    *slog.Logger
	origin string // absolute base for URLs we hand out (BASE_URL)
}

func NewHandler(store *Store, repo AvatarWriter, authn *auth.Authenticator, baseURL string, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{
		store:  store,
		repo:   repo,
		authn:  authn,
		log:    log,
		origin: strings.TrimRight(baseURL, "/"),
	}
}

func (h *Handler) Register(r chi.Router) {
	// PUBLIC read. An avatar is shown on public profiles to signed-out visitors and
	// crawlers, so gating it behind a session would blank every shared profile card.
	// Safe to serve openly precisely because the key is a content hash: it carries no
	// user id, cannot be enumerated into somebody's identity, and reveals nothing
	// beyond the image the developer chose to publish.
	r.Get("/v1/media/avatars/{name}", h.serveAvatar)

	r.Group(func(gr chi.Router) {
		gr.Use(h.authn.Middleware)
		// Upload is user-scoped: an AGENT key must not be able to change its owner's
		// public identity.
		gr.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/developer/avatar", h.uploadAvatar)
	})
}

// uploadAvatar accepts a multipart image, stores a normalised copy, and points the
// developer's profile at it.
func (h *Handler) uploadAvatar(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())

	if !h.store.Enabled() {
		httpx.Error(w, errUnavailable("Image uploads are not available in this environment."))
		return
	}

	// Cap the body BEFORE reading it. MaxBytesReader makes an oversized upload fail
	// while streaming instead of after buffering it all, so a 2 GB post cannot occupy
	// memory even briefly.
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadBytes+1<<20) // +1MiB for the multipart envelope
	// gosec G120 flags ParseMultipartForm in a handler as unbounded. It is bounded here —
	// by the MaxBytesReader on the line above, which is the fix G120 asks for and which
	// the rule does not track across statements. The argument below is the in-memory
	// threshold (parts larger than it spill to a temp file); it is NOT a request cap, so
	// tuning it would not address the finding and removing the reader would.
	//nolint:gosec // G120: body is bounded by http.MaxBytesReader immediately above
	if err := r.ParseMultipartForm(MaxUploadBytes); err != nil {
		httpx.Error(w, httpx.NewError(http.StatusRequestEntityTooLarge, "image_too_large",
			"That file is too large. Images must be 8 MB or smaller."))
		return
	}
	// "file" first, then "avatar" — two names because two clients could reasonably
	// pick either, and a 400 for the wrong field name is a confusing failure.
	file, _, err := r.FormFile("file")
	if err != nil {
		file, _, err = r.FormFile("avatar")
	}
	if err != nil {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "missing_file",
			"Attach the image as the `file` field of a multipart form."))
		return
	}
	defer file.Close()

	raw, err := io.ReadAll(io.LimitReader(file, MaxUploadBytes+1))
	if err != nil {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "invalid_image", "The upload could not be read."))
		return
	}

	prepared, err := PrepareAvatar(raw)
	if err != nil {
		httpx.Error(w, err)
		return
	}

	if err := h.store.Put(r.Context(), prepared.Key, prepared.ContentType, prepared.Bytes); err != nil {
		// The developer gets a retryable message; the operator gets the reason.
		h.log.Error("media: avatar upload failed", "user", p.UserPublicID, "key", prepared.Key, "error", err)
		httpx.Error(w, httpx.NewError(http.StatusBadGateway, "upload_failed",
			"The image could not be saved. Try again shortly."))
		return
	}

	url := h.publicURL(prepared.Key)
	// Store the pointer only AFTER the bytes are durable. The other order can leave a
	// profile pointing at an object that does not exist — a broken image with no way
	// for the developer to tell whether it saved.
	if err := h.repo.SetAvatarURL(r.Context(), p.UserPublicID, url); err != nil {
		httpx.Error(w, err)
		return
	}

	h.log.Info("media: avatar stored", "user", p.UserPublicID, "key", prepared.Key,
		"bytes", len(prepared.Bytes))
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{"avatar_url": url})
}

// serveAvatar streams a stored avatar.
//
// The arena proxies the bytes rather than redirecting to the bucket, so object storage
// needs no public policy, no second hostname and no signed URLs — one fewer thing that
// can be misconfigured into either a 403 or an open bucket.
func (h *Handler) serveAvatar(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	// The key is a hex hash plus ".jpg". Validating the SHAPE rather than sanitising
	// the string is what makes traversal impossible: "../../etc/passwd" is not 64 hex
	// characters, so it is rejected before it reaches the store.
	if !validAvatarName(name) {
		httpx.Error(w, httpx.NewError(http.StatusNotFound, "not_found", "no such image"))
		return
	}
	if !h.store.Enabled() {
		httpx.Error(w, errUnavailable("Image storage is not available in this environment."))
		return
	}

	obj, err := h.store.Get(r.Context(), "avatars/"+name)
	if errors.Is(err, ErrNotFound) {
		httpx.Error(w, httpx.NewError(http.StatusNotFound, "not_found", "no such image"))
		return
	}
	if err != nil {
		h.log.Warn("media: avatar read failed", "name", name, "error", err)
		httpx.Error(w, httpx.NewError(http.StatusBadGateway, "read_failed", "The image could not be loaded."))
		return
	}
	defer obj.Body.Close()

	ct := obj.ContentType
	if ct == "" {
		ct = "image/jpeg"
	}
	w.Header().Set("Content-Type", ct)
	// Immutable and cacheable by anyone, including shared caches, because the name IS
	// the content: these bytes can never change. This is the whole payoff of
	// content-addressing — a profile photo is fetched once per viewer, forever.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if obj.Size > 0 {
		w.Header().Set("Content-Length", itoa(obj.Size))
	}
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, obj.Body); err != nil {
		h.log.Debug("media: avatar stream interrupted", "name", name, "error", err)
	}
}

// publicURL is where a client should fetch this key. A configured CDN wins; otherwise
// the arena's own absolute origin, so the value stored in the database is a complete
// URL that works in an <img src> from any page on any host.
func (h *Handler) publicURL(key string) string {
	if base := h.store.PublicBase(); base != "" {
		return base + "/" + key
	}
	return h.origin + "/v1/media/" + key
}

// validAvatarName accepts exactly the shape PrepareAvatar produces: 64 lowercase hex
// characters followed by ".jpg".
func validAvatarName(name string) bool {
	const suffix = ".jpg"
	if len(name) != 64+len(suffix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	for i := 0; i < 64; i++ {
		c := name[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
