package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"backend/internal/db"
	"github.com/redis/go-redis/v9"
)

type fileSessionStore interface {
	SetTicket(ctx context.Context, key string, payload []byte, ttl time.Duration) error
	ConsumeTicket(ctx context.Context, key string) ([]byte, error)
	AcquireStream(ctx context.Context, userID, kind, streamID string, ttl time.Duration) bool
	RenewStream(ctx context.Context, userID, kind, streamID string, ttl time.Duration) bool
	ReleaseStream(ctx context.Context, userID, kind, streamID string)
}

type redisFileSessionStore struct {
	client *redis.Client
}

func newRedisFileSessionStore(client *redis.Client) *redisFileSessionStore {
	return &redisFileSessionStore{client: client}
}

func (s *redisFileSessionStore) redisClient() (*redis.Client, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("redis client unavailable")
	}
	return s.client, nil
}

func (s *redisFileSessionStore) SetTicket(ctx context.Context, key string, payload []byte, ttl time.Duration) error {
	client, err := s.redisClient()
	if err != nil {
		return err
	}
	return client.Set(ctx, key, payload, ttl).Err()
}

func (s *redisFileSessionStore) ConsumeTicket(ctx context.Context, key string) ([]byte, error) {
	client, err := s.redisClient()
	if err != nil {
		return nil, err
	}
	return client.GetDel(ctx, key).Bytes()
}

func (s *redisFileSessionStore) AcquireStream(ctx context.Context, userID, kind, streamID string, ttl time.Duration) bool {
	client, err := s.redisClient()
	if err != nil {
		return false
	}
	now := time.Now()
	result, err := acquireFileStreamScript.Run(ctx, client, []string{fileStreamRedisKey(userID, kind)}, now.UnixMilli(), fileMaxStreamsPerUser, now.Add(ttl).UnixMilli(), streamID, ttl.Milliseconds()).Int()
	return err == nil && result == 1
}

func (s *redisFileSessionStore) ReleaseStream(ctx context.Context, userID, kind, streamID string) {
	client, err := s.redisClient()
	if err != nil {
		return
	}
	_ = client.ZRem(ctx, fileStreamRedisKey(userID, kind), streamID).Err()
}

func (s *redisFileSessionStore) RenewStream(ctx context.Context, userID, kind, streamID string, ttl time.Duration) bool {
	client, err := s.redisClient()
	if err != nil {
		return false
	}
	now := time.Now()
	result, err := renewFileStreamScript.Run(ctx, client, []string{fileStreamRedisKey(userID, kind)}, now.UnixMilli(), now.Add(ttl).UnixMilli(), ttl.Milliseconds(), streamID).Int()
	return err == nil && result == 1
}

// fileDependencies contains only the infrastructure required by the cloud
// file-manager feature. Cross-cutting request, profile-access, and background
// concerns are injected at module construction.
type fileDependencies struct {
	db                   *sql.DB
	encryptionKey        string
	backgroundCtx        context.Context
	rateLimiter          rateLimiter
	profiles             *ownedProfileAccess
	sessionStore         fileSessionStore
	publishCancelEvent   func(context.Context, string) error
	startTransferIndexer func(context.Context, string)
	requireUserID        func(http.ResponseWriter, *http.Request) (string, bool)
	clientIP             func(*http.Request) string
	writeAudit           func(*http.Request, db.AuditAction, string, string, map[string]interface{})
	logf                 func(*http.Request, string, ...any)
}

type fileAPI struct {
	fileDependencies
}

func newFileAPI(deps fileDependencies) *fileAPI {
	if deps.backgroundCtx == nil {
		deps.backgroundCtx = context.Background()
	}
	if deps.profiles == nil {
		deps.profiles = &ownedProfileAccess{
			db:            deps.db,
			encryptionKey: deps.encryptionKey,
		}
	}
	if deps.requireUserID == nil {
		deps.requireUserID = requireAuthenticatedUserID
	}
	if deps.clientIP == nil {
		deps.clientIP = (&APIServer{}).clientIP
	}
	if deps.writeAudit == nil {
		deps.writeAudit = func(*http.Request, db.AuditAction, string, string, map[string]interface{}) {}
	}
	if deps.logf == nil {
		deps.logf = (&APIServer{}).logf
	}
	return &fileAPI{fileDependencies: deps}
}

