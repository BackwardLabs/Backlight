package store

const schemaDDL = `
CREATE TABLE IF NOT EXISTS cases (
	case_id              TEXT PRIMARY KEY,
	chain                TEXT NOT NULL,
	tx_hash              TEXT NOT NULL,
	source               TEXT,
	detected_at          TEXT,
	metadata             TEXT NOT NULL DEFAULT '{}',
	state                TEXT NOT NULL,
	outcome              TEXT,
	failure_kind         TEXT,
	output_root          TEXT,
	summary_json_path    TEXT,
	attempt_number       INTEGER NOT NULL,
	parent_case_id       TEXT,
	force_rerun          INTEGER NOT NULL DEFAULT 0,
	handoff_status       TEXT NOT NULL DEFAULT 'pending',
	notification_status  TEXT NOT NULL DEFAULT 'pending',
	created_at           TEXT NOT NULL,
	updated_at           TEXT NOT NULL,
	FOREIGN KEY (parent_case_id) REFERENCES cases(case_id)
);

CREATE INDEX IF NOT EXISTS idx_cases_chain_tx ON cases(chain, tx_hash, attempt_number DESC);
CREATE INDEX IF NOT EXISTS idx_cases_state    ON cases(state);
CREATE INDEX IF NOT EXISTS idx_cases_created  ON cases(created_at DESC);

CREATE TABLE IF NOT EXISTS incoming_signals (
	lumos_signal_id   TEXT PRIMARY KEY,
	incident_group_id TEXT NOT NULL,
	case_id           TEXT NOT NULL,
	chain             TEXT NOT NULL,
	tx_hash           TEXT NOT NULL,
	protocol_name     TEXT NOT NULL,
	source            TEXT NOT NULL,
	source_url        TEXT NOT NULL,
	detected_at       TEXT NOT NULL,
	metadata          TEXT NOT NULL DEFAULT '{}',
	received_at       TEXT NOT NULL,
	updated_at        TEXT NOT NULL,
	FOREIGN KEY (case_id) REFERENCES cases(case_id)
);

CREATE INDEX IF NOT EXISTS idx_incoming_signals_case     ON incoming_signals(case_id, received_at);
CREATE INDEX IF NOT EXISTS idx_incoming_signals_group    ON incoming_signals(incident_group_id, received_at);
CREATE INDEX IF NOT EXISTS idx_incoming_signals_chain_tx ON incoming_signals(chain, tx_hash, received_at);

CREATE TABLE IF NOT EXISTS case_events (
	event_id     TEXT PRIMARY KEY,
	case_id      TEXT NOT NULL,
	from_state   TEXT,
	to_state     TEXT,
	event_type   TEXT NOT NULL,
	payload      TEXT,
	occurred_at  TEXT NOT NULL,
	FOREIGN KEY (case_id) REFERENCES cases(case_id)
);

CREATE INDEX IF NOT EXISTS idx_case_events_case ON case_events(case_id, occurred_at);

CREATE TABLE IF NOT EXISTS handoff_attempts (
	attempt_id                TEXT PRIMARY KEY,
	case_id                   TEXT NOT NULL,
	target_url                TEXT NOT NULL,
	attempted_at              TEXT NOT NULL,
	http_status               INTEGER,
	result                    TEXT NOT NULL,
	error                     TEXT,
	attempt_index_for_target  INTEGER NOT NULL,
	FOREIGN KEY (case_id) REFERENCES cases(case_id)
);

CREATE INDEX IF NOT EXISTS idx_handoff_case ON handoff_attempts(case_id, attempted_at);

CREATE TABLE IF NOT EXISTS notification_attempts (
	attempt_id     TEXT PRIMARY KEY,
	case_id        TEXT NOT NULL,
	channel        TEXT NOT NULL,
	event          TEXT NOT NULL,
	attempted_at   TEXT NOT NULL,
	result         TEXT NOT NULL,
	error          TEXT,
	attempt_index  INTEGER NOT NULL,
	FOREIGN KEY (case_id) REFERENCES cases(case_id)
);

CREATE INDEX IF NOT EXISTS idx_notif_case ON notification_attempts(case_id, attempted_at);
`
