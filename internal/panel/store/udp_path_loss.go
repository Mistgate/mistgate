package store

import (
	"context"
	"strings"
	"time"
)

// UDPPathLoss is a persistent comparison between a sender whose packets were lost and one that delivered them.
type UDPPathLoss struct {
	SenderID, TargetID, Ports string
	Sent, Got                 uint32
	OtherSenderID             string
	OtherGot                  uint32
	CheckedAt                 time.Time
	SenderName, SenderCountry string
	TargetName                string
	OtherSenderName           string
	OtherSenderCountry        string
}

// UpdateUDPPathLosses replaces the result for senders in a completed two-node check of targetID. Senders present in
// losses are upserted; senders that ran but had no path loss have their old row removed.
func (s *Store) UpdateUDPPathLosses(ctx context.Context, targetID string, checkedSenders []string, losses []UDPPathLoss) error {
	if targetID == "" || len(checkedSenders) == 0 {
		return nil
	}
	_, err := s.batch(ctx, udpPathLossStatements(targetID, checkedSenders, losses)...)
	return err
}

// PutPortChecksAndUDPPathLosses saves the node verdicts and their path attribution as one atomic batch.
func (s *Store) PutPortChecksAndUDPPathLosses(ctx context.Context, checks []PortCheck, targetID string, checkedSenders []string, losses []UDPPathLoss) error {
	statements := make([]Stmt, 0, 3)
	if stmt, ok := portCheckStatement(checks); ok {
		statements = append(statements, stmt)
	}
	statements = append(statements, udpPathLossStatements(targetID, checkedSenders, losses)...)
	if len(statements) == 0 {
		return nil
	}
	_, err := s.batch(ctx, statements...)
	return err
}

func udpPathLossStatements(targetID string, checkedSenders []string, losses []UDPPathLoss) []Stmt {
	if targetID == "" || len(checkedSenders) == 0 {
		return nil
	}
	blamed := make([]string, 0, len(losses))
	for _, loss := range losses {
		if loss.SenderID != "" && loss.TargetID == targetID && loss.OtherSenderID != "" {
			blamed = append(blamed, loss.SenderID)
		}
	}
	statements := []Stmt{{Query: `DELETE FROM udp_path_loss
		WHERE target_id = ? AND sender_id IN (SELECT value FROM json_each(?))
		AND sender_id NOT IN (SELECT value FROM json_each(?))`,
		Args: []any{targetID, jsonStrings(checkedSenders), jsonStrings(blamed)}}}
	if len(losses) > 0 {
		values := make([]string, 0, len(losses))
		args := make([]any, 0, len(losses)*8)
		for _, loss := range losses {
			if loss.SenderID == "" || loss.TargetID != targetID || loss.OtherSenderID == "" {
				continue
			}
			values = append(values, "(?, ?, ?, ?, ?, ?, ?, ?)")
			args = append(args, loss.SenderID, loss.TargetID, loss.Ports, int64(loss.Sent), int64(loss.Got),
				loss.OtherSenderID, int64(loss.OtherGot), unix(loss.CheckedAt))
		}
		if len(values) > 0 {
			statements = append(statements, Stmt{Query: `INSERT INTO udp_path_loss
				(sender_id, target_id, ports, sent, got, other_sender_id, other_got, checked_at)
				VALUES ` + strings.Join(values, ", ") + `
				ON CONFLICT(sender_id, target_id) DO UPDATE SET
					ports = excluded.ports,
					sent = excluded.sent,
					got = excluded.got,
					other_sender_id = excluded.other_sender_id,
					other_got = excluded.other_got,
					checked_at = excluded.checked_at`, Args: args})
		}
	}
	return statements
}

// UDPPathLosses reads path-loss rows newer than since, with node names and countries for alerts and events.
func (s *Store) UDPPathLosses(ctx context.Context, since time.Time) ([]UDPPathLoss, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT p.sender_id, p.target_id, p.ports, p.sent, p.got, p.other_sender_id,
		p.other_got, p.checked_at, sender.name, sender.country_code, target.name, other.name, other.country_code
		FROM udp_path_loss p
		JOIN node sender ON sender.id = p.sender_id
		JOIN node target ON target.id = p.target_id
		JOIN node other ON other.id = p.other_sender_id
		WHERE p.checked_at > ? ORDER BY p.checked_at, p.sender_id, p.target_id`, unix(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UDPPathLoss
	for rows.Next() {
		var loss UDPPathLoss
		var sent, got, otherGot, checkedAt int64
		if err := rows.Scan(&loss.SenderID, &loss.TargetID, &loss.Ports, &sent, &got, &loss.OtherSenderID,
			&otherGot, &checkedAt, &loss.SenderName, &loss.SenderCountry, &loss.TargetName, &loss.OtherSenderName,
			&loss.OtherSenderCountry); err != nil {
			return nil, err
		}
		loss.Sent, loss.Got, loss.OtherGot = uint32(sent), uint32(got), uint32(otherGot)
		loss.CheckedAt = fromUnix(checkedAt)
		out = append(out, loss)
	}
	return out, rows.Err()
}