func (s *APIServer) files() *fileAPI {
	var redisClient *redis.Client
	var publishCancel func(context.Context, string) error
	if s.queue != nil {
		redisClient = s.queue.RedisClient()
		publishCancel = s.queue.PublishCancelEvent
	}
	var startIndexer func(context.Context, string)
	if s.indexer != nil {
		startIndexer = s.indexer.Start
	}
	return newFileAPI(fileDependencies{
		db:                   s.db,
		encryptionKey:        s.encryptionKey,
		backgroundCtx:        s.backgroundCtx,
		rateLimiter:          s.rateLimiter,
		profiles:             s.profiles(),
		sessionStore:         newRedisFileSessionStore(redisClient),
		publishCancelEvent:   publishCancel,
		startTransferIndexer: startIndexer,
		requireUserID:        s.requireUserID,
		clientIP:             s.clientIP,
		writeAudit:           s.writeAudit,
		logf:                 s.logf,
	})
}

// routes returns the file-manager feature's route definitions for gateway wiring.
// All management endpoints use saved profile IDs only; the ticket consumers
// authenticate with short-lived, single-use tickets rather than a JWT in the URL.
func (api *fileAPI) routes() []routeSpec {
	return []routeSpec{
		{pattern: "GET /api/files/profiles/{profileID}/capabilities", access: routeAuthenticated, handler: api.handleFileCapabilities},
		{pattern: "POST /api/files/profiles/{profileID}/entries:list", access: routeAuthenticated, handler: api.handleFileEntriesList},
		{pattern: "POST /api/files/profiles/{profileID}/entries:resolve", access: routeAuthenticated, handler: api.handleFileEntriesResolve},
		{pattern: "DELETE /api/files/profiles/{profileID}/entries", access: routeAuthenticated, handler: api.handleFileEntryDelete},
		{pattern: "POST /api/files/profiles/{profileID}/entries:rename", access: routeAuthenticated, handler: api.handleFileEntryRename},
		{pattern: "POST /api/files/profiles/{profileID}/entries:copy", access: routeAuthenticated, handler: api.handleFileEntryCopy},
		{pattern: "POST /api/files/profiles/{profileID}/entries:move", access: routeAuthenticated, handler: api.handleFileEntryMove},
		{pattern: "PUT /api/files/profiles/{profileID}/content", access: routeAuthenticated, handler: api.handleFileUpload},
		{pattern: "POST /api/files/profiles/{profileID}/directories", access: routeAuthenticated, handler: api.handleFileDirectoryCreate},
		{pattern: "POST /api/files/profiles/{profileID}/download-tickets", access: routeAuthenticated, handler: api.handleFileDownloadTicket},
		{pattern: "POST /api/files/profiles/{profileID}/archive-tickets", access: routeAuthenticated, handler: api.handleFileArchiveTicket},
		{pattern: "POST /api/files/profiles/{profileID}/entries:batch-delete", access: routeAuthenticated, handler: api.handleFileEntriesBatchDelete},
		{pattern: "POST /api/files/profiles/{profileID}/entries:batch-copy", access: routeAuthenticated, handler: api.handleFileEntriesBatchCopy},
		{pattern: "POST /api/files/profiles/{profileID}/entries:batch-move", access: routeAuthenticated, handler: api.handleFileEntriesBatchMove},
		{pattern: "POST /api/files/profiles/{profileID}/transfers", access: routeAuthenticated, handler: api.handleFileTransferCreate},
		{pattern: "GET /api/files/profiles/{profileID}/transfers", access: routeAuthenticated, handler: api.handleFileTransfersList},
		{pattern: "POST /api/files/transfers/{id}/cancel", access: routeAuthenticated, handler: api.handleFileTransferCancel},
		{pattern: "POST /api/files/profiles/{profileID}/thumbnail", access: routeAuthenticated, handler: api.handleFileThumbnail},
		{pattern: "GET /api/files/download/{ticket}", access: routePublic, handler: api.handleFileDownload},
		{pattern: "GET /api/files/archive/{ticket}", access: routePublic, handler: api.handleFileArchive},
	}
}

// registerRoutes registers the file-manager module's routes on mux with the
// supplied authentication middleware wrapping protected endpoints.
func (api *fileAPI) registerRoutes(mux *http.ServeMux, authenticated func(http.Handler) http.Handler) {
	for _, r := range api.routes() {
		switch r.access {
		case routePublic:
			mux.HandleFunc(r.pattern, r.handler)
		case routeAuthenticated:
			if authenticated == nil {
				panic("authenticated middleware required for protected file routes")
			}
			mux.Handle(r.pattern, authenticated(http.HandlerFunc(r.handler)))
		default:
			panic("unsupported route access category in file module")
		}
	}
}
