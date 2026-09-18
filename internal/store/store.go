package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
	_ "github.com/mattn/go-sqlite3"
)

type ParticipantInfo struct {
	AgentName        string   `json:"agent_name"`
	Adapter          string   `json:"adapter,omitempty"`
	HarnessSessionID string   `json:"harness_session_id"`
	Role             string   `json:"role"`
	Roles            []string `json:"roles,omitempty"`
	Writable         bool     `json:"writable"`
}

type Session struct {
	ID                string                     `json:"id"`
	RepoRoot          string                     `json:"repo_root"`
	Task              string                     `json:"task"`
	Status            string                     `json:"status"`
	StopReason        string                     `json:"stop_reason,omitempty"`
	WriterParticipant string                     `json:"writer_participant,omitempty"`
	CreatedAt         time.Time                  `json:"created_at"`
	UpdatedAt         time.Time                  `json:"updated_at"`
	Participants      map[string]ParticipantInfo `json:"participants"`
	Budget            protocol.BudgetStatus      `json:"budget"`
	Metadata          map[string]any             `json:"metadata,omitempty"`
}

type Store interface {
	SaveSession(ctx context.Context, session *Session) error
	GetSession(ctx context.Context, id string) (*Session, error)
	ListSessions(ctx context.Context) ([]*Session, error)
	UpdateSessionStatus(ctx context.Context, id, status, stopReason string) error
	UpdateSessionBudget(ctx context.Context, id string, budget protocol.BudgetStatus) error
	UpdateParticipantHarnessSession(ctx context.Context, sessionID, agentName, harnessSessionID string) error

	SaveMessage(ctx context.Context, env *protocol.PeerEnvelope) error
	GetMessages(ctx context.Context, sessionID string) ([]*protocol.PeerEnvelope, error)

	SaveFinding(ctx context.Context, sessionID string, f *protocol.FindingPayload) error
	GetFindings(ctx context.Context, sessionID string) ([]protocol.FindingPayload, error)
	GetFinding(ctx context.Context, sessionID, findingID string) (*protocol.FindingPayload, error)
	UpdateFindingStatus(ctx context.Context, sessionID, findingID string, status protocol.FindingStatus) error

	SaveEvidence(ctx context.Context, sessionID string, ev *protocol.EvidencePayload) error
	GetEvidence(ctx context.Context, sessionID string) ([]protocol.EvidencePayload, error)
	GetEvidenceByFinding(ctx context.Context, sessionID, findingID string) ([]protocol.EvidencePayload, error)

	SaveChallenge(ctx context.Context, sessionID string, ch *protocol.ChallengePayload) error
	GetChallenges(ctx context.Context, sessionID string) ([]protocol.ChallengePayload, error)

	SaveResolution(ctx context.Context, sessionID string, res *protocol.ResolutionPayload) error
	GetResolutions(ctx context.Context, sessionID string) ([]protocol.ResolutionPayload, error)

	SaveIdempotency(ctx context.Context, key, sessionID, resultJSON string) error
	GetIdempotency(ctx context.Context, key string) (string, error)

	SaveRoutingDecision(ctx context.Context, r *RoutingDecisionRecord) error
	GetRoutingDecisions(ctx context.Context, sessionID string) ([]RoutingDecisionRecord, error)

	SaveUsage(ctx context.Context, u *UsageRecord) error
	GetUsage(ctx context.Context, sessionID string) ([]UsageRecord, error)

	EmitEvent(ctx context.Context, sessionID, eventType string, payload any) error
	GetEvents(ctx context.Context, sessionID string) ([]EventRecord, error)

	// v0.3.0 Collaboration Space Methods
	SaveSpace(ctx context.Context, space *protocol.CollaborationSpace) error
	GetSpace(ctx context.Context, id string) (*protocol.CollaborationSpace, error)
	ListSpaces(ctx context.Context) ([]*protocol.CollaborationSpace, error)
	UpdateSpaceLifecycle(ctx context.Context, id string, state protocol.SpaceLifecycleState) error
	AddSpaceParticipant(ctx context.Context, spaceID string, p *protocol.SpaceParticipant) error
	UpdateParticipantMode(ctx context.Context, spaceID, participantID string, mode protocol.ParticipantActivityMode) error

	CreateChannel(ctx context.Context, ch *protocol.Channel) error
	GetChannel(ctx context.Context, spaceID, channelID string) (*protocol.Channel, error)
	ListChannels(ctx context.Context, spaceID string) ([]protocol.Channel, error)

	CreateThread(ctx context.Context, th *protocol.Thread) error
	GetThread(ctx context.Context, threadID string) (*protocol.Thread, error)
	ListThreads(ctx context.Context, spaceID, channelID string) ([]protocol.Thread, error)

	GetSpaceMessages(ctx context.Context, spaceID, channelID, threadID string, limit int) ([]*protocol.PeerEnvelope, error)

	SaveSubscription(ctx context.Context, sub *protocol.Subscription) error
	GetSubscriptions(ctx context.Context, spaceID string) ([]protocol.Subscription, error)
	GetParticipantSubscriptions(ctx context.Context, spaceID, participantID string) ([]protocol.Subscription, error)
	DeleteSubscription(ctx context.Context, id string) error

	RecordEventDelivery(ctx context.Context, d *protocol.EventDelivery) error
	GetEventDelivery(ctx context.Context, eventID, participantID string) (*protocol.EventDelivery, error)
	UpdateEventDeliveryStatus(ctx context.Context, id string, status protocol.DeliveryStatus, resultMsgID, skipReason string) error

	SaveDecision(ctx context.Context, d *protocol.Decision) error
	GetDecision(ctx context.Context, spaceID, id string) (*protocol.Decision, error)
	ListDecisions(ctx context.Context, spaceID string) ([]protocol.Decision, error)

	UpdateParticipantCursor(ctx context.Context, cursor *protocol.ParticipantCursor) error
	GetParticipantCursor(ctx context.Context, spaceID, participantID string) (*protocol.ParticipantCursor, error)

	SaveSummary(ctx context.Context, spaceID, targetType, targetID, text string, sourceMsgIDs []string) error
	GetSummaries(ctx context.Context, spaceID, targetID string) ([]string, error)

	Close() error
}

type EventRecord struct {
	ID        int64          `json:"id"`
	SessionID string         `json:"session_id"`
	EventType string         `json:"event_type"`
	Timestamp time.Time      `json:"timestamp"`
	Payload   map[string]any `json:"payload,omitempty"`
}

type RoutingDecisionRecord struct {
	ID                  string    `json:"id"`
	SessionID           string    `json:"session_id"`
	RequestedCapability string    `json:"requested_capability"`
	TaskTier            string    `json:"task_tier"`
	Eligible            []string  `json:"eligible"`
	Selected            string    `json:"selected"`
	Reason              []string  `json:"reason"`
	Timestamp           time.Time `json:"timestamp"`
}

type UsageRecord struct {
	ID                  string    `json:"id"`
	SessionID           string    `json:"session_id"`
	Participant         string    `json:"participant"`
	Adapter             string    `json:"adapter"`
	Model               string    `json:"model,omitempty"`
	ModelRoutingBackend string    `json:"model_routing_backend,omitempty"`
	Route               string    `json:"route,omitempty"`
	InputTokens         int64     `json:"input_tokens,omitempty"`
	OutputTokens        int64     `json:"output_tokens,omitempty"`
	CachedTokens        int64     `json:"cached_tokens,omitempty"`
	DurationMS          int64     `json:"duration_ms,omitempty"`
	MonetaryCostUSD     float64   `json:"monetary_cost_usd,omitempty"`
	EscalationCount     int       `json:"escalation_count,omitempty"`
	SelectionReason     string    `json:"selection_reason,omitempty"`
	CostSource          string    `json:"cost_source,omitempty"` // "provider", "configured", "switchyard", "unknown"
	Timestamp           time.Time `json:"timestamp"`
}

type SQLiteStore struct {
	db   *sql.DB
	path string
	mu   sync.RWMutex
}

func canWriteDir(dir string) bool {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return false
	}
	testFile := filepath.Join(dir, ".permcheck")
	if err := os.WriteFile(testFile, []byte("ok"), 0600); err != nil {
		return false
	}
	_ = os.Remove(testFile)
	return true
}

func OpenSQLite(dbPath string) (*SQLiteStore, error) {
	if envDB := os.Getenv("HARNESSMESH_DB_PATH"); envDB != "" && dbPath == "" {
		dbPath = envDB
	}

	if dbPath == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			dir := filepath.Join(home, ".harnessmesh")
			if canWriteDir(dir) {
				dbPath = filepath.Join(dir, "harnessmesh.db")
			}
		}
		if dbPath == "" {
			dir := ".harnessmesh"
			_ = os.MkdirAll(dir, 0700)
			dbPath = filepath.Join(dir, "harnessmesh.db")
		}
	} else {
		dir := filepath.Dir(dbPath)
		if dir != "" && dir != "." {
			_ = os.MkdirAll(dir, 0700)
		}
	}

	absPath, err := filepath.Abs(dbPath)
	if err == nil {
		dbPath = absPath
	}

	// SQLite connection string with WAL and busy_timeout
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON", dbPath)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	db.SetMaxOpenConns(1) // Single connection to avoid SQLite locking issues across goroutines

	store := &SQLiteStore{
		db:   db,
		path: dbPath,
	}

	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate sqlite db: %w", err)
	}

	return store, nil
}

