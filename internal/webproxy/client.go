package webproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Client struct {
	baseURL    string
	capability string
	http       *http.Client
	upMu       sync.Mutex
	mu         sync.Mutex
	token      string
	upSequence uint64
	downCursor uint64
	nextStream uint32
	streams    map[uint32]*ClientStream
	ctx        context.Context
	cancel     context.CancelFunc
	pollDone   chan struct{}
	pollErr    error
	closed     bool
}

type ClientStream struct {
	client *Client
	id     uint32

	mu            sync.Mutex
	writeMu       sync.Mutex
	readWake      chan struct{}
	creditWake    chan struct{}
	incoming      [][]byte
	current       []byte
	currentOffset int
	sendCredit    uint64
	closed        bool
	readErr       error
}

// NewClient creates an HTTPS carrier client. baseURL may be an origin or a
// complete http(s) URL; the latter is useful with in-memory test transports.
func NewClient(baseURL, capability string, transport http.RoundTripper) (*Client, error) {
	if !canonicalBase64URL(capability, 32) {
		return nil, errors.New("invalid bridge capability")
	}
	if !strings.Contains(baseURL, "://") {
		baseURL = "https://" + baseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("invalid carrier host")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("carrier base paths are not supported")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Client{
		baseURL:    strings.TrimRight(parsed.String(), "/"),
		capability: capability,
		http:       &http.Client{Transport: transport},
		streams:    make(map[uint32]*ClientStream),
	}, nil
}

// Bootstrap fetches a one-use page token from the capability URL and creates a
// serialized HTTPS carrier session.
func (c *Client) Bootstrap(ctx context.Context) error {
	c.mu.Lock()
	if c.closed || c.token != "" {
		c.mu.Unlock()
		return errors.New("carrier client is already bootstrapped or closed")
	}
	c.mu.Unlock()

	pageURL := c.baseURL + "/?bridge=" + c.capability
	pageReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return err
	}
	pageResp, err := c.http.Do(pageReq)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, pageResp.Body)
	_ = pageResp.Body.Close()
	if pageResp.StatusCode != http.StatusOK {
		return fmt.Errorf("bridge page returned HTTP %d", pageResp.StatusCode)
	}
	bootstrapToken := pageResp.Header.Get(headerBootstrap)
	if !canonicalBase64URL(bootstrapToken, tokenSize) {
		return errors.New("bridge page did not provide a bootstrap token")
	}
	hello, _ := appendFrame(nil, frame{Type: frameHello, Payload: appendHelloPayload(nil)})
	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		req, requestErr := c.newRequest(ctx, http.MethodPost, "/api/v1/session", bootstrapToken, hello)
		if requestErr != nil {
			return requestErr
		}
		resp, err = c.http.Do(req)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	body, readErr := readBounded(resp.Body, 64)
	_ = resp.Body.Close()
	if readErr != nil {
		return readErr
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("carrier session returned HTTP %d", resp.StatusCode)
	}
	sessionToken := resp.Header.Get(headerSessionToken)
	if !canonicalBase64URL(sessionToken, tokenSize) || resp.Header.Get(headerCarrierMode) != carrierModeHTTPS {
		return errors.New("invalid carrier session response")
	}
	frame, rest, err := parseFrame(body)
	if err != nil || len(rest) != 0 || frame.Type != frameWelcome || frame.StreamID != 0 {
		return errors.New("invalid carrier welcome frame")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("carrier client is closed")
	}
	c.token = sessionToken
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.pollDone = make(chan struct{})
	c.mu.Unlock()
	go c.pollLoop()
	return nil
}

// OpenStream sends OPEN and returns a full-duplex stream backed by the carrier.
func (c *Client) OpenStream(ctx context.Context) (*ClientStream, error) {
	c.mu.Lock()
	if c.closed || c.token == "" {
		c.mu.Unlock()
		return nil, errors.New("carrier session is not active")
	}
	if c.nextStream == maxStreamID {
		c.mu.Unlock()
		return nil, errors.New("carrier stream id space exhausted")
	}
	c.nextStream++
	id := c.nextStream
	stream := &ClientStream{
		client:     c,
		id:         id,
		readWake:   make(chan struct{}, 1),
		creditWake: make(chan struct{}, 1),
		sendCredit: initialStreamWindow,
	}
	c.streams[id] = stream
	c.mu.Unlock()
	if err := c.sendFrames(ctx, frame{Type: frameOpen, StreamID: id}); err != nil {
		stream.setError(err)
		return nil, err
	}
	return stream, nil
}

