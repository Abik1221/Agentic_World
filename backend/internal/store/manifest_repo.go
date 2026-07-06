package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/agent-arena/arena/internal/events"
	"github.com/agent-arena/arena/internal/manifest"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SeedVerifiedManifest gives an internal/demo agent a pre-verified, ACTIVE manifest
// so it clears the ranked certification gate WITHOUT hosting a real endpoint. The
// platform's rule-based demo bots have no HTTP endpoint to verify the normal way,
// so in dev we record certification directly — this is what lets the demo
// bot-runner produce rated Goofspiel matches (which feed the ELO leaderboard and
// the season champion). DEV-ONLY: called from the demo-bots seeding path, which is
// off in production. Idempotent: a no-op if the agent already has an active manifest.
func (r *ManifestRepo) SeedVerifiedManifest(ctx context.Context, agentPublicID string) error {
	var existing string
	if err := r.db.QueryRow(ctx,
		`SELECT COALESCE(active_manifest_public_id, '') FROM agents WHERE public_id = $1`,
		agentPublicID).Scan(&existing); err != nil {
		return err
	}
	if existing != "" {
		return nil // already certified
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	mid := platform.NewID(platform.PrefixManifest)
	if _, err := tx.Exec(ctx,
		`INSERT INTO agent_manifests
		   (public_id, agent_public_id, manifest_version, agent_version, name,
		    endpoint_url, auth_type, runtime_timeout_ms, raw_document, normalized, status)
		 VALUES ($1, $2, '1.0', '1.0.0', 'demo-bot', 'internal://demo', 'none', 5000, '{}', '{}'::jsonb, 'verified')
		 ON CONFLICT (agent_public_id, agent_version) DO NOTHING`,
		mid, agentPublicID); err != nil {
		return err
	}
	// Adopt whichever verified manifest the agent has (the one just inserted, or a
	// pre-existing row if this ran before).
	var active string
	if err := tx.QueryRow(ctx,
		`SELECT public_id FROM agent_manifests
		 WHERE agent_public_id = $1 AND status = 'verified'
		 ORDER BY id DESC LIMIT 1`, agentPublicID).Scan(&active); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE agents SET active_manifest_public_id = $2, updated_at = now() WHERE public_id = $1`,
		agentPublicID, active); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ManifestRepo is the pgx-backed implementation of manifest.Repo. All SQL is
// parameterized. As elsewhere in this package the queries are hand-written (the
// sqlc target in sqlc.yaml can generate equivalents without changing the port).
type ManifestRepo struct{ db *pgxpool.Pool }

// NewManifestRepo wires the repo to the connection pool.
func NewManifestRepo(db *pgxpool.Pool) *ManifestRepo { return &ManifestRepo{db: db} }

var _ manifest.Repo = (*ManifestRepo)(nil)

// manifestColumns is the shared projection for reading a manifest row. Nullable
// text columns are coalesced to ”; the genuinely optional model columns are
// read into pointers so absence is distinguishable.
// Columns are qualified with the `m` alias because ActiveManifest JOINs `agents`
// (which also has public_id) — every query below aliases agent_manifests as m.
const manifestColumns = `
	m.public_id, m.agent_public_id, m.manifest_version, m.agent_version, m.name,
	COALESCE(m.description,''), m.visibility, COALESCE(m.developer_name,''), COALESCE(m.organization,''),
	m.endpoint_url, m.auth_type, m.runtime_timeout_ms, COALESCE(m.runtime_max_memory,''),
	m.model_provider, m.model_name, m.model_reasoning,
	COALESCE(m.sdk_language,''), COALESCE(m.sdk_version,''), COALESCE(m.contact_email,''),
	m.status, m.created_at`

func (r *ManifestRepo) AgentOwned(ctx context.Context, agentPublicID, ownerPublicID string) (bool, error) {
	var one int
	err := r.db.QueryRow(ctx,
		`SELECT 1 FROM agents a JOIN users u ON u.id = a.owner_user_id
		 WHERE a.public_id = $1 AND u.public_id = $2`,
		agentPublicID, ownerPublicID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (r *ManifestRepo) InsertManifest(ctx context.Context, m manifest.Manifest, rawDoc string, normalized []byte) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after commit

	var provider, name any
	var reasoning any
	if m.Model != nil {
		provider = nullString(m.Model.Provider)
		name = nullString(m.Model.Model)
		reasoning = m.Model.Reasoning
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO agent_manifests (
			public_id, agent_public_id, manifest_version, agent_version, name, description,
			visibility, developer_name, organization, endpoint_url, auth_type,
			runtime_timeout_ms, runtime_max_memory, model_provider, model_name, model_reasoning,
			sdk_language, sdk_version, contact_email, raw_document, normalized, status)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21::jsonb,$22)`,
		m.PublicID, m.AgentPublicID, m.ManifestVersion, m.AgentVersion, m.Name, nullString(m.Description),
		m.Visibility, nullString(m.DeveloperName), nullString(m.Organization), m.EndpointURL, m.AuthType,
		m.RuntimeTimeoutMs, nullString(m.RuntimeMaxMemory), provider, name, reasoning,
		nullString(m.SDKLanguage), nullString(m.SDKVersion), nullString(m.ContactEmail),
		rawDoc, normalized, m.Status)
	if err != nil {
		if isUniqueViolation(err) {
			return manifest.ErrVersionExists
		}
		return err
	}

	for _, g := range m.Games {
		if _, err = tx.Exec(ctx,
			`INSERT INTO agent_manifest_games (manifest_public_id, game_id) VALUES ($1, $2)`,
			m.PublicID, g); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

func (r *ManifestRepo) ActiveManifest(ctx context.Context, agentPublicID string) (manifest.Manifest, bool, error) {
	row := r.db.QueryRow(ctx,
		`SELECT `+manifestColumns+`
		 FROM agent_manifests m
		 JOIN agents a ON a.active_manifest_public_id = m.public_id
		 WHERE a.public_id = $1`, agentPublicID)
	return r.scanOne(ctx, row)
}

func (r *ManifestRepo) LatestManifest(ctx context.Context, agentPublicID string) (manifest.Manifest, bool, error) {
	row := r.db.QueryRow(ctx,
		`SELECT `+manifestColumns+`
		 FROM agent_manifests m
		 WHERE m.agent_public_id = $1
		 ORDER BY m.created_at DESC, m.id DESC
		 LIMIT 1`, agentPublicID)
	return r.scanOne(ctx, row)
}

func (r *ManifestRepo) ListVersions(ctx context.Context, agentPublicID string) ([]manifest.Manifest, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+manifestColumns+`
		 FROM agent_manifests m
		 WHERE m.agent_public_id = $1
		 ORDER BY m.created_at DESC, m.id DESC`, agentPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []manifest.Manifest
	for rows.Next() {
		m, err := scanManifest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		games, err := r.loadGames(ctx, out[i].PublicID)
		if err != nil {
			return nil, err
		}
		out[i].Games = games
	}
	return out, nil
}

// scanOne scans a single manifest row (found=false on no rows) and loads its
// games.
func (r *ManifestRepo) scanOne(ctx context.Context, row pgx.Row) (manifest.Manifest, bool, error) {
	m, err := scanManifest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return manifest.Manifest{}, false, nil
	}
	if err != nil {
		return manifest.Manifest{}, false, err
	}
	games, err := r.loadGames(ctx, m.PublicID)
	if err != nil {
		return manifest.Manifest{}, false, err
	}
	m.Games = games
	return m, true, nil
}

func (r *ManifestRepo) loadGames(ctx context.Context, manifestPublicID string) ([]string, error) {
	rows, err := r.db.Query(ctx,
		`SELECT game_id FROM agent_manifest_games WHERE manifest_public_id = $1 ORDER BY game_id`,
		manifestPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var games []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		games = append(games, g)
	}
	return games, rows.Err()
}

func (r *ManifestRepo) GetManifest(ctx context.Context, agentPublicID, manifestPublicID string) (manifest.Manifest, bool, error) {
	row := r.db.QueryRow(ctx,
		`SELECT `+manifestColumns+`
		 FROM agent_manifests m
		 WHERE m.public_id = $1 AND m.agent_public_id = $2`, manifestPublicID, agentPublicID)
	return r.scanOne(ctx, row)
}

func (r *ManifestRepo) SetEndpointToken(ctx context.Context, agentPublicID, manifestPublicID string, sealed []byte) error {
	ct, err := r.db.Exec(ctx,
		`UPDATE agent_manifests SET endpoint_auth_token_enc = $3
		 WHERE public_id = $1 AND agent_public_id = $2`,
		manifestPublicID, agentPublicID, sealed)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return manifest.ErrNoManifest
	}
	return nil
}

