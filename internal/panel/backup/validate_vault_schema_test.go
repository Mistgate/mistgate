//go:build !js

package backup

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/assistant"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/tasks"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// A sealed column the restore check does not know is a column a wrong master.key slips past: the archive restores, and the
// value cannot be opened afterwards. The list is compared with the schema instead of being kept in step by hand.
func TestRestoreChecksEverySealedColumn(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rows, err := st.R.QueryContext(ctx, `SELECT m.name, p.name FROM sqlite_master m, pragma_table_info(m.name) p
		WHERE m.type = 'table' AND p.name LIKE '%\_enc' ESCAPE '\'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := 0
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		found++
		checked := slices.ContainsFunc(restoredSealedValueQueries, func(q sealedValueQuery) bool {
			return q.table == table && len(q.columns) > 0 && q.columns[0] == column
		})
		if !checked {
			t.Errorf("%s.%s is sealed but restoredSealedValueQueries does not check it", table, column)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("no *_enc column in the schema: the query found nothing")
	}
}

// Settings that hold sealed values name their AAD "setting/<key>"; every such name in the panel needs a restore check.
func TestRestoreChecksEverySealedSetting(t *testing.T) {
	literal := regexp.MustCompile(`"setting/[A-Za-z0-9_.]+"`)
	var seen []string
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
			filepath.Base(path) == "validate_vault.go" {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range literal.FindAll(src, -1) {
			seen = append(seen, strings.Trim(string(m), `"`))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 {
		t.Fatal("no setting/ AAD found in the panel: the scan found nothing")
	}
	for _, aad := range seen {
		if !slices.ContainsFunc(restoredSealedSettingKeys, func(k struct{ key, aad string }) bool { return k.aad == aad }) {
			t.Errorf("%s is a sealed setting but restoredSealedSettingKeys does not check it", aad)
		}
	}
}

// A task prompt and a follow-up note sealed under another master.key refuse the restore, each by its own name.
func TestRestoreRefusesForeignTaskPromptAndFollowUpNote(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	own := bytes.Repeat([]byte{0x11}, vault.KeySize)
	foreign := bytes.Repeat([]byte{0x22}, vault.KeySize)
	ownVault, err := vault.New(own)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSealedDatabaseValues(ctx, st.R, own); err != nil {
		t.Fatalf("an empty database: %v", err)
	}
	for _, c := range []struct{ name, stmt, aad string }{
		{"scheduled task prompt", `INSERT INTO scheduled_task(id,name,kind,schedule_kind,prompt_enc,created_by,created_at,updated_at) VALUES('tsk_a','a','assistant','every',?,'adm_a',1,1)`, tasks.PromptAAD("tsk_a")},
		{"assistant follow-up note", `INSERT INTO assistant_follow_up(id,conversation_id,admin_id,note_enc,created_at) VALUES('fup_a','cnv_a','adm_a',?,1)`, assistant.FollowUpAAD("fup_a")},
	} {
		if _, err := st.W.ExecContext(ctx, c.stmt, ownVault.Seal([]byte("text"), c.aad)); err != nil {
			t.Fatal(err)
		}
		if err := validateSealedDatabaseValues(ctx, st.R, own); err != nil {
			t.Fatalf("%s under its own key: %v", c.name, err)
		}
		err := validateSealedDatabaseValues(ctx, st.R, foreign)
		if err == nil || !strings.Contains(err.Error(), c.name) {
			t.Errorf("%s under a foreign key: %v", c.name, err)
		}
		if _, err := st.W.ExecContext(ctx, `DELETE FROM scheduled_task; DELETE FROM assistant_follow_up`); err != nil {
			t.Fatal(err)
		}
	}
}
