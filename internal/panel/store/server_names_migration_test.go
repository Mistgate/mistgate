package store

import (
	"context"
	"testing"
)

// 00026: an install whose stored settings keep the old default server name "{flag} {node}" moves to "{flag} {country}";
// a template somebody edited is left alone, and so is everything else in the document. Down changes no data: it cannot
// tell Up's "{flag} {country}" from the owner's own choice of it.
func TestServerNamesMigration(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct{ name, stored, want string }{
		{"old default", `{"title":"T","server_name_template":"{flag} {node}","user_page":{"show_qr":true}}`, "{flag} {country}"},
		{"edited", `{"server_name_template":"{node} · {profile}"}`, "{node} · {profile}"},
		{"chosen by the owner", `{"server_name_template":"{flag} {country}"}`, "{flag} {country}"},
	} {
		s, p := openProvider(t)
		if _, err := p.UpTo(ctx, 19); err != nil {
			t.Fatal(err)
		}
		execT(t, s, `INSERT INTO setting (k, v) VALUES ('subscription_settings', ?), ('other', '{"server_name_template":"{flag} {node}"}')`, c.stored)
		if _, err := p.UpTo(ctx, 26); err != nil {
			t.Fatalf("%s: up: %v", c.name, err)
		}
		var tpl, title, other string
		if err := s.W.QueryRowContext(ctx, `SELECT json_extract(v, '$.server_name_template'), coalesce(json_extract(v, '$.title'), '') FROM setting WHERE k = 'subscription_settings'`).Scan(&tpl, &title); err != nil {
			t.Fatal(err)
		}
		s.W.QueryRowContext(ctx, `SELECT v FROM setting WHERE k = 'other'`).Scan(&other)
		if tpl != c.want || other != `{"server_name_template":"{flag} {node}"}` {
			t.Errorf("%s: template %q, other setting %q", c.name, tpl, other)
		}
		if c.name == "old default" && title != "T" {
			t.Errorf("the rest of the document changed: title %q", title)
		}
		if _, err := p.DownTo(ctx, 19); err != nil {
			t.Fatalf("%s: down: %v", c.name, err)
		}
		s.W.QueryRowContext(ctx, `SELECT json_extract(v, '$.server_name_template') FROM setting WHERE k = 'subscription_settings'`).Scan(&tpl)
		if tpl != c.want {
			t.Errorf("%s: down rewrote the template to %q", c.name, tpl)
		}
	}
	// An install that never saved its settings has nothing stored, and the migration is a no-op.
	s, p := openProvider(t)
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countT(t, s, `SELECT count(*) FROM setting WHERE k = 'subscription_settings'`); n != 0 {
		t.Errorf("settings appeared: %d", n)
	}
}
