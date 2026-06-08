package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// AlarmDefinition is a row of alarm_definitions: one user-defined, threshold-driven
// alarm rule over the unified inventory/metrics. The alarms engine
// (server/internal/alarms) holds the live instance state; this is the durable
// definition. NotifyChannelIDs is stored as a JSON array (notify_channel_ids).
type AlarmDefinition struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Target           string   `json:"target"`     // vm|host|datastore
	Metric           string   `json:"metric"`     // cpu|memory|disk|storage_pct|state
	Comparator       string   `json:"comparator"` // gt|lt|eq
	Threshold        float64  `json:"threshold"`
	StateValue       string   `json:"stateValue,omitempty"`
	DurationSec      int      `json:"durationSec"`
	Severity         string   `json:"severity"` // info|warning|critical
	Enabled          bool     `json:"enabled"`
	NotifyChannelIDs []string `json:"notifyChannelIds"`
	CreatedAt        int64    `json:"createdAt"`
	UpdatedAt        int64    `json:"updatedAt"`
}

// AlarmChannel is a row of alarm_channels: a notification destination.
type AlarmChannel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"` // webhook|email-stub
	Config    string `json:"config"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// AlarmInstance is a row of alarm_instances: a durable snapshot of an ACTIVE alarm
// instance so an in-flight alarm survives a restart.
type AlarmInstance struct {
	ID             string  `json:"id"`
	DefinitionID   string  `json:"definitionId"`
	DefinitionName string  `json:"definitionName"`
	ObjectID       string  `json:"objectId"`
	ObjectName     string  `json:"objectName"`
	ObjectType     string  `json:"objectType"`
	Severity       string  `json:"severity"`
	Metric         string  `json:"metric"`
	Value          float64 `json:"value"`
	StateRaw       string  `json:"stateRaw,omitempty"`
	RaisedAt       int64   `json:"raisedAt"`
	LastNotifiedAt int64   `json:"lastNotifiedAt,omitempty"`
}

const alarmDefCols = `id, name, target, metric, comparator, threshold, state_value, ` +
	`duration_sec, severity, enabled, notify_channel_ids, created_at, updated_at`

func scanAlarmDef(row interface{ Scan(...any) error }) (*AlarmDefinition, error) {
	var d AlarmDefinition
	var enabled int
	var stateVal sql.NullString
	var chJSON string
	if err := row.Scan(&d.ID, &d.Name, &d.Target, &d.Metric, &d.Comparator, &d.Threshold,
		&stateVal, &d.DurationSec, &d.Severity, &enabled, &chJSON, &d.CreatedAt, &d.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	d.StateValue = stateVal.String
	d.Enabled = enabled != 0
	d.NotifyChannelIDs = []string{}
	if chJSON != "" {
		_ = json.Unmarshal([]byte(chJSON), &d.NotifyChannelIDs)
	}
	if d.NotifyChannelIDs == nil {
		d.NotifyChannelIDs = []string{}
	}
	return &d, nil
}

// ListAlarmDefinitions returns all definitions ordered by name.
func (s *Store) ListAlarmDefinitions(ctx context.Context) ([]*AlarmDefinition, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+alarmDefCols+` FROM alarm_definitions ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*AlarmDefinition
	for rows.Next() {
		d, err := scanAlarmDef(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetAlarmDefinition returns one definition by id.
func (s *Store) GetAlarmDefinition(ctx context.Context, id string) (*AlarmDefinition, error) {
	return scanAlarmDef(s.db.QueryRowContext(ctx,
		`SELECT `+alarmDefCols+` FROM alarm_definitions WHERE id = ?`, id))
}

// CreateAlarmDefinition inserts a new definition.
func (s *Store) CreateAlarmDefinition(ctx context.Context, d *AlarmDefinition) error {
	now := time.Now().Unix()
	d.CreatedAt, d.UpdatedAt = now, now
	chJSON := marshalIDs(d.NotifyChannelIDs)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO alarm_definitions
			(id, name, target, metric, comparator, threshold, state_value,
			 duration_sec, severity, enabled, notify_channel_ids, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.Name, d.Target, d.Metric, d.Comparator, d.Threshold, nullStr(d.StateValue),
		d.DurationSec, d.Severity, boolInt(d.Enabled), chJSON, d.CreatedAt, d.UpdatedAt)
	return err
}

// UpdateAlarmDefinition replaces a definition's mutable fields.
func (s *Store) UpdateAlarmDefinition(ctx context.Context, d *AlarmDefinition) error {
	chJSON := marshalIDs(d.NotifyChannelIDs)
	res, err := s.db.ExecContext(ctx,
		`UPDATE alarm_definitions
		 SET name = ?, target = ?, metric = ?, comparator = ?, threshold = ?, state_value = ?,
		     duration_sec = ?, severity = ?, enabled = ?, notify_channel_ids = ?, updated_at = ?
		 WHERE id = ?`,
		d.Name, d.Target, d.Metric, d.Comparator, d.Threshold, nullStr(d.StateValue),
		d.DurationSec, d.Severity, boolInt(d.Enabled), chJSON, time.Now().Unix(), d.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAlarmDefinition removes a definition + its active instances.
func (s *Store) DeleteAlarmDefinition(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM alarm_definitions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM alarm_instances WHERE definition_id = ?`, id)
	return nil
}

const alarmChCols = `id, name, type, config, created_at, updated_at`

func scanAlarmChannel(row interface{ Scan(...any) error }) (*AlarmChannel, error) {
	var c AlarmChannel
	if err := row.Scan(&c.ID, &c.Name, &c.Type, &c.Config, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// ListAlarmChannels returns all channels ordered by name.
func (s *Store) ListAlarmChannels(ctx context.Context) ([]*AlarmChannel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+alarmChCols+` FROM alarm_channels ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*AlarmChannel
	for rows.Next() {
		c, err := scanAlarmChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetAlarmChannel returns one channel by id.
func (s *Store) GetAlarmChannel(ctx context.Context, id string) (*AlarmChannel, error) {
	return scanAlarmChannel(s.db.QueryRowContext(ctx,
		`SELECT `+alarmChCols+` FROM alarm_channels WHERE id = ?`, id))
}

// CreateAlarmChannel inserts a new channel.
func (s *Store) CreateAlarmChannel(ctx context.Context, c *AlarmChannel) error {
	now := time.Now().Unix()
	c.CreatedAt, c.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO alarm_channels (id, name, type, config, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.Type, c.Config, c.CreatedAt, c.UpdatedAt)
	return err
}

// DeleteAlarmChannel removes a channel by id.
func (s *Store) DeleteAlarmChannel(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM alarm_channels WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// LoadAlarmInstances returns the persisted ACTIVE instances (resume snapshot).
func (s *Store) LoadAlarmInstances(ctx context.Context) ([]*AlarmInstance, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, definition_id, definition_name, object_id, object_name, object_type,
		        severity, metric, value, state_raw, raised_at, last_notified_at
		 FROM alarm_instances`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*AlarmInstance
	for rows.Next() {
		var in AlarmInstance
		var stateRaw sql.NullString
		var lastNotified sql.NullInt64
		if err := rows.Scan(&in.ID, &in.DefinitionID, &in.DefinitionName, &in.ObjectID,
			&in.ObjectName, &in.ObjectType, &in.Severity, &in.Metric, &in.Value,
			&stateRaw, &in.RaisedAt, &lastNotified); err != nil {
			return nil, err
		}
		in.StateRaw = stateRaw.String
		in.LastNotifiedAt = lastNotified.Int64
		out = append(out, &in)
	}
	return out, rows.Err()
}

// ReplaceAlarmInstances atomically swaps the persisted active-instance set with
// the engine's current snapshot (delete-all + insert), so the table always
// reflects exactly the currently-active alarms.
func (s *Store) ReplaceAlarmInstances(ctx context.Context, insts []*AlarmInstance) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM alarm_instances`); err != nil {
		return err
	}
	for _, in := range insts {
		var lastNotified any
		if in.LastNotifiedAt > 0 {
			lastNotified = in.LastNotifiedAt
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO alarm_instances
				(id, definition_id, definition_name, object_id, object_name, object_type,
				 severity, metric, value, state_raw, raised_at, last_notified_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.ID, in.DefinitionID, in.DefinitionName, in.ObjectID, in.ObjectName, in.ObjectType,
			in.Severity, in.Metric, in.Value, nullStr(in.StateRaw), in.RaisedAt, lastNotified); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func marshalIDs(ids []string) string {
	if ids == nil {
		ids = []string{}
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return "[]"
	}
	return string(b)
}