// DeleteSession cancels the client's poll owner and asks the relay to remove
// this session and all of its streams.
func (c *Client) DeleteSession(ctx context.Context) error {
	c.mu.Lock()
	if c.token == "" {
		c.closed = true
		c.mu.Unlock()
		return nil
	}
	token := c.token
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	done := c.pollDone
	c.mu.Unlock()
	if done != nil {
		<-done
	}
	req, err := c.newRequest(ctx, http.MethodDelete, "/api/v1/session", token, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("carrier delete returned HTTP %d", resp.StatusCode)
	}
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
	return nil
}

// Close is shorthand for deleting the current transport session.
func (c *Client) Close() error { return c.DeleteSession(context.Background()) }

func (c *Client) newRequest(ctx context.Context, method, path, token string, body []byte) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	return req, nil
}

func (c *Client) sendFrames(ctx context.Context, frames ...frame) error {
	var body []byte
	for _, frame := range frames {
		var err error
		body, err = appendFrame(body, frame)
		if err != nil {
			return err
		}
	}
	c.upMu.Lock()
	defer c.upMu.Unlock()
	c.mu.Lock()
	if c.closed || c.token == "" {
		c.mu.Unlock()
		return errors.New("carrier session is not active")
	}
	sequence := c.upSequence + 1
	token := c.token
	c.mu.Unlock()
	for attempt := 0; attempt < 3; attempt++ {
		req, err := c.newRequest(ctx, http.MethodPost, "/api/v1/up", token, body)
		if err != nil {
			return err
		}
		req.Header.Set(headerUpSequence, strconv.FormatUint(sequence, 10))
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusServiceUnavailable {
			return errors.New("carrier uplink is under pressure")
		}
		ack, parseErr := parseCanonicalUint(resp.Header.Get(headerUpAck))
		if resp.StatusCode != http.StatusNoContent || parseErr != nil || ack != sequence {
			return fmt.Errorf("carrier uplink returned HTTP %d", resp.StatusCode)
		}
		c.mu.Lock()
		if sequence > c.upSequence {
			c.upSequence = sequence
		}
		c.mu.Unlock()
		return nil
	}
	return errors.New("carrier uplink response was lost")
}