func (s *SQLiteStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *SQLiteStore) migrate() error {
	ctx := context.Background()
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL
		);
	`)
	if err != nil {
		return err
	}

	var currentVersion int
	row := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations;`)
	if err := row.Scan(&currentVersion); err != nil {
		return err
	}

	migrations := []struct {
		version int
		sql     string
	}{
		{
			version: 1,
			sql: `
				CREATE TABLE IF NOT EXISTS sessions (
					id TEXT PRIMARY KEY,
					repo_root TEXT NOT NULL,
					task TEXT NOT NULL,
					status TEXT NOT NULL,
					stop_reason TEXT NOT NULL DEFAULT '',
					created_at TIMESTAMP NOT NULL,
					updated_at TIMESTAMP NOT NULL,
					budget_json TEXT NOT NULL DEFAULT '{}',
					metadata_json TEXT NOT NULL DEFAULT '{}'
				);

				CREATE TABLE IF NOT EXISTS session_participants (
					session_id TEXT NOT NULL,
					agent_name TEXT NOT NULL,
					harness_session_id TEXT NOT NULL DEFAULT '',
					role TEXT NOT NULL DEFAULT 'peer',
					writable INTEGER NOT NULL DEFAULT 0,
					PRIMARY KEY (session_id, agent_name),
					FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
				);

				CREATE TABLE IF NOT EXISTS messages (
					id TEXT PRIMARY KEY,
					session_id TEXT NOT NULL,
					from_agent TEXT NOT NULL,
					to_agent TEXT NOT NULL,
					type TEXT NOT NULL,
					depth INTEGER NOT NULL DEFAULT 0,
					correlation_id TEXT NOT NULL DEFAULT '',
					idempotency_key TEXT NOT NULL DEFAULT '',
					created_at TIMESTAMP NOT NULL,
					payload_json TEXT NOT NULL,
					FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
				);

				CREATE TABLE IF NOT EXISTS findings (
					session_id TEXT NOT NULL,
					id TEXT NOT NULL,
					source_agent TEXT NOT NULL,
					severity TEXT NOT NULL,
					category TEXT NOT NULL,
					file TEXT NOT NULL DEFAULT '',
					line INTEGER NOT NULL DEFAULT 0,
					end_line INTEGER NOT NULL DEFAULT 0,
					claim TEXT NOT NULL,
					evidence TEXT NOT NULL,
					recommendation TEXT NOT NULL,
					status TEXT NOT NULL,
					timestamp TIMESTAMP NOT NULL,
					PRIMARY KEY (session_id, id),
					FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
				);

				CREATE TABLE IF NOT EXISTS evidence (
					id TEXT PRIMARY KEY,
					session_id TEXT NOT NULL,
					finding_id TEXT NOT NULL DEFAULT '',
					message_id TEXT NOT NULL DEFAULT '',
					source_agent TEXT NOT NULL,
					evidence_type TEXT NOT NULL,
					command TEXT NOT NULL DEFAULT '',
					result TEXT NOT NULL DEFAULT '',
					excerpt TEXT NOT NULL DEFAULT '',
					exit_code INTEGER,
					created_at TIMESTAMP NOT NULL,
					metadata_json TEXT NOT NULL DEFAULT '{}',
					FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
				);

				CREATE TABLE IF NOT EXISTS challenges (
					id TEXT PRIMARY KEY,
					session_id TEXT NOT NULL,
					finding_id TEXT NOT NULL,
					challenger TEXT NOT NULL,
					claim TEXT NOT NULL,
					evidence TEXT NOT NULL,
					requested_verification TEXT NOT NULL DEFAULT '',
					created_at TIMESTAMP NOT NULL,
					FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
				);

				CREATE TABLE IF NOT EXISTS resolutions (
					id TEXT PRIMARY KEY,
					session_id TEXT NOT NULL,
					finding_id TEXT NOT NULL,
					status TEXT NOT NULL,
					rationale TEXT NOT NULL,
					resolving_agent TEXT NOT NULL,
					evidence_refs_json TEXT NOT NULL DEFAULT '[]',
					timestamp TIMESTAMP NOT NULL,
					FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
				);

				CREATE TABLE IF NOT EXISTS idempotency_records (
					key TEXT PRIMARY KEY,
					session_id TEXT NOT NULL,
					result_json TEXT NOT NULL,
					created_at TIMESTAMP NOT NULL
				);

				CREATE TABLE IF NOT EXISTS events (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					session_id TEXT NOT NULL,
					event_type TEXT NOT NULL,
					timestamp TIMESTAMP NOT NULL,
					payload_json TEXT NOT NULL,
					FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
				);
			`,
		},
		{
			version: 2,
			sql: `
				ALTER TABLE findings ADD COLUMN duplicate_of TEXT NOT NULL DEFAULT '';
				ALTER TABLE findings ADD COLUMN related_findings_json TEXT NOT NULL DEFAULT '[]';
				ALTER TABLE findings ADD COLUMN source_participant TEXT NOT NULL DEFAULT '';
				ALTER TABLE findings ADD COLUMN source_adapter TEXT NOT NULL DEFAULT '';
				ALTER TABLE findings ADD COLUMN evidence_refs_json TEXT NOT NULL DEFAULT '[]';
				ALTER TABLE sessions ADD COLUMN writer_participant TEXT NOT NULL DEFAULT '';
			`,
		},
		{
			version: 3,
			sql: `
				CREATE TABLE IF NOT EXISTS routing_decisions (
					id TEXT PRIMARY KEY,
					session_id TEXT NOT NULL,
					requested_capability TEXT NOT NULL,
					task_tier TEXT NOT NULL,
					eligible_json TEXT NOT NULL DEFAULT '[]',
					selected TEXT NOT NULL,
					reason_json TEXT NOT NULL DEFAULT '[]',
					timestamp TIMESTAMP NOT NULL,
					FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
				);

				CREATE TABLE IF NOT EXISTS usage_records (
					id TEXT PRIMARY KEY,
					session_id TEXT NOT NULL,
					participant TEXT NOT NULL,
					adapter TEXT NOT NULL,
					model TEXT NOT NULL DEFAULT '',
					model_routing_backend TEXT NOT NULL DEFAULT '',
					route TEXT NOT NULL DEFAULT '',
					input_tokens INTEGER NOT NULL DEFAULT 0,
					output_tokens INTEGER NOT NULL DEFAULT 0,
					cached_tokens INTEGER NOT NULL DEFAULT 0,
					duration_ms INTEGER NOT NULL DEFAULT 0,
					monetary_cost_usd REAL NOT NULL DEFAULT 0.0,
					escalation_count INTEGER NOT NULL DEFAULT 0,
					selection_reason TEXT NOT NULL DEFAULT '',
					cost_source TEXT NOT NULL DEFAULT 'unknown',
					timestamp TIMESTAMP NOT NULL,
					FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
				);
			`,
		},
		{
			version: 4,
			sql: `
				ALTER TABLE messages ADD COLUMN causation_id TEXT NOT NULL DEFAULT '';
				ALTER TABLE messages ADD COLUMN external_session_id TEXT NOT NULL DEFAULT '';
				ALTER TABLE messages ADD COLUMN duration_ms INTEGER NOT NULL DEFAULT 0;
				ALTER TABLE messages ADD COLUMN status TEXT NOT NULL DEFAULT '';
			`,
		},
		{
			version: 5,
			sql: `
				CREATE TABLE IF NOT EXISTS collaboration_spaces (
					id TEXT PRIMARY KEY,
					workspace_id TEXT NOT NULL DEFAULT '',
					title TEXT NOT NULL,
					purpose TEXT NOT NULL DEFAULT '',
					lifecycle_state TEXT NOT NULL DEFAULT 'active',
					writer_participant TEXT NOT NULL DEFAULT '',
					budget_json TEXT NOT NULL DEFAULT '{}',
					metadata_json TEXT NOT NULL DEFAULT '{}',
					created_at TIMESTAMP NOT NULL,
					updated_at TIMESTAMP NOT NULL
				);

				CREATE TABLE IF NOT EXISTS space_participants (
					space_id TEXT NOT NULL,
					participant_id TEXT NOT NULL,
					adapter TEXT NOT NULL DEFAULT '',
					roles_json TEXT NOT NULL DEFAULT '[]',
					capabilities_json TEXT NOT NULL DEFAULT '[]',
					mode TEXT NOT NULL DEFAULT 'active',
					writable INTEGER NOT NULL DEFAULT 0,
					joined_at TIMESTAMP NOT NULL,
					updated_at TIMESTAMP NOT NULL,
					PRIMARY KEY (space_id, participant_id),
					FOREIGN KEY (space_id) REFERENCES collaboration_spaces(id) ON DELETE CASCADE
				);

				CREATE TABLE IF NOT EXISTS channels (
					id TEXT PRIMARY KEY,
					space_id TEXT NOT NULL,
					name TEXT NOT NULL,
					description TEXT NOT NULL DEFAULT '',
					visibility TEXT NOT NULL DEFAULT 'all_participants',
					allowed_participants_json TEXT NOT NULL DEFAULT '[]',
					allowed_capabilities_json TEXT NOT NULL DEFAULT '[]',
					created_by TEXT NOT NULL DEFAULT '',
					created_at TIMESTAMP NOT NULL,
					archived_at TIMESTAMP,
					FOREIGN KEY (space_id) REFERENCES collaboration_spaces(id) ON DELETE CASCADE
				);

				CREATE TABLE IF NOT EXISTS threads (
					id TEXT PRIMARY KEY,
					space_id TEXT NOT NULL,
					channel_id TEXT NOT NULL,
					root_message_id TEXT NOT NULL,
					title TEXT NOT NULL DEFAULT '',
					status TEXT NOT NULL DEFAULT 'open',
					created_at TIMESTAMP NOT NULL,
					updated_at TIMESTAMP NOT NULL,
					FOREIGN KEY (space_id) REFERENCES collaboration_spaces(id) ON DELETE CASCADE,
					FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE
				);

				CREATE TABLE IF NOT EXISTS subscriptions (
					id TEXT PRIMARY KEY,
					space_id TEXT NOT NULL,
					participant_id TEXT NOT NULL,
					channels_json TEXT NOT NULL DEFAULT '[]',
					event_types_json TEXT NOT NULL DEFAULT '[]',
					scope_patterns_json TEXT NOT NULL DEFAULT '[]',
					mode TEXT NOT NULL DEFAULT 'active',
					created_at TIMESTAMP NOT NULL,
					FOREIGN KEY (space_id) REFERENCES collaboration_spaces(id) ON DELETE CASCADE
				);

				CREATE TABLE IF NOT EXISTS event_deliveries (
					id TEXT PRIMARY KEY,
					event_id TEXT NOT NULL,
					space_id TEXT NOT NULL,
					participant_id TEXT NOT NULL,
					status TEXT NOT NULL DEFAULT 'pending',
					attempt INTEGER NOT NULL DEFAULT 0,
					started_at TIMESTAMP,
					completed_at TIMESTAMP,
					result_message_id TEXT NOT NULL DEFAULT '',
					skip_reason TEXT NOT NULL DEFAULT '',
					FOREIGN KEY (space_id) REFERENCES collaboration_spaces(id) ON DELETE CASCADE
				);

				CREATE TABLE IF NOT EXISTS decisions (
					id TEXT PRIMARY KEY,
					space_id TEXT NOT NULL,
					title TEXT NOT NULL,
					statement TEXT NOT NULL,
					rationale TEXT NOT NULL DEFAULT '',
					evidence_refs_json TEXT NOT NULL DEFAULT '[]',
					proposed_by TEXT NOT NULL,
					accepted_by_json TEXT NOT NULL DEFAULT '[]',
					status TEXT NOT NULL DEFAULT 'proposed',
					supersedes TEXT NOT NULL DEFAULT '',
					created_at TIMESTAMP NOT NULL,
					updated_at TIMESTAMP NOT NULL,
					FOREIGN KEY (space_id) REFERENCES collaboration_spaces(id) ON DELETE CASCADE
				);

				CREATE TABLE IF NOT EXISTS participant_states (
					space_id TEXT NOT NULL,
					participant_id TEXT NOT NULL,
					mode TEXT NOT NULL DEFAULT 'active',
					last_seen_message_id TEXT NOT NULL DEFAULT '',
					last_seen_event_id TEXT NOT NULL DEFAULT '',
					updated_at TIMESTAMP NOT NULL,
					PRIMARY KEY (space_id, participant_id),
					FOREIGN KEY (space_id) REFERENCES collaboration_spaces(id) ON DELETE CASCADE
				);

				CREATE TABLE IF NOT EXISTS summaries (
					id TEXT PRIMARY KEY,
					space_id TEXT NOT NULL,
					target_type TEXT NOT NULL,
					target_id TEXT NOT NULL,
					summary_text TEXT NOT NULL,
					source_message_ids_json TEXT NOT NULL DEFAULT '[]',
					created_at TIMESTAMP NOT NULL,
					FOREIGN KEY (space_id) REFERENCES collaboration_spaces(id) ON DELETE CASCADE
				);

				ALTER TABLE messages ADD COLUMN space_id TEXT NOT NULL DEFAULT '';
				ALTER TABLE messages ADD COLUMN channel_id TEXT NOT NULL DEFAULT '';
				ALTER TABLE messages ADD COLUMN thread_id TEXT NOT NULL DEFAULT '';
				ALTER TABLE messages ADD COLUMN mentions_json TEXT NOT NULL DEFAULT '[]';
				ALTER TABLE messages ADD COLUMN reply_to TEXT NOT NULL DEFAULT '';
				ALTER TABLE messages ADD COLUMN scope_json TEXT NOT NULL DEFAULT '[]';
				ALTER TABLE messages ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '{}';

				CREATE INDEX IF NOT EXISTS idx_messages_space_channel ON messages(space_id, channel_id);
				CREATE INDEX IF NOT EXISTS idx_messages_thread ON messages(thread_id);
				CREATE INDEX IF NOT EXISTS idx_event_deliveries_event_part ON event_deliveries(event_id, participant_id);
				CREATE INDEX IF NOT EXISTS idx_subscriptions_space ON subscriptions(space_id);
				CREATE INDEX IF NOT EXISTS idx_decisions_space ON decisions(space_id);
			`,
		},
	}

	for _, m := range migrations {
		if m.version > currentVersion {
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("execute migration %d: %w", m.version, err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?);`, m.version, time.Now().UTC()); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("record migration %d: %w", m.version, err)
			}
			if err := tx.Commit(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *SQLiteStore) SaveSession(ctx context.Context, session *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	budgetJSON, err := json.Marshal(session.Budget)
	if err != nil {
		return err
	}
	metaJSON, err := json.Marshal(session.Metadata)
	if err != nil {
		return err
	}

	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now().UTC()
	}
	session.UpdatedAt = time.Now().UTC()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO sessions (id, repo_root, task, status, stop_reason, writer_participant, created_at, updated_at, budget_json, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status,
			stop_reason = excluded.stop_reason,
			writer_participant = excluded.writer_participant,
			updated_at = excluded.updated_at,
			budget_json = excluded.budget_json,
			metadata_json = excluded.metadata_json;
	`, session.ID, session.RepoRoot, session.Task, session.Status, session.StopReason, session.WriterParticipant, session.CreatedAt, session.UpdatedAt, string(budgetJSON), string(metaJSON))
	if err != nil {
		return err
	}

	for _, p := range session.Participants {
		writableInt := 0
		if p.Writable {
			writableInt = 1
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO session_participants (session_id, agent_name, harness_session_id, role, writable)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(session_id, agent_name) DO UPDATE SET
				harness_session_id = excluded.harness_session_id,
				role = excluded.role,
				writable = excluded.writable;
		`, session.ID, p.AgentName, p.HarnessSessionID, p.Role, writableInt)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *SQLiteStore) GetSession(ctx context.Context, id string) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRowContext(ctx, `
		SELECT id, repo_root, task, status, stop_reason, writer_participant, created_at, updated_at, budget_json, metadata_json
		FROM sessions WHERE id = ?;
	`, id)

	var sess Session
	var budgetJSON, metaJSON string
	err := row.Scan(&sess.ID, &sess.RepoRoot, &sess.Task, &sess.Status, &sess.StopReason, &sess.WriterParticipant, &sess.CreatedAt, &sess.UpdatedAt, &budgetJSON, &metaJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &protocol.SessionNotFoundError{SessionID: id}
		}
		return nil, err
	}

	_ = json.Unmarshal([]byte(budgetJSON), &sess.Budget)
	_ = json.Unmarshal([]byte(metaJSON), &sess.Metadata)

	rows, err := s.db.QueryContext(ctx, `
		SELECT agent_name, harness_session_id, role, writable
		FROM session_participants WHERE session_id = ?;
	`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sess.Participants = make(map[string]ParticipantInfo)
	for rows.Next() {
		var p ParticipantInfo
		var writableInt int
		if err := rows.Scan(&p.AgentName, &p.HarnessSessionID, &p.Role, &writableInt); err != nil {
			return nil, err
		}
		p.Writable = writableInt == 1
		sess.Participants[p.AgentName] = p
	}

	return &sess, nil
}

func (s *SQLiteStore) ListSessions(ctx context.Context) ([]*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, repo_root, task, status, stop_reason, writer_participant, created_at, updated_at, budget_json, metadata_json
		FROM sessions ORDER BY created_at DESC;
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Session
	for rows.Next() {
		var sess Session
		var budgetJSON, metaJSON string
		if err := rows.Scan(&sess.ID, &sess.RepoRoot, &sess.Task, &sess.Status, &sess.StopReason, &sess.WriterParticipant, &sess.CreatedAt, &sess.UpdatedAt, &budgetJSON, &metaJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(budgetJSON), &sess.Budget)
		_ = json.Unmarshal([]byte(metaJSON), &sess.Metadata)
		out = append(out, &sess)
	}
	return out, nil
}

func (s *SQLiteStore) UpdateSessionStatus(ctx context.Context, id, status, stopReason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET status = ?, stop_reason = ?, updated_at = ? WHERE id = ?;
	`, status, stopReason, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &protocol.SessionNotFoundError{SessionID: id}
	}
	return nil
}

func (s *SQLiteStore) UpdateSessionBudget(ctx context.Context, id string, budget protocol.BudgetStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := json.Marshal(budget)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET budget_json = ?, updated_at = ? WHERE id = ?;
	`, string(raw), time.Now().UTC(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &protocol.SessionNotFoundError{SessionID: id}
	}
	return nil
}

func (s *SQLiteStore) UpdateParticipantHarnessSession(ctx context.Context, sessionID, agentName, harnessSessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `
		UPDATE session_participants SET harness_session_id = ? WHERE session_id = ? AND agent_name = ?;
	`, harnessSessionID, sessionID, agentName)
	return err
}

func (s *SQLiteStore) SaveMessage(ctx context.Context, env *protocol.PeerEnvelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if env.CreatedAt.IsZero() {
		env.CreatedAt = time.Now().UTC()
	}
	payloadJSON := string(env.Payload)
	if payloadJSON == "" {
		payloadJSON = "{}"
	}

	sessionID := env.SessionID
	if sessionID == "" {
		sessionID = env.SpaceID
	}
	if sessionID == "" {
		sessionID = "default_space"
	}

	mentionsJSON, _ := json.Marshal(env.Mentions)
	scopeJSON, _ := json.Marshal(env.Scope)
	metaJSON, _ := json.Marshal(env.Metadata)

	// Ensure session exists if foreign key applies
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, repo_root, task, status, stop_reason, writer_participant, created_at, updated_at, budget_json, metadata_json)
		VALUES (?, '.', 'Auto session', 'active', '', '', ?, ?, '{}', '{}')
		ON CONFLICT(id) DO NOTHING;
	`, sessionID, env.CreatedAt, env.CreatedAt)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO messages (
			id, session_id, from_agent, to_agent, type, depth,
			correlation_id, causation_id, idempotency_key, external_session_id,
			duration_ms, status, created_at, payload_json,
			space_id, channel_id, thread_id, mentions_json, reply_to, scope_json, metadata_json
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`, env.ID, sessionID, env.From, env.To, string(env.Type), env.Depth,
		env.CorrelationID, env.CausationID, env.IdempotencyKey, env.ExternalSessionID,
		env.DurationMS, env.Status, env.CreatedAt, payloadJSON,
		env.SpaceID, env.ChannelID, env.ThreadID, string(mentionsJSON), env.ReplyTo, string(scopeJSON), string(metaJSON))
	return err
}

func (s *SQLiteStore) GetMessages(ctx context.Context, sessionID string) ([]*protocol.PeerEnvelope, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, from_agent, to_agent, type, depth, correlation_id, causation_id, idempotency_key, external_session_id, duration_ms, status, created_at, payload_json, space_id, channel_id, thread_id, mentions_json, reply_to, scope_json, metadata_json
		FROM messages WHERE session_id = ? OR space_id = ? ORDER BY created_at ASC;
	`, sessionID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*protocol.PeerEnvelope
	for rows.Next() {
		var env protocol.PeerEnvelope
		var typ, payloadJSON, spID, chID, thID, mentionsJSON, replyTo, scopeJSON, metaJSON string
		if err := rows.Scan(&env.ID, &env.SessionID, &env.From, &env.To, &typ, &env.Depth, &env.CorrelationID, &env.CausationID, &env.IdempotencyKey, &env.ExternalSessionID, &env.DurationMS, &env.Status, &env.CreatedAt, &payloadJSON, &spID, &chID, &thID, &mentionsJSON, &replyTo, &scopeJSON, &metaJSON); err != nil {
			return nil, err
		}
		if spID != "" {
			env.Protocol = protocol.CollaborationProtocolV1
		} else {
			env.Protocol = protocol.PeerProtocolV1
		}
		env.Type = protocol.MessageType(typ)
		env.Payload = json.RawMessage(payloadJSON)
		env.SpaceID = spID
		env.ChannelID = chID
		env.ThreadID = thID
		env.ReplyTo = replyTo
		_ = json.Unmarshal([]byte(mentionsJSON), &env.Mentions)
		_ = json.Unmarshal([]byte(scopeJSON), &env.Scope)
		_ = json.Unmarshal([]byte(metaJSON), &env.Metadata)
		out = append(out, &env)
	}
	return out, nil
}

func (s *SQLiteStore) GetSpaceMessages(ctx context.Context, spaceID, channelID, threadID string, limit int) ([]*protocol.PeerEnvelope, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `SELECT id, session_id, from_agent, to_agent, type, depth, correlation_id, causation_id, idempotency_key, external_session_id, duration_ms, status, created_at, payload_json, space_id, channel_id, thread_id, mentions_json, reply_to, scope_json, metadata_json FROM messages WHERE (space_id = ? OR session_id = ?)`
	args := []any{spaceID, spaceID}

	if channelID != "" {
		query += " AND channel_id = ?"
		args = append(args, channelID)
	}
	if threadID != "" {
		query += " AND thread_id = ?"
		args = append(args, threadID)
	}
	query += " ORDER BY created_at ASC"
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*protocol.PeerEnvelope
	for rows.Next() {
		var env protocol.PeerEnvelope
		var typ, payloadJSON, spID, chID, thID, mentionsJSON, replyTo, scopeJSON, metaJSON string
		if err := rows.Scan(&env.ID, &env.SessionID, &env.From, &env.To, &typ, &env.Depth, &env.CorrelationID, &env.CausationID, &env.IdempotencyKey, &env.ExternalSessionID, &env.DurationMS, &env.Status, &env.CreatedAt, &payloadJSON, &spID, &chID, &thID, &mentionsJSON, &replyTo, &scopeJSON, &metaJSON); err != nil {
			return nil, err
		}
		env.Protocol = protocol.CollaborationProtocolV1
		env.Type = protocol.MessageType(typ)
		env.Payload = json.RawMessage(payloadJSON)
		env.SpaceID = spID
		env.ChannelID = chID
		env.ThreadID = thID
		env.ReplyTo = replyTo
		_ = json.Unmarshal([]byte(mentionsJSON), &env.Mentions)
		_ = json.Unmarshal([]byte(scopeJSON), &env.Scope)
		_ = json.Unmarshal([]byte(metaJSON), &env.Metadata)
		out = append(out, &env)
	}
	return out, nil
}

func (s *SQLiteStore) SaveFinding(ctx context.Context, sessionID string, f *protocol.FindingPayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if f.Timestamp.IsZero() {
		f.Timestamp = time.Now().UTC()
	}
	if f.CreatedAt.IsZero() {
		f.CreatedAt = f.Timestamp
	}
	if f.UpdatedAt.IsZero() {
		f.UpdatedAt = time.Now().UTC()
	}
	if f.Status == "" {
		f.Status = protocol.FindingOpen
	}
	if f.SourceParticipant == "" {
		f.SourceParticipant = f.SourceAgent
	}
	if f.SourceAgent == "" {
		f.SourceAgent = f.SourceParticipant
	}
	if f.Line == 0 && f.LineStart != 0 {
		f.Line = f.LineStart
	}
	if f.LineStart == 0 && f.Line != 0 {
		f.LineStart = f.Line
	}
	if f.EndLine == 0 && f.LineEnd != 0 {
		f.EndLine = f.LineEnd
	}
	if f.LineEnd == 0 && f.EndLine != 0 {
		f.LineEnd = f.EndLine
	}

	relJSON, _ := json.Marshal(f.RelatedFindings)
	evRefsJSON, _ := json.Marshal(f.EvidenceRefs)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO findings (session_id, id, source_agent, severity, category, file, line, end_line, claim, evidence, recommendation, status, timestamp, duplicate_of, related_findings_json, source_participant, source_adapter, evidence_refs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, id) DO UPDATE SET
			severity = excluded.severity,
			category = excluded.category,
			file = excluded.file,
			line = excluded.line,
			end_line = excluded.end_line,
			claim = excluded.claim,
			evidence = excluded.evidence,
			recommendation = excluded.recommendation,
			status = excluded.status,
			timestamp = excluded.timestamp,
			duplicate_of = excluded.duplicate_of,
			related_findings_json = excluded.related_findings_json,
			source_participant = excluded.source_participant,
			source_adapter = excluded.source_adapter,
			evidence_refs_json = excluded.evidence_refs_json;
	`, sessionID, f.ID, f.SourceAgent, f.Severity, f.Category, f.File, f.Line, f.EndLine, f.Claim, f.Evidence, f.Recommendation, string(f.Status), f.Timestamp, f.DuplicateOf, string(relJSON), f.SourceParticipant, f.SourceAdapter, string(evRefsJSON))
	return err
}

func (s *SQLiteStore) GetFindings(ctx context.Context, sessionID string) ([]protocol.FindingPayload, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source_agent, severity, category, file, line, end_line, claim, evidence, recommendation, status, timestamp, duplicate_of, related_findings_json, source_participant, source_adapter, evidence_refs_json
		FROM findings WHERE session_id = ? ORDER BY timestamp ASC;
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.FindingPayload
	for rows.Next() {
		var f protocol.FindingPayload
		var st, dupOf, relJSON, srcPart, srcAdap, evRefsJSON string
		if err := rows.Scan(&f.ID, &f.SourceAgent, &f.Severity, &f.Category, &f.File, &f.Line, &f.EndLine, &f.Claim, &f.Evidence, &f.Recommendation, &st, &f.Timestamp, &dupOf, &relJSON, &srcPart, &srcAdap, &evRefsJSON); err != nil {
			return nil, err
		}
		f.SessionID = sessionID
		f.Status = protocol.FindingStatus(st)
		f.DuplicateOf = dupOf
		f.SourceParticipant = srcPart
		if f.SourceParticipant == "" {
			f.SourceParticipant = f.SourceAgent
		}
		f.SourceAdapter = srcAdap
		f.LineStart = f.Line
		f.LineEnd = f.EndLine
		f.CreatedAt = f.Timestamp
		_ = json.Unmarshal([]byte(relJSON), &f.RelatedFindings)
		_ = json.Unmarshal([]byte(evRefsJSON), &f.EvidenceRefs)
		out = append(out, f)
	}
	return out, nil
}

func (s *SQLiteStore) GetFinding(ctx context.Context, sessionID, findingID string) (*protocol.FindingPayload, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRowContext(ctx, `
		SELECT id, source_agent, severity, category, file, line, end_line, claim, evidence, recommendation, status, timestamp, duplicate_of, related_findings_json, source_participant, source_adapter, evidence_refs_json
		FROM findings WHERE session_id = ? AND id = ?;
	`, sessionID, findingID)

	var f protocol.FindingPayload
	var st, dupOf, relJSON, srcPart, srcAdap, evRefsJSON string
	if err := row.Scan(&f.ID, &f.SourceAgent, &f.Severity, &f.Category, &f.File, &f.Line, &f.EndLine, &f.Claim, &f.Evidence, &f.Recommendation, &st, &f.Timestamp, &dupOf, &relJSON, &srcPart, &srcAdap, &evRefsJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	f.SessionID = sessionID
	f.Status = protocol.FindingStatus(st)
	f.DuplicateOf = dupOf
	f.SourceParticipant = srcPart
	if f.SourceParticipant == "" {
		f.SourceParticipant = f.SourceAgent
	}
	f.SourceAdapter = srcAdap
	f.LineStart = f.Line
	f.LineEnd = f.EndLine
	f.CreatedAt = f.Timestamp
	_ = json.Unmarshal([]byte(relJSON), &f.RelatedFindings)
	_ = json.Unmarshal([]byte(evRefsJSON), &f.EvidenceRefs)
	return &f, nil
}

func (s *SQLiteStore) UpdateFindingStatus(ctx context.Context, sessionID, findingID string, status protocol.FindingStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx, `
		UPDATE findings SET status = ? WHERE session_id = ? AND id = ?;
	`, string(status), sessionID, findingID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("finding %q in session %q not found", findingID, sessionID)
	}
	return nil
}

func (s *SQLiteStore) SaveEvidence(ctx context.Context, sessionID string, ev *protocol.EvidencePayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now().UTC()
	}
	metaJSON, _ := json.Marshal(ev.Metadata)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO evidence (id, session_id, finding_id, message_id, source_agent, evidence_type, command, result, excerpt, exit_code, created_at, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			finding_id = excluded.finding_id,
			message_id = excluded.message_id,
			command = excluded.command,
			result = excluded.result,
			excerpt = excluded.excerpt,
			exit_code = excluded.exit_code,
			metadata_json = excluded.metadata_json;
	`, ev.ID, sessionID, ev.FindingID, ev.MessageID, ev.SourceAgent, string(ev.Type), ev.Command, ev.Result, ev.Excerpt, ev.ExitCode, ev.CreatedAt, string(metaJSON))
	return err
}