func (r *ManifestRepo) EndpointToken(ctx context.Context, manifestPublicID string) ([]byte, bool, error) {
	var enc []byte
	err := r.db.QueryRow(ctx,
		`SELECT endpoint_auth_token_enc FROM agent_manifests WHERE public_id = $1`,
		manifestPublicID).Scan(&enc)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return enc, len(enc) > 0, nil
}

func (r *ManifestRepo) RecordVerification(ctx context.Context, a manifest.VerificationAttempt) error {
	var games any
	if len(a.HandshakeGames) > 0 {
		b, err := json.Marshal(a.HandshakeGames)
		if err != nil {
			return err
		}
		games = b
	}
	_, err := r.db.Exec(ctx,
		`INSERT INTO agent_endpoint_verifications
		    (manifest_public_id, health_ok, health_latency_ms, handshake_ok,
		     handshake_sdk_version, handshake_games, error)
		 VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7)`,
		a.ManifestPublicID, a.HealthOK, a.HealthLatencyMs, a.HandshakeOK,
		nullString(a.HandshakeSDKVersion), games, nullString(a.Error))
	return err
}

func (r *ManifestRepo) MarkVerifiedAndActivate(ctx context.Context, agentPublicID, manifestPublicID string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after commit

	ct, err := tx.Exec(ctx,
		`UPDATE agent_manifests SET status = 'verified'
		 WHERE public_id = $1 AND agent_public_id = $2`, manifestPublicID, agentPublicID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return manifest.ErrNoManifest
	}
	if _, err = tx.Exec(ctx,
		`UPDATE agents SET active_manifest_public_id = $2, updated_at = now()
		 WHERE public_id = $1`, agentPublicID, manifestPublicID); err != nil {
		return err
	}

	// Emit agent.certified in the SAME transaction (transactional outbox): the
	// event exists iff the certification commits. Downstream (badges,
	// notifications, analytics) projects off this.
	payload, err := json.Marshal(map[string]string{
		"agent_id":    agentPublicID,
		"manifest_id": manifestPublicID,
	})
	if err != nil {
		return err
	}
	if _, err = InsertEventTx(ctx, tx, events.TypeAgentCertified, payload); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// scanManifest maps one row (from QueryRow or Query) to a Manifest. The model
// columns are read into pointers; a Model is attached only when present.
func scanManifest(row pgx.Row) (manifest.Manifest, error) {
	var m manifest.Manifest
	var provider, name *string
	var reasoning *bool
	err := row.Scan(
		&m.PublicID, &m.AgentPublicID, &m.ManifestVersion, &m.AgentVersion, &m.Name, &m.Description,
		&m.Visibility, &m.DeveloperName, &m.Organization, &m.EndpointURL, &m.AuthType,
		&m.RuntimeTimeoutMs, &m.RuntimeMaxMemory, &provider, &name, &reasoning,
		&m.SDKLanguage, &m.SDKVersion, &m.ContactEmail, &m.Status, &m.CreatedAt)
	if err != nil {
		return manifest.Manifest{}, err
	}
	if provider != nil || name != nil || reasoning != nil {
		mb := manifest.ModelBlock{}
		if provider != nil {
			mb.Provider = *provider
		}
		if name != nil {
			mb.Model = *name
		}
		if reasoning != nil {
			mb.Reasoning = *reasoning
		}
		m.Model = &mb
	}
	return m, nil
}
