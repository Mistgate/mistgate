//go:build !js

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 00048: the tables come up on an existing database and go away on the way down, and the way up again works.
func TestTelegramMigrationUpAndDown(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 47); err != nil {
		t.Fatalf("up to 47: %v", err)
	}
	execT(t, s, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES ('adm_1', 'Owner', 'owner', x'01', 1)`)
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up through the telegram migration: %v", err)
	}
	for _, table := range []string{"telegram_bot", "telegram_link"} {
		if countT(t, s, `SELECT count(*) FROM sqlite_master WHERE name = ?`, table) != 1 {
			t.Fatalf("%s missing after up", table)
		}
	}
	if n := countT(t, s, `SELECT count(*) FROM admin`); n != 1 {
		t.Fatalf("the admin did not survive the migration: %d", n)
	}
	if _, err := p.DownTo(ctx, 47); err != nil {
		t.Fatalf("down to 47: %v", err)
	}
	for _, table := range []string{"telegram_bot", "telegram_link"} {
		if countT(t, s, `SELECT count(*) FROM sqlite_master WHERE name = ?`, table) != 0 {
			t.Fatalf("%s is left after rolling back the migration", table)
		}
	}
	if n := countT(t, s, `SELECT count(*) FROM admin`); n != 1 {
		t.Fatalf("rolling back lost the admin: %d", n)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

func TestTelegramLinkCodeMigrationUpAndDown(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 73); err != nil {
		t.Fatalf("up to 73: %v", err)
	}
	if countT(t, s, `SELECT count(*) FROM sqlite_master WHERE name = 'telegram_link_code'`) != 0 {
		t.Fatal("link-code table exists before migration 00074")
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up through migration 00074: %v", err)
	}
	if countT(t, s, `SELECT count(*) FROM sqlite_master WHERE name = 'telegram_link_code'`) != 1 {
		t.Fatal("link-code table missing after migration 00074")
	}
	if _, err := p.DownTo(ctx, 73); err != nil {
		t.Fatalf("down to 73: %v", err)
	}
	if countT(t, s, `SELECT count(*) FROM sqlite_master WHERE name = 'telegram_link_code'`) != 0 {
		t.Fatal("link-code table remains after rolling back migration 00074")
	}
}

func TestTelegramBotAndLinks(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	now := time.Unix(1_700_000_000, 0)
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
	for _, ex := range []string{
		`INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES ('adm_1', 'One', 'owner', x'01', 1)`,
		`INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES ('adm_2', 'Two', 'helper', x'02', 1)`,
	} {
		execT(t, s, ex)
	}
	if _, err := s.TelegramBot(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a fresh database has a bot: %v", err)
	}
	if dropped, err := s.SetTelegramBot(ctx, TelegramBotRow{Token: []byte("sealed-1"), BotID: 11, Username: "first_bot"}, now); err != nil || dropped != 0 {
		t.Fatalf("set bot: %d, %v", dropped, err)
	}
	if err := s.BindTelegramChat(ctx, "adm_1", 100, now); err != nil {
		t.Fatal(err)
	}
	if err := s.BindTelegramChat(ctx, "adm_2", 200, now); err != nil {
		t.Fatal(err)
	}
	if err := s.BindTelegramChat(ctx, "adm_nobody", 300, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a link for an unknown admin: %v", err)
	}

	// the same bot pasted again keeps the chats; its token is replaced
	if dropped, err := s.SetTelegramBot(ctx, TelegramBotRow{Token: []byte("sealed-2"), BotID: 11, Username: "first_bot"}, now); err != nil || dropped != 0 {
		t.Fatalf("same bot again: %d, %v", dropped, err)
	}
	if b, _ := s.TelegramBot(ctx); string(b.Token) != "sealed-2" {
		t.Fatalf("token not replaced: %q", b.Token)
	}
	if links, _ := s.TelegramLinks(ctx); len(links) != 2 {
		t.Fatalf("links after the same bot: %d", len(links))
	}

	// one chat, one admin: adm_2 takes chat 100, adm_1 loses it
	if err := s.BindTelegramChat(ctx, "adm_2", 100, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TelegramLink(ctx, "adm_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("adm_1 kept a chat another admin took: %v", err)
	}
	l, err := s.TelegramLink(ctx, "adm_2")
	if err != nil || l.ChatID != 100 || !l.Enabled || l.AdminName != "Two" || l.Role != "helper" {
		t.Fatalf("adm_2 link: %+v, %v", l, err)
	}
	if by, err := s.TelegramLinkByChat(ctx, 100); err != nil || by.AdminID != "adm_2" {
		t.Fatalf("by chat: %+v, %v", by, err)
	}

	if err := s.SetTelegramEnabled(ctx, "adm_2", false); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.TelegramLink(ctx, "adm_2"); l.Enabled {
		t.Fatal("alerts still on")
	}
	if err := s.SetTelegramEnabled(ctx, "adm_1", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("switching an admin without a chat: %v", err)
	}

	// deleting an admin takes the link with it
	execT(t, s, `DELETE FROM admin WHERE id = 'adm_2'`)
	if links, _ := s.TelegramLinks(ctx); len(links) != 0 {
		t.Fatalf("links of a deleted admin: %+v", links)
	}

	// another bot drops every link
	if err := s.BindTelegramChat(ctx, "adm_1", 400, now); err != nil {
		t.Fatal(err)
	}
	if dropped, err := s.SetTelegramBot(ctx, TelegramBotRow{Token: []byte("sealed-3"), BotID: 22, Username: "second_bot"}, now); err != nil || dropped != 1 {
		t.Fatalf("another bot: %d, %v", dropped, err)
	}
	if links, _ := s.TelegramLinks(ctx); len(links) != 0 {
		t.Fatalf("links survived another bot: %+v", links)
	}

	if had, err := s.UnlinkTelegram(ctx, "adm_1"); err != nil || had {
		t.Fatalf("unlink without a link: %v, %v", had, err)
	}
	if dropped, err := s.ClearTelegramBot(ctx); err != nil || dropped != 0 {
		t.Fatalf("clear: %d, %v", dropped, err)
	}
	if _, err := s.TelegramBot(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bot after clear: %v", err)
	}
}

func TestTelegramLinkCodeLifecycleAndConcurrentTake(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	for _, ex := range []string{
		`INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES ('adm_alice', 'alice', 'owner', x'01', 1)`,
		`INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES ('adm_helper', 'helper', 'helper', x'02', 1)`,
	} {
		execT(t, s, ex)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	oldHash := strings.Repeat("a", 64)
	newHash := strings.Repeat("b", 64)
	if err := s.SetTelegramLinkCode(ctx, oldHash, "adm_alice", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.HasPendingTelegramLinkCode(ctx, now); err != nil || !pending {
		t.Fatalf("pending code = %v, %v; want true", pending, err)
	}
	if err := s.SetTelegramLinkCode(ctx, newHash, "adm_alice", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TakeTelegramLinkCode(ctx, oldHash, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replaced code take = %v, want ErrNotFound", err)
	}

	const contenders = 24
	start := make(chan struct{})
	var winners atomic.Int32
	var unexpected atomic.Pointer[error]
	var wg sync.WaitGroup
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			adminID, err := s.TakeTelegramLinkCode(ctx, newHash, now)
			if err == nil {
				if adminID != "adm_alice" {
					e := fmt.Errorf("taken by %q", adminID)
					unexpected.CompareAndSwap(nil, &e)
				}
				winners.Add(1)
			} else if !errors.Is(err, ErrNotFound) {
				unexpected.CompareAndSwap(nil, &err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if err := unexpected.Load(); err != nil {
		t.Fatalf("concurrent take error: %v", *err)
	}
	if got := winners.Load(); got != 1 {
		t.Fatalf("concurrent code takes = %d, want exactly one", got)
	}
	if pending, err := s.HasPendingTelegramLinkCode(ctx, now); err != nil || pending {
		t.Fatalf("pending after take = %v, %v; want false", pending, err)
	}

	if err := s.SetTelegramLinkCode(ctx, oldHash, "adm_helper", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TakeTelegramLinkCode(ctx, oldHash, now.Add(2*time.Second)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired code take = %v, want ErrNotFound", err)
	}
	if pending, err := s.HasPendingTelegramLinkCode(ctx, now.Add(2*time.Second)); err != nil || pending {
		t.Fatalf("expired code pending = %v, %v; want false", pending, err)
	}
}