func (s *SQLiteStore) GetEvidence(ctx context.Context, sessionID string) ([]protocol.EvidencePayload, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, finding_id, message_id, source_agent, evidence_type, command, result, excerpt, exit_code, created_at, metadata_json
		FROM evidence WHERE session_id = ? ORDER BY created_at ASC;
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.EvidencePayload
	for rows.Next() {
		var ev protocol.EvidencePayload
		var typ, metaJSON string
		if err := rows.Scan(&ev.ID, &ev.FindingID, &ev.MessageID, &ev.SourceAgent, &typ, &ev.Command, &ev.Result, &ev.Excerpt, &ev.ExitCode, &ev.CreatedAt, &metaJSON); err != nil {
			return nil, err
		}
		ev.Type = protocol.EvidenceType(typ)
		_ = json.Unmarshal([]byte(metaJSON), &ev.Metadata)
		out = append(out, ev)
	}
	return out, nil
}

func (s *SQLiteStore) GetEvidenceByFinding(ctx context.Context, sessionID, findingID string) ([]protocol.EvidencePayload, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, finding_id, message_id, source_agent, evidence_type, command, result, excerpt, exit_code, created_at, metadata_json
		FROM evidence WHERE session_id = ? AND finding_id = ? ORDER BY created_at ASC;
	`, sessionID, findingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.EvidencePayload
	for rows.Next() {
		var ev protocol.EvidencePayload
		var typ, metaJSON string
		if err := rows.Scan(&ev.ID, &ev.FindingID, &ev.MessageID, &ev.SourceAgent, &typ, &ev.Command, &ev.Result, &ev.Excerpt, &ev.ExitCode, &ev.CreatedAt, &metaJSON); err != nil {
			return nil, err
		}
		ev.Type = protocol.EvidenceType(typ)
		_ = json.Unmarshal([]byte(metaJSON), &ev.Metadata)
		out = append(out, ev)
	}
	return out, nil
}

func (s *SQLiteStore) SaveChallenge(ctx context.Context, sessionID string, ch *protocol.ChallengePayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if ch.CreatedAt.IsZero() {
		ch.CreatedAt = time.Now().UTC()
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO challenges (id, session_id, finding_id, challenger, claim, evidence, requested_verification, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?);
	`, ch.ID, sessionID, ch.FindingID, ch.Challenger, ch.Claim, ch.Evidence, ch.RequestedVerification, ch.CreatedAt)
	if err != nil {
		return err
	}

	// Formal disagreement marks the finding as disputed
	_, err = tx.ExecContext(ctx, `
		UPDATE findings SET status = ? WHERE session_id = ? AND id = ?;
	`, string(protocol.FindingDisputed), sessionID, ch.FindingID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (s *SQLiteStore) GetChallenges(ctx context.Context, sessionID string) ([]protocol.ChallengePayload, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, finding_id, challenger, claim, evidence, requested_verification, created_at
		FROM challenges WHERE session_id = ? ORDER BY created_at ASC;
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.ChallengePayload
	for rows.Next() {
		var ch protocol.ChallengePayload
		if err := rows.Scan(&ch.ID, &ch.FindingID, &ch.Challenger, &ch.Claim, &ch.Evidence, &ch.RequestedVerification, &ch.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, nil
}

func (s *SQLiteStore) SaveResolution(ctx context.Context, sessionID string, res *protocol.ResolutionPayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if res.Timestamp.IsZero() {
		res.Timestamp = time.Now().UTC()
	}
	refsJSON, _ := json.Marshal(res.EvidenceReferences)

	_, err = tx.ExecContext(ctx, `
		INSERT INTO resolutions (id, session_id, finding_id, status, rationale, resolving_agent, evidence_refs_json, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?);
	`, res.ID, sessionID, res.FindingID, string(res.Status), res.Rationale, res.ResolvingAgent, string(refsJSON), res.Timestamp)
	if err != nil {
		return err
	}

	// Update finding status according to resolution
	var findingStatus protocol.FindingStatus
	switch res.Status {
	case protocol.ResolutionConfirmed:
		findingStatus = protocol.FindingAcknowledged
	case protocol.ResolutionRejected:
		findingStatus = protocol.FindingDismissed
	case protocol.ResolutionPartiallyConfirmed:
		findingStatus = protocol.FindingAcknowledged
	case protocol.ResolutionSuperseded:
		findingStatus = protocol.FindingResolved
	case protocol.ResolutionRequiresHuman:
		findingStatus = protocol.FindingDisputed
	default:
		findingStatus = protocol.FindingResolved
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE findings SET status = ? WHERE session_id = ? AND id = ?;
	`, string(findingStatus), sessionID, res.FindingID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (s *SQLiteStore) GetResolutions(ctx context.Context, sessionID string) ([]protocol.ResolutionPayload, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, finding_id, status, rationale, resolving_agent, evidence_refs_json, timestamp
		FROM resolutions WHERE session_id = ? ORDER BY timestamp ASC;
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.ResolutionPayload
	for rows.Next() {
		var res protocol.ResolutionPayload
		var st, refsJSON string
		if err := rows.Scan(&res.ID, &res.FindingID, &st, &res.Rationale, &res.ResolvingAgent, &refsJSON, &res.Timestamp); err != nil {
			return nil, err
		}
		res.Status = protocol.ResolutionStatus(st)
		_ = json.Unmarshal([]byte(refsJSON), &res.EvidenceReferences)
		out = append(out, res)
	}
	return out, nil
}

func (s *SQLiteStore) SaveIdempotency(ctx context.Context, key, sessionID, resultJSON string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO idempotency_records (key, session_id, result_json, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET result_json = excluded.result_json;
	`, key, sessionID, resultJSON, time.Now().UTC())
	return err
}

func (s *SQLiteStore) GetIdempotency(ctx context.Context, key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRowContext(ctx, `SELECT result_json FROM idempotency_records WHERE key = ?;`, key)
	var resultJSON string
	if err := row.Scan(&resultJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return resultJSON, nil
}

func (s *SQLiteStore) EmitEvent(ctx context.Context, sessionID, eventType string, payload any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, _ := json.Marshal(payload)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO events (session_id, event_type, timestamp, payload_json)
		VALUES (?, ?, ?, ?);
	`, sessionID, eventType, time.Now().UTC(), string(raw))
	return err
}

func (s *SQLiteStore) GetEvents(ctx context.Context, sessionID string) ([]EventRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, event_type, timestamp, payload_json
		FROM events WHERE session_id = ? ORDER BY id ASC;
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EventRecord
	for rows.Next() {
		var ev EventRecord
		var payloadJSON string
		if err := rows.Scan(&ev.ID, &ev.SessionID, &ev.EventType, &ev.Timestamp, &payloadJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(payloadJSON), &ev.Payload)
		out = append(out, ev)
	}
	return out, nil
}

func (s *SQLiteStore) SaveRoutingDecision(ctx context.Context, r *RoutingDecisionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	eligJSON, _ := json.Marshal(r.Eligible)
	reasonJSON, _ := json.Marshal(r.Reason)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO routing_decisions (id, session_id, requested_capability, task_tier, eligible_json, selected, reason_json, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?);
	`, r.ID, r.SessionID, r.RequestedCapability, r.TaskTier, string(eligJSON), r.Selected, string(reasonJSON), r.Timestamp)
	return err
}

func (s *SQLiteStore) GetRoutingDecisions(ctx context.Context, sessionID string) ([]RoutingDecisionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, requested_capability, task_tier, eligible_json, selected, reason_json, timestamp
		FROM routing_decisions WHERE session_id = ? ORDER BY timestamp ASC;
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RoutingDecisionRecord
	for rows.Next() {
		var r RoutingDecisionRecord
		var eligJSON, reasonJSON string
		if err := rows.Scan(&r.ID, &r.SessionID, &r.RequestedCapability, &r.TaskTier, &eligJSON, &r.Selected, &reasonJSON, &r.Timestamp); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(eligJSON), &r.Eligible)
		_ = json.Unmarshal([]byte(reasonJSON), &r.Reason)
		out = append(out, r)
	}
	return out, nil
}

func (s *SQLiteStore) SaveUsage(ctx context.Context, u *UsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO usage_records (id, session_id, participant, adapter, model, model_routing_backend, route, input_tokens, output_tokens, cached_tokens, duration_ms, monetary_cost_usd, escalation_count, selection_reason, cost_source, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`, u.ID, u.SessionID, u.Participant, u.Adapter, u.Model, u.ModelRoutingBackend, u.Route, u.InputTokens, u.OutputTokens, u.CachedTokens, u.DurationMS, u.MonetaryCostUSD, u.EscalationCount, u.SelectionReason, u.CostSource, u.Timestamp)
	return err
}

func (s *SQLiteStore) GetUsage(ctx context.Context, sessionID string) ([]UsageRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, participant, adapter, model, model_routing_backend, route, input_tokens, output_tokens, cached_tokens, duration_ms, monetary_cost_usd, escalation_count, selection_reason, cost_source, timestamp
		FROM usage_records WHERE session_id = ? ORDER BY timestamp ASC;
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []UsageRecord
	for rows.Next() {
		var u UsageRecord
		if err := rows.Scan(&u.ID, &u.SessionID, &u.Participant, &u.Adapter, &u.Model, &u.ModelRoutingBackend, &u.Route, &u.InputTokens, &u.OutputTokens, &u.CachedTokens, &u.DurationMS, &u.MonetaryCostUSD, &u.EscalationCount, &u.SelectionReason, &u.CostSource, &u.Timestamp); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

func (s *SQLiteStore) SaveSpace(ctx context.Context, space *protocol.CollaborationSpace) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	if space.CreatedAt.IsZero() {
		space.CreatedAt = now
	}
	space.UpdatedAt = now

	metaJSON, err := json.Marshal(space.Metadata)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO collaboration_spaces (id, workspace_id, title, purpose, lifecycle_state, writer_participant, budget_json, metadata_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, '{}', ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			workspace_id = excluded.workspace_id,
			title = excluded.title,
			purpose = excluded.purpose,
			lifecycle_state = excluded.lifecycle_state,
			writer_participant = excluded.writer_participant,
			metadata_json = excluded.metadata_json,
			updated_at = excluded.updated_at;
	`, space.ID, space.WorkspaceID, space.Title, space.Purpose, string(space.LifecycleState), space.WriterParticipant, string(metaJSON), space.CreatedAt, space.UpdatedAt)
	if err != nil {
		return err
	}

	// Mirror to sessions table for backward compatibility
	_, _ = tx.ExecContext(ctx, `
		INSERT INTO sessions (id, repo_root, task, status, stop_reason, writer_participant, created_at, updated_at, budget_json, metadata_json)
		VALUES (?, ?, ?, ?, '', ?, ?, ?, '{}', ?)
		ON CONFLICT(id) DO UPDATE SET
			repo_root = excluded.repo_root,
			task = excluded.task,
			status = excluded.status,
			writer_participant = excluded.writer_participant,
			updated_at = excluded.updated_at,
			metadata_json = excluded.metadata_json;
	`, space.ID, space.WorkspaceID, space.Purpose, string(space.LifecycleState), space.WriterParticipant, space.CreatedAt, space.UpdatedAt, string(metaJSON))

	// Save participants
	for _, p := range space.Participants {
		rolesJSON, _ := json.Marshal(p.Roles)
		capsJSON, _ := json.Marshal(p.Capabilities)
		writableInt := 0
		if p.Writable {
			writableInt = 1
		}
		if p.JoinedAt.IsZero() {
			p.JoinedAt = now
		}
		p.UpdatedAt = now

		_, err = tx.ExecContext(ctx, `
			INSERT INTO space_participants (space_id, participant_id, adapter, roles_json, capabilities_json, mode, writable, joined_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(space_id, participant_id) DO UPDATE SET
				adapter = excluded.adapter,
				roles_json = excluded.roles_json,
				capabilities_json = excluded.capabilities_json,
				mode = excluded.mode,
				writable = excluded.writable,
				updated_at = excluded.updated_at;
		`, space.ID, p.ID, p.Adapter, string(rolesJSON), string(capsJSON), string(p.Mode), writableInt, p.JoinedAt, p.UpdatedAt)
		if err != nil {
			return err
		}
	}

	// Save channels
	for _, ch := range space.Channels {
		allowedPartsJSON, _ := json.Marshal(ch.AllowedParticipants)
		allowedCapsJSON, _ := json.Marshal(ch.AllowedCapabilities)
		if ch.CreatedAt.IsZero() {
			ch.CreatedAt = now
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO channels (id, space_id, name, description, visibility, allowed_participants_json, allowed_capabilities_json, created_by, created_at, archived_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				name = excluded.name,
				description = excluded.description,
				visibility = excluded.visibility,
				allowed_participants_json = excluded.allowed_participants_json,
				allowed_capabilities_json = excluded.allowed_capabilities_json,
				archived_at = excluded.archived_at;
		`, ch.ID, space.ID, ch.Name, ch.Description, string(ch.Visibility), string(allowedPartsJSON), string(allowedCapsJSON), ch.CreatedBy, ch.CreatedAt, ch.ArchivedAt)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *SQLiteStore) GetSpace(ctx context.Context, id string) (*protocol.CollaborationSpace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRowContext(ctx, `
		SELECT id, workspace_id, title, purpose, lifecycle_state, writer_participant, metadata_json, created_at, updated_at
		FROM collaboration_spaces WHERE id = ?;
	`, id)

	var space protocol.CollaborationSpace
	var stateStr, metaJSON string
	err := row.Scan(&space.ID, &space.WorkspaceID, &space.Title, &space.Purpose, &stateStr, &space.WriterParticipant, &metaJSON, &space.CreatedAt, &space.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &protocol.SpaceNotFoundError{SpaceID: id}
		}
		return nil, err
	}
	space.LifecycleState = protocol.SpaceLifecycleState(stateStr)
	_ = json.Unmarshal([]byte(metaJSON), &space.Metadata)

	// Fetch participants
	pRows, err := s.db.QueryContext(ctx, `
		SELECT participant_id, adapter, roles_json, capabilities_json, mode, writable, joined_at, updated_at
		FROM space_participants WHERE space_id = ?;
	`, id)
	if err != nil {
		return nil, err
	}
	defer pRows.Close()

	space.Participants = make(map[string]protocol.SpaceParticipant)
	for pRows.Next() {
		var p protocol.SpaceParticipant
		var rolesJSON, capsJSON, modeStr string
		var writableInt int
		if err := pRows.Scan(&p.ID, &p.Adapter, &rolesJSON, &capsJSON, &modeStr, &writableInt, &p.JoinedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.Mode = protocol.ParticipantActivityMode(modeStr)
		p.Writable = writableInt == 1
		_ = json.Unmarshal([]byte(rolesJSON), &p.Roles)
		_ = json.Unmarshal([]byte(capsJSON), &p.Capabilities)
		space.Participants[p.ID] = p
	}

	// Fetch channels
	cRows, err := s.db.QueryContext(ctx, `
		SELECT id, space_id, name, description, visibility, allowed_participants_json, allowed_capabilities_json, created_by, created_at, archived_at
		FROM channels WHERE space_id = ?;
	`, id)
	if err != nil {
		return nil, err
	}
	defer cRows.Close()

	space.Channels = make(map[string]protocol.Channel)
	for cRows.Next() {
		var ch protocol.Channel
		var visStr, allowedPartsJSON, allowedCapsJSON string
		if err := cRows.Scan(&ch.ID, &ch.SpaceID, &ch.Name, &ch.Description, &visStr, &allowedPartsJSON, &allowedCapsJSON, &ch.CreatedBy, &ch.CreatedAt, &ch.ArchivedAt); err != nil {
			return nil, err
		}
		ch.Visibility = protocol.ChannelVisibility(visStr)
		_ = json.Unmarshal([]byte(allowedPartsJSON), &ch.AllowedParticipants)
		_ = json.Unmarshal([]byte(allowedCapsJSON), &ch.AllowedCapabilities)
		space.Channels[ch.ID] = ch
	}

	return &space, nil
}

func (s *SQLiteStore) ListSpaces(ctx context.Context) ([]*protocol.CollaborationSpace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, workspace_id, title, purpose, lifecycle_state, writer_participant, metadata_json, created_at, updated_at
		FROM collaboration_spaces ORDER BY created_at DESC;
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*protocol.CollaborationSpace
	for rows.Next() {
		var space protocol.CollaborationSpace
		var stateStr, metaJSON string
		if err := rows.Scan(&space.ID, &space.WorkspaceID, &space.Title, &space.Purpose, &stateStr, &space.WriterParticipant, &metaJSON, &space.CreatedAt, &space.UpdatedAt); err != nil {
			return nil, err
		}
		space.LifecycleState = protocol.SpaceLifecycleState(stateStr)
		_ = json.Unmarshal([]byte(metaJSON), &space.Metadata)
		out = append(out, &space)
	}

	// Populate participants and channels for each space
	for _, sp := range out {
		pRows, err := s.db.QueryContext(ctx, `
			SELECT participant_id, adapter, roles_json, capabilities_json, mode, writable, joined_at, updated_at
			FROM space_participants WHERE space_id = ?;
		`, sp.ID)
		if err == nil {
			sp.Participants = make(map[string]protocol.SpaceParticipant)
			for pRows.Next() {
				var p protocol.SpaceParticipant
				var rolesJSON, capsJSON, modeStr string
				var writableInt int
				if err := pRows.Scan(&p.ID, &p.Adapter, &rolesJSON, &capsJSON, &modeStr, &writableInt, &p.JoinedAt, &p.UpdatedAt); err == nil {
					p.Mode = protocol.ParticipantActivityMode(modeStr)
					p.Writable = writableInt == 1
					_ = json.Unmarshal([]byte(rolesJSON), &p.Roles)
					_ = json.Unmarshal([]byte(capsJSON), &p.Capabilities)
					sp.Participants[p.ID] = p
				}
			}
			pRows.Close()
		}

		cRows, err := s.db.QueryContext(ctx, `
			SELECT id, space_id, name, description, visibility, allowed_participants_json, allowed_capabilities_json, created_by, created_at, archived_at
			FROM channels WHERE space_id = ?;
		`, sp.ID)
		if err == nil {
			sp.Channels = make(map[string]protocol.Channel)
			for cRows.Next() {
				var ch protocol.Channel
				var visStr, allowedPartsJSON, allowedCapsJSON string
				if err := cRows.Scan(&ch.ID, &ch.SpaceID, &ch.Name, &ch.Description, &visStr, &allowedPartsJSON, &allowedCapsJSON, &ch.CreatedBy, &ch.CreatedAt, &ch.ArchivedAt); err == nil {
					ch.Visibility = protocol.ChannelVisibility(visStr)
					_ = json.Unmarshal([]byte(allowedPartsJSON), &ch.AllowedParticipants)
					_ = json.Unmarshal([]byte(allowedCapsJSON), &ch.AllowedCapabilities)
					sp.Channels[ch.ID] = ch
				}
			}
			cRows.Close()
		}
	}

	return out, nil
}

func (s *SQLiteStore) UpdateSpaceLifecycle(ctx context.Context, id string, state protocol.SpaceLifecycleState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
		UPDATE collaboration_spaces SET lifecycle_state = ?, updated_at = ? WHERE id = ?;
	`, string(state), now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &protocol.SpaceNotFoundError{SpaceID: id}
	}

	_, _ = s.db.ExecContext(ctx, `
		UPDATE sessions SET status = ?, updated_at = ? WHERE id = ?;
	`, string(state), now, id)

	return nil
}

func (s *SQLiteStore) AddSpaceParticipant(ctx context.Context, spaceID string, p *protocol.SpaceParticipant) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if p.JoinedAt.IsZero() {
		p.JoinedAt = now
	}
	p.UpdatedAt = now

	rolesJSON, _ := json.Marshal(p.Roles)
	capsJSON, _ := json.Marshal(p.Capabilities)
	writableInt := 0
	if p.Writable {
		writableInt = 1
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO space_participants (space_id, participant_id, adapter, roles_json, capabilities_json, mode, writable, joined_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(space_id, participant_id) DO UPDATE SET
			adapter = excluded.adapter,
			roles_json = excluded.roles_json,
			capabilities_json = excluded.capabilities_json,
			mode = excluded.mode,
			writable = excluded.writable,
			updated_at = excluded.updated_at;
	`, spaceID, p.ID, p.Adapter, string(rolesJSON), string(capsJSON), string(p.Mode), writableInt, p.JoinedAt, p.UpdatedAt)
	return err
}

func (s *SQLiteStore) UpdateParticipantMode(ctx context.Context, spaceID, participantID string, mode protocol.ParticipantActivityMode) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
		UPDATE space_participants SET mode = ?, updated_at = ? WHERE space_id = ? AND participant_id = ?;
	`, string(mode), now, spaceID, participantID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("participant %q in space %q not found", participantID, spaceID)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO participant_states (space_id, participant_id, mode, last_seen_message_id, last_seen_event_id, updated_at)
		VALUES (?, ?, ?, '', '', ?)
		ON CONFLICT(space_id, participant_id) DO UPDATE SET
			mode = excluded.mode,
			updated_at = excluded.updated_at;
	`, spaceID, participantID, string(mode), now)
	return err
}

func (s *SQLiteStore) CreateChannel(ctx context.Context, ch *protocol.Channel) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if ch.CreatedAt.IsZero() {
		ch.CreatedAt = now
	}
	allowedPartsJSON, _ := json.Marshal(ch.AllowedParticipants)
	allowedCapsJSON, _ := json.Marshal(ch.AllowedCapabilities)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO channels (id, space_id, name, description, visibility, allowed_participants_json, allowed_capabilities_json, created_by, created_at, archived_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			visibility = excluded.visibility,
			allowed_participants_json = excluded.allowed_participants_json,
			allowed_capabilities_json = excluded.allowed_capabilities_json,
			archived_at = excluded.archived_at;
	`, ch.ID, ch.SpaceID, ch.Name, ch.Description, string(ch.Visibility), string(allowedPartsJSON), string(allowedCapsJSON), ch.CreatedBy, ch.CreatedAt, ch.ArchivedAt)
	return err
}

func (s *SQLiteStore) GetChannel(ctx context.Context, spaceID, channelID string) (*protocol.Channel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRowContext(ctx, `
		SELECT id, space_id, name, description, visibility, allowed_participants_json, allowed_capabilities_json, created_by, created_at, archived_at
		FROM channels WHERE space_id = ? AND id = ?;
	`, spaceID, channelID)

	var ch protocol.Channel
	var visStr, allowedPartsJSON, allowedCapsJSON string
	err := row.Scan(&ch.ID, &ch.SpaceID, &ch.Name, &ch.Description, &visStr, &allowedPartsJSON, &allowedCapsJSON, &ch.CreatedBy, &ch.CreatedAt, &ch.ArchivedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &protocol.ChannelNotFoundError{SpaceID: spaceID, Channel: channelID}
		}
		return nil, err
	}
	ch.Visibility = protocol.ChannelVisibility(visStr)
	_ = json.Unmarshal([]byte(allowedPartsJSON), &ch.AllowedParticipants)
	_ = json.Unmarshal([]byte(allowedCapsJSON), &ch.AllowedCapabilities)
	return &ch, nil
}

func (s *SQLiteStore) ListChannels(ctx context.Context, spaceID string) ([]protocol.Channel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, space_id, name, description, visibility, allowed_participants_json, allowed_capabilities_json, created_by, created_at, archived_at
		FROM channels WHERE space_id = ? ORDER BY created_at ASC;
	`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.Channel
	for rows.Next() {
		var ch protocol.Channel
		var visStr, allowedPartsJSON, allowedCapsJSON string
		if err := rows.Scan(&ch.ID, &ch.SpaceID, &ch.Name, &ch.Description, &visStr, &allowedPartsJSON, &allowedCapsJSON, &ch.CreatedBy, &ch.CreatedAt, &ch.ArchivedAt); err != nil {
			return nil, err
		}
		ch.Visibility = protocol.ChannelVisibility(visStr)
		_ = json.Unmarshal([]byte(allowedPartsJSON), &ch.AllowedParticipants)
		_ = json.Unmarshal([]byte(allowedCapsJSON), &ch.AllowedCapabilities)
		out = append(out, ch)
	}
	return out, nil
}

func (s *SQLiteStore) CreateThread(ctx context.Context, th *protocol.Thread) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if th.CreatedAt.IsZero() {
		th.CreatedAt = now
	}
	th.UpdatedAt = now

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO threads (id, space_id, channel_id, root_message_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title = excluded.title,
			status = excluded.status,
			updated_at = excluded.updated_at;
	`, th.ID, th.SpaceID, th.ChannelID, th.RootMessageID, th.Title, string(th.Status), th.CreatedAt, th.UpdatedAt)
	return err
}

func (s *SQLiteStore) GetThread(ctx context.Context, threadID string) (*protocol.Thread, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRowContext(ctx, `
		SELECT id, space_id, channel_id, root_message_id, title, status, created_at, updated_at
		FROM threads WHERE id = ?;
	`, threadID)

	var th protocol.Thread
	var stStr string
	err := row.Scan(&th.ID, &th.SpaceID, &th.ChannelID, &th.RootMessageID, &th.Title, &stStr, &th.CreatedAt, &th.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &protocol.ThreadNotFoundError{ThreadID: threadID}
		}
		return nil, err
	}
	th.Status = protocol.ThreadStatus(stStr)
	return &th, nil
}

func (s *SQLiteStore) ListThreads(ctx context.Context, spaceID, channelID string) ([]protocol.Thread, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `SELECT id, space_id, channel_id, root_message_id, title, status, created_at, updated_at FROM threads WHERE space_id = ?`
	args := []any{spaceID}
	if channelID != "" {
		query += ` AND channel_id = ?`
		args = append(args, channelID)
	}
	query += ` ORDER BY created_at ASC;`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.Thread
	for rows.Next() {
		var th protocol.Thread
		var stStr string
		if err := rows.Scan(&th.ID, &th.SpaceID, &th.ChannelID, &th.RootMessageID, &th.Title, &stStr, &th.CreatedAt, &th.UpdatedAt); err != nil {
			return nil, err
		}
		th.Status = protocol.ThreadStatus(stStr)
		out = append(out, th)
	}
	return out, nil
}

func (s *SQLiteStore) SaveSubscription(ctx context.Context, sub *protocol.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if sub.CreatedAt.IsZero() {
		sub.CreatedAt = now
	}
	channelsJSON, _ := json.Marshal(sub.Channels)
	eventTypesJSON, _ := json.Marshal(sub.EventTypes)
	scopeJSON, _ := json.Marshal(sub.ScopePatterns)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO subscriptions (id, space_id, participant_id, channels_json, event_types_json, scope_patterns_json, mode, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			channels_json = excluded.channels_json,
			event_types_json = excluded.event_types_json,
			scope_patterns_json = excluded.scope_patterns_json,
			mode = excluded.mode;
	`, sub.ID, sub.SpaceID, sub.ParticipantID, string(channelsJSON), string(eventTypesJSON), string(scopeJSON), string(sub.Mode), sub.CreatedAt)
	return err
}

func (s *SQLiteStore) GetSubscriptions(ctx context.Context, spaceID string) ([]protocol.Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, space_id, participant_id, channels_json, event_types_json, scope_patterns_json, mode, created_at
		FROM subscriptions WHERE space_id = ? ORDER BY created_at ASC;
	`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.Subscription
	for rows.Next() {
		var sub protocol.Subscription
		var chJSON, evJSON, scJSON, modeStr string
		if err := rows.Scan(&sub.ID, &sub.SpaceID, &sub.ParticipantID, &chJSON, &evJSON, &scJSON, &modeStr, &sub.CreatedAt); err != nil {
			return nil, err
		}
		sub.Mode = protocol.ParticipantActivityMode(modeStr)
		_ = json.Unmarshal([]byte(chJSON), &sub.Channels)
		_ = json.Unmarshal([]byte(evJSON), &sub.EventTypes)
		_ = json.Unmarshal([]byte(scJSON), &sub.ScopePatterns)
		out = append(out, sub)
	}
	return out, nil
}

func (s *SQLiteStore) GetParticipantSubscriptions(ctx context.Context, spaceID, participantID string) ([]protocol.Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, space_id, participant_id, channels_json, event_types_json, scope_patterns_json, mode, created_at
		FROM subscriptions WHERE space_id = ? AND participant_id = ? ORDER BY created_at ASC;
	`, spaceID, participantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.Subscription
	for rows.Next() {
		var sub protocol.Subscription
		var chJSON, evJSON, scJSON, modeStr string
		if err := rows.Scan(&sub.ID, &sub.SpaceID, &sub.ParticipantID, &chJSON, &evJSON, &scJSON, &modeStr, &sub.CreatedAt); err != nil {
			return nil, err
		}
		sub.Mode = protocol.ParticipantActivityMode(modeStr)
		_ = json.Unmarshal([]byte(chJSON), &sub.Channels)
		_ = json.Unmarshal([]byte(evJSON), &sub.EventTypes)
		_ = json.Unmarshal([]byte(scJSON), &sub.ScopePatterns)
		out = append(out, sub)
	}
	return out, nil
}

func (s *SQLiteStore) DeleteSubscription(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `DELETE FROM subscriptions WHERE id = ?;`, id)
	return err
}

func (s *SQLiteStore) RecordEventDelivery(ctx context.Context, d *protocol.EventDelivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO event_deliveries (id, event_id, space_id, participant_id, status, attempt, started_at, completed_at, result_message_id, skip_reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status,
			attempt = excluded.attempt,
			started_at = excluded.started_at,
			completed_at = excluded.completed_at,
			result_message_id = excluded.result_message_id,
			skip_reason = excluded.skip_reason;
	`, d.ID, d.EventID, d.SpaceID, d.ParticipantID, string(d.Status), d.Attempt, d.StartedAt, d.CompletedAt, d.ResultMessageID, d.SkipReason)
	return err
}

func (s *SQLiteStore) GetEventDelivery(ctx context.Context, eventID, participantID string) (*protocol.EventDelivery, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRowContext(ctx, `
		SELECT id, event_id, space_id, participant_id, status, attempt, started_at, completed_at, result_message_id, skip_reason
		FROM event_deliveries WHERE event_id = ? AND participant_id = ?;
	`, eventID, participantID)

	var d protocol.EventDelivery
	var stStr string
	err := row.Scan(&d.ID, &d.EventID, &d.SpaceID, &d.ParticipantID, &stStr, &d.Attempt, &d.StartedAt, &d.CompletedAt, &d.ResultMessageID, &d.SkipReason)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	d.Status = protocol.DeliveryStatus(stStr)
	return &d, nil
}

func (s *SQLiteStore) UpdateEventDeliveryStatus(ctx context.Context, id string, status protocol.DeliveryStatus, resultMsgID, skipReason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		UPDATE event_deliveries
		SET status = ?, result_message_id = ?, skip_reason = ?, completed_at = ?
		WHERE id = ?;
	`, string(status), resultMsgID, skipReason, now, id)
	return err
}

func (s *SQLiteStore) SaveDecision(ctx context.Context, d *protocol.Decision) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	d.UpdatedAt = now

	evRefsJSON, _ := json.Marshal(d.EvidenceRefs)
	acceptedJSON, _ := json.Marshal(d.AcceptedBy)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO decisions (id, space_id, title, statement, rationale, evidence_refs_json, proposed_by, accepted_by_json, status, supersedes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title = excluded.title,
			statement = excluded.statement,
			rationale = excluded.rationale,
			evidence_refs_json = excluded.evidence_refs_json,
			accepted_by_json = excluded.accepted_by_json,
			status = excluded.status,
			supersedes = excluded.supersedes,
			updated_at = excluded.updated_at;
	`, d.ID, d.SpaceID, d.Title, d.Statement, d.Rationale, string(evRefsJSON), d.ProposedBy, string(acceptedJSON), string(d.Status), d.Supersedes, d.CreatedAt, d.UpdatedAt)
	return err
}

