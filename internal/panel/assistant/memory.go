package assistant

import (
	"context"
	"database/sql"
	"errors"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func (s *Service) listMemory(ctx context.Context, adminID string) ([]*adminv1.MemoryNote, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT id,text_enc,by_assistant,conversation_id,created_at,updated_at FROM assistant_memory WHERE admin_id=? ORDER BY updated_at DESC,id`, adminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*adminv1.MemoryNote
	for rows.Next() {
		var id, conversation string
		var sealed []byte
		var by int
		var created, updated int64
		if err = rows.Scan(&id, &sealed, &by, &conversation, &created, &updated); err != nil {
			return nil, err
		}
		text, err := openText(s.vault, sealed, MemoryAAD(id))
		if err != nil {
			return nil, err
		}
		out = append(out, &adminv1.MemoryNote{Id: id, Text: text, ByAssistant: by != 0, ConversationId: conversation, CreatedUnix: created, UpdatedUnix: updated})
	}
	return out, rows.Err()
}

func (s *Service) ListMemory(ctx context.Context, _ *connect.Request[adminv1.ListMemoryRequest]) (*connect.Response[adminv1.ListMemoryResponse], error) {
	admin, err := adminFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.requireAccess(ctx, admin); err != nil {
		return nil, err
	}
	notes, err := s.listMemory(ctx, admin.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.ListMemoryResponse{Notes: notes}), nil
}

func (s *Service) SaveMemory(ctx context.Context, req *connect.Request[adminv1.SaveMemoryRequest]) (*connect.Response[adminv1.SaveMemoryResponse], error) {
	admin, err := adminFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.requireAccess(ctx, admin); err != nil {
		return nil, err
	}
	if !validateAssistantText(req.Msg.Text, 400) {
		return nil, codeConnect("note_too_long")
	}
	if err = s.ensureRows(ctx); err != nil {
		return nil, err
	}
	id := req.Msg.Id
	now := s.now()
	created := now.Unix()
	if id == "" {
		id = newMemoryID()
		sealed := s.vault.Seal([]byte(req.Msg.Text), MemoryAAD(id))
		err = s.st.InsertAssistantMemory(ctx, id, admin.ID, sealed, false, "", now)
		if errors.Is(err, store.ErrAssistantLimit) {
			return nil, codeConnect("too_many_notes")
		}
		if err != nil {
			return nil, err
		}
	}
	if req.Msg.Id != "" {
		var old int64
		if err = s.st.R.QueryRowContext(ctx, `SELECT created_at FROM assistant_memory WHERE id=? AND admin_id=?`, id, admin.ID).Scan(&old); errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("memory note not found"))
		}
		if err != nil {
			return nil, err
		}
		created = old
		sealed := s.vault.Seal([]byte(req.Msg.Text), MemoryAAD(id))
		updated, err := s.st.UpdateAssistantMemory(ctx, id, admin.ID, sealed, now)
		if err != nil {
			return nil, err
		}
		if !updated {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("memory note not found"))
		}
	}
	note := &adminv1.MemoryNote{Id: id, Text: req.Msg.Text, CreatedUnix: created, UpdatedUnix: now.Unix()}
	return connect.NewResponse(&adminv1.SaveMemoryResponse{Note: note}), nil
}

func (s *Service) DeleteMemory(ctx context.Context, req *connect.Request[adminv1.DeleteMemoryRequest]) (*connect.Response[adminv1.DeleteMemoryResponse], error) {
	admin, err := adminFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.requireAccess(ctx, admin); err != nil {
		return nil, err
	}
	if req.Msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("memory note id is required"))
	}
	deleted, err := s.st.DeleteAssistantMemory(ctx, req.Msg.Id, admin.ID)
	if err != nil {
		return nil, err
	}
	if !deleted {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("memory note not found"))
	}
	return connect.NewResponse(&adminv1.DeleteMemoryResponse{}), nil
}

func (s *Service) ForgetAll(ctx context.Context, _ *connect.Request[adminv1.ForgetAllRequest]) (*connect.Response[adminv1.ForgetAllResponse], error) {
	admin, err := adminFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.requireAccess(ctx, admin); err != nil {
		return nil, err
	}
	deleted, err := s.st.DeleteAssistantMemoryAll(ctx, admin.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.ForgetAllResponse{Deleted: uint32(max(deleted, 0))}), nil
}

func (s *Service) saveAssistantMemory(ctx context.Context, adminID, conversationID, text string) (*adminv1.MemoryNote, error) {
	text = cleanContextText(text, 400)
	if !validateAssistantText(text, 400) {
		return nil, codeConnect("note_too_long")
	}
	id := newMemoryID()
	now := s.now()
	sealed := s.vault.Seal([]byte(text), MemoryAAD(id))
	err := s.st.InsertAssistantMemory(ctx, id, adminID, sealed, true, conversationID, now)
	if errors.Is(err, store.ErrAssistantLimit) {
		return nil, codeConnect("too_many_notes")
	}
	if err != nil {
		return nil, err
	}
	return &adminv1.MemoryNote{Id: id, Text: text, CreatedUnix: now.Unix(), UpdatedUnix: now.Unix(), ByAssistant: true, ConversationId: conversationID}, nil
}

func (s *Service) forgetAssistantMemory(ctx context.Context, adminID, id string) error {
	if id == "" {
		return errors.New("memory id is required")
	}
	deleted, err := s.st.DeleteAssistantMemory(ctx, id, adminID)
	if err != nil {
		return err
	}
	if !deleted {
		return errors.New("memory note not found")
	}
	return nil
}

func (s *Service) requireAccess(ctx context.Context, admin store.Admin) error {
	access, err := s.access(ctx, admin)
	if err != nil {
		return err
	}
	if !access.Available {
		return codeConnect(access.Reason)
	}
	return nil
}