func (c *Client) pollLoop() {
	defer close(c.pollDone)
	c.mu.Lock()
	ctx := c.ctx
	token := c.token
	c.mu.Unlock()
	for {
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		cursor := c.downCursor
		c.mu.Unlock()
		req, err := c.newRequest(ctx, http.MethodPost, "/api/v1/down", token, nil)
		if err != nil {
			c.setPollError(err)
			return
		}
		req.Header.Set(headerDownCursor, strconv.FormatUint(cursor, 10))
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		body, readErr := readBounded(resp.Body, maxDownBatch)
		_ = resp.Body.Close()
		if readErr != nil {
			c.setPollError(readErr)
			return
		}
		if resp.StatusCode == http.StatusNoContent {
			if resp.Header.Get(headerDownCursor) != strconv.FormatUint(cursor, 10) {
				c.setPollError(errors.New("invalid empty downlink cursor"))
				return
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			c.setPollError(fmt.Errorf("carrier downlink returned HTTP %d", resp.StatusCode))
			return
		}
		next, parseErr := parseCanonicalUint(resp.Header.Get(headerDownCursor))
		if parseErr != nil || next != cursor+1 {
			c.setPollError(errors.New("invalid downlink cursor"))
			return
		}
		frames, decodeErr := parseFrameBatch(body)
		if decodeErr != nil {
			c.setPollError(decodeErr)
			return
		}
		for _, frame := range frames {
			c.deliver(frame)
		}
		c.mu.Lock()
		c.downCursor = next
		c.mu.Unlock()
	}
}

func (c *Client) deliver(frame frame) {
	c.mu.Lock()
	stream := c.streams[frame.StreamID]
	c.mu.Unlock()
	if frame.Type == frameWindow && stream != nil {
		credit, err := parseWindowPayload(frame.Payload)
		if err != nil {
			c.setPollError(err)
			return
		}
		stream.mu.Lock()
		stream.sendCredit += uint64(credit)
		if stream.sendCredit > initialStreamWindow {
			stream.readErr = errInvalidUp
			stream.closed = true
		}
		signal(stream.creditWake)
		stream.mu.Unlock()
		return
	}
	if frame.Type == frameData && stream != nil {
		stream.mu.Lock()
		if !stream.closed {
			stream.incoming = append(stream.incoming, append([]byte(nil), frame.Payload...))
			signal(stream.readWake)
		}
		stream.mu.Unlock()
		return
	}
	if frame.Type == frameClose && stream != nil {
		stream.mu.Lock()
		stream.closed = true
		stream.incoming = nil
		stream.current = nil
		stream.currentOffset = 0
		signal(stream.readWake)
		signal(stream.creditWake)
		stream.mu.Unlock()
	}
}

func (c *Client) setPollError(err error) {
	c.mu.Lock()
	if c.pollErr == nil {
		c.pollErr = err
	}
	streams := make([]*ClientStream, 0, len(c.streams))
	for _, stream := range c.streams {
		streams = append(streams, stream)
	}
	c.mu.Unlock()
	for _, stream := range streams {
		stream.setError(err)
	}
}

func (s *ClientStream) Write(data []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	written := 0
	for len(data) > 0 {
		s.mu.Lock()
		for s.sendCredit == 0 && !s.closed && s.readErr == nil {
			wake := s.creditWake
			s.mu.Unlock()
			select {
			case <-s.client.ctx.Done():
				return written, s.client.ctx.Err()
			case <-wake:
			}
			s.mu.Lock()
		}
		if s.readErr != nil {
			err := s.readErr
			s.mu.Unlock()
			return written, err
		}
		if s.closed {
			s.mu.Unlock()
			return written, io.ErrClosedPipe
		}
		amount := uint64(len(data))
		if amount > maxFramePayload {
			amount = maxFramePayload
		}
		if amount > s.sendCredit {
			amount = s.sendCredit
		}
		s.sendCredit -= amount
		s.mu.Unlock()
		frame := frame{Type: frameData, StreamID: s.id, Payload: data[:int(amount)]}
		if err := s.client.sendFrames(s.client.ctx, frame); err != nil {
			return written, err
		}
		written += int(amount)
		data = data[int(amount):]
	}
	return written, nil
}

func (s *ClientStream) Read(output []byte) (int, error) {
	if len(output) == 0 {
		return 0, nil
	}
	for {
		s.mu.Lock()
		if s.currentOffset >= len(s.current) && len(s.incoming) > 0 {
			s.current = s.incoming[0]
			s.currentOffset = 0
			s.incoming[0] = nil
			s.incoming = s.incoming[1:]
		}
		if s.currentOffset < len(s.current) {
			n := copy(output, s.current[s.currentOffset:])
			s.currentOffset += n
			s.mu.Unlock()
			var payload [4]byte
			binary.BigEndian.PutUint32(payload[:], uint32(n))
			if err := s.client.sendFrames(s.client.ctx, frame{Type: frameWindow, StreamID: s.id, Payload: payload[:]}); err != nil {
				return n, err
			}
			return n, nil
		}
		if s.readErr != nil {
			err := s.readErr
			s.mu.Unlock()
			return 0, err
		}
		if s.closed {
			s.mu.Unlock()
			return 0, io.EOF
		}
		wake := s.readWake
		s.mu.Unlock()
		select {
		case <-s.client.ctx.Done():
			return 0, s.client.ctx.Err()
		case <-wake:
		}
	}
}

func (s *ClientStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	err := s.client.sendFrames(s.client.ctx, frame{Type: frameClose, StreamID: s.id})
	s.mu.Lock()
	s.closed = true
	s.incoming = nil
	s.current = nil
	s.currentOffset = 0
	signal(s.readWake)
	signal(s.creditWake)
	s.mu.Unlock()
	return err
}

func (s *ClientStream) setError(err error) {
	s.mu.Lock()
	if s.readErr == nil {
		s.readErr = err
	}
	signal(s.readWake)
	signal(s.creditWake)
	s.mu.Unlock()
}