func (s *SQLiteStore) GetDecision(ctx context.Context, spaceID, id string) (*protocol.Decision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRowContext(ctx, `
		SELECT id, space_id, title, statement, rationale, evidence_refs_json, proposed_by, accepted_by_json, status, supersedes, created_at, updated_at
		FROM decisions WHERE space_id = ? AND id = ?;
	`, spaceID, id)

	var d protocol.Decision
	var evRefsJSON, acceptedJSON, stStr string
	err := row.Scan(&d.ID, &d.SpaceID, &d.Title, &d.Statement, &d.Rationale, &evRefsJSON, &d.ProposedBy, &acceptedJSON, &stStr, &d.Supersedes, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("decision %q in space %q not found", id, spaceID)
		}
		return nil, err
	}
	d.Status = protocol.DecisionStatus(stStr)
	_ = json.Unmarshal([]byte(evRefsJSON), &d.EvidenceRefs)
	_ = json.Unmarshal([]byte(acceptedJSON), &d.AcceptedBy)
	return &d, nil
}

func (s *SQLiteStore) ListDecisions(ctx context.Context, spaceID string) ([]protocol.Decision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, space_id, title, statement, rationale, evidence_refs_json, proposed_by, accepted_by_json, status, supersedes, created_at, updated_at
		FROM decisions WHERE space_id = ? ORDER BY created_at ASC;
	`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.Decision
	for rows.Next() {
		var d protocol.Decision
		var evRefsJSON, acceptedJSON, stStr string
		if err := rows.Scan(&d.ID, &d.SpaceID, &d.Title, &d.Statement, &d.Rationale, &evRefsJSON, &d.ProposedBy, &acceptedJSON, &stStr, &d.Supersedes, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		d.Status = protocol.DecisionStatus(stStr)
		_ = json.Unmarshal([]byte(evRefsJSON), &d.EvidenceRefs)
		_ = json.Unmarshal([]byte(acceptedJSON), &d.AcceptedBy)
		out = append(out, d)
	}
	return out, nil
}

func (s *SQLiteStore) UpdateParticipantCursor(ctx context.Context, cursor *protocol.ParticipantCursor) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	cursor.UpdatedAt = now

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO participant_states (space_id, participant_id, mode, last_seen_message_id, last_seen_event_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(space_id, participant_id) DO UPDATE SET
			mode = excluded.mode,
			last_seen_message_id = excluded.last_seen_message_id,
			last_seen_event_id = excluded.last_seen_event_id,
			updated_at = excluded.updated_at;
	`, cursor.SpaceID, cursor.ParticipantID, string(cursor.Mode), cursor.LastSeenMessageID, cursor.LastSeenEventID, cursor.UpdatedAt)
	return err
}

