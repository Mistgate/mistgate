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

// 00035: move settings that still use the previous default to country + profile names so client lists identify
// protocol variants and WARP twins. A custom template and a document without one stay untouched.
func TestServerNamesProfileMigration(t *testing.T) {
	ctx := context.Background()
	const newDefault = "{flag} {country} · {profile}"
	for _, c := range []struct{ name, stored, want string }{
		{"previous default", `{"title":"T","server_name_template":"{flag} {country}","user_page":{"show_qr":true}}`, newDefault},
		{"custom", `{"server_name_template":"{node} / {profile}"}`, "{node} / {profile}"},
		{"unset", `{"title":"T"}`, ""},
		{"invalid document", `{not json`, ""},
	} {
		s, p := openProvider(t)
		if _, err := p.UpTo(ctx, 34); err != nil {
			t.Fatal(err)
		}
		execT(t, s, `INSERT INTO setting (k, v) VALUES ('subscription_settings', ?), ('other', '{"server_name_template":"{flag} {country}"}')`, c.stored)
		if _, err := p.Up(ctx); err != nil {
			t.Fatalf("%s: up: %v", c.name, err)
		}
		var tpl, title, other string
		if err := s.W.QueryRowContext(ctx, `SELECT CASE WHEN json_valid(v) THEN coalesce(json_extract(v, '$.server_name_template'), '') ELSE '' END, CASE WHEN json_valid(v) THEN coalesce(json_extract(v, '$.title'), '') ELSE '' END FROM setting WHERE k = 'subscription_settings'`).Scan(&tpl, &title); err != nil {
			t.Fatal(err)
		}
		if err := s.W.QueryRowContext(ctx, `SELECT v FROM setting WHERE k = 'other'`).Scan(&other); err != nil {
			t.Fatal(err)
		}
		if tpl != c.want || other != `{"server_name_template":"{flag} {country}"}` {
			t.Errorf("%s: template %q, other setting %q", c.name, tpl, other)
		}
		if c.name == "previous default" && title != "T" {
			t.Errorf("the rest of the document changed: title %q", title)
		}
		if _, err := p.DownTo(ctx, 34); err != nil {
			t.Fatalf("%s: down: %v", c.name, err)
		}
		if err := s.W.QueryRowContext(ctx, `SELECT CASE WHEN json_valid(v) THEN coalesce(json_extract(v, '$.server_name_template'), '') ELSE '' END FROM setting WHERE k = 'subscription_settings'`).Scan(&tpl); err != nil {
			t.Fatal(err)
		}
		if tpl != c.want {
			t.Errorf("%s: down rewrote the template to %q", c.name, tpl)
		}
	}

	// No saved subscription settings means the new code default applies without the migration creating a row.
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 34); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countT(t, s, `SELECT count(*) FROM setting WHERE k = 'subscription_settings'`); n != 0 {
		t.Errorf("settings appeared: %d", n)
	}
}
