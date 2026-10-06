package agentlink

import (
	"context"
	"errors"
	"io"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

const MaxFrameSize = 4 << 20

func ReadFrame(ctx context.Context, ws *websocket.Conn) (websocket.MessageType, []byte, error) {
	typ, reader, err := ws.Reader(ctx)
	if err != nil {
		return typ, nil, err
	}
	b, err := io.ReadAll(io.LimitReader(reader, MaxFrameSize+1))
	if err != nil {
		return typ, nil, err
	}
	if len(b) > MaxFrameSize {
		return typ, nil, ErrFrameTooLarge
	}
	return typ, b, nil
}

func WriteFrame(ctx context.Context, ws *websocket.Conn, message proto.Message) error {
	b, err := proto.Marshal(message)
	if err != nil {
		return err
	}
	if len(b) > MaxFrameSize {
		return ErrFrameTooLarge
	}
	return ws.Write(ctx, websocket.MessageBinary, b)
}

var ErrFrameTooLarge = errors.New("link frame too large")