func (s *SQLiteStore) GetParticipantCursor(ctx context.Context, spaceID, participantID string) (*protocol.ParticipantCursor, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRowContext(ctx, `
		SELECT space_id, participant_id, mode, last_seen_message_id, last_seen_event_id, updated_at
		FROM participant_states WHERE space_id = ? AND participant_id = ?;
	`, spaceID, participantID)

	var c protocol.ParticipantCursor
	var modeStr string
	err := row.Scan(&c.SpaceID, &c.ParticipantID, &modeStr, &c.LastSeenMessageID, &c.LastSeenEventID, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &protocol.ParticipantCursor{
				SpaceID:       spaceID,
				ParticipantID: participantID,
				Mode:          protocol.ParticipantModeActive,
				UpdatedAt:     time.Now().UTC(),
			}, nil
		}
		return nil, err
	}
	c.Mode = protocol.ParticipantActivityMode(modeStr)
	return &c, nil
}

func (s *SQLiteStore) SaveSummary(ctx context.Context, spaceID, targetType, targetID, text string, sourceMsgIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	srcJSON, _ := json.Marshal(sourceMsgIDs)
	sumID := fmt.Sprintf("sum_%s_%d", targetID, time.Now().UnixNano())

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO summaries (id, space_id, target_type, target_id, summary_text, source_message_ids_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?);
	`, sumID, spaceID, targetType, targetID, text, string(srcJSON), time.Now().UTC())
	return err
}

func (s *SQLiteStore) GetSummaries(ctx context.Context, spaceID, targetID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT summary_text FROM summaries WHERE space_id = ? AND target_id = ? ORDER BY created_at ASC;
	`, spaceID, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, err
		}
		out = append(out, text)
	}
	return out, nil
}
