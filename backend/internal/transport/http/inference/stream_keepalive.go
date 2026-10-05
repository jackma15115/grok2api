package inference

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/chenyme/grok2api/backend/internal/application/gateway"
	"github.com/gin-gonic/gin"
)

const streamKeepAliveKey = "inference.streamKeepAlive"
const streamKeepAliveInterval = 15 * time.Second

// The handler owns all downstream writes. Only upstream work runs in workers,
// so heartbeats cannot race Gin's writer, response headers, or stream metadata.
type streamKeepAlive struct {
	ctx      context.Context
	cancel   context.CancelCauseFunc
	writer   gin.ResponseWriter
	protocol streamProtocol
	model    string
	ticker   *time.Ticker
	ticks    <-chan time.Time
	started  bool

	pending          []byte
	lineHasData      bool
	previousCR       bool
	partialForwarded bool
}

func (h *Handler) SetStreamKeepAliveResolver(resolve func() bool) *Handler {
	h.streamKeepAliveEnabled = resolve
	return h
}

func (h *Handler) beginStreamKeepAlive(c *gin.Context, stream bool, protocol streamProtocol, model string) *streamKeepAlive {
	if !stream || (h.streamKeepAliveEnabled != nil && !h.streamKeepAliveEnabled()) {
		return nil
	}
	ctx, cancel := context.WithCancelCause(c.Request.Context())
	s := &streamKeepAlive{ctx: ctx, cancel: cancel, writer: c.Writer, protocol: protocol, model: model}
	s.ticker = time.NewTicker(streamKeepAliveInterval)
	s.ticks = s.ticker.C
	c.Request = c.Request.WithContext(ctx)
	c.Set(streamKeepAliveKey, s)
	return s
}

func (s *streamKeepAlive) Close() {
	if s != nil {
		s.ticker.Stop()
		s.cancel(nil)
	}
}

func requestStreamKeepAlive(c *gin.Context) *streamKeepAlive {
	value, _ := c.Get(streamKeepAliveKey)
	s, _ := value.(*streamKeepAlive)
	return s
}

func setInferenceStreamHeaders(header http.Header) {
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("X-Accel-Buffering", "no")
	header.Del("Content-Length")
}

func (s *streamKeepAlive) ping() error {
	if err := s.ctx.Err(); err != nil {
		return context.Cause(s.ctx)
	}
	if err := setResponseWriteDeadline(s.writer); err != nil {
		return err
	}
	// Very large events are passed through without unbounded buffering. A
	// comment must never be inserted into a partially forwarded SSE event.
	if s.partialForwarded {
		return nil
	}
	if !s.writer.Written() {
		setInferenceStreamHeaders(s.writer.Header())
	}
	if _, err := s.writer.Write([]byte(": PING\n\n")); err != nil {
		return err
	}
	s.started = true
	return http.NewResponseController(s.writer).Flush()
}

type gatewayResult struct {
	result     *gateway.Result
	err        error
	panicValue any
}

func awaitGatewayResult(ctx context.Context, s *streamKeepAlive, call func(context.Context) (*gateway.Result, error)) (*gateway.Result, error) {
	if s == nil {
		return call(ctx)
	}
	results := make(chan gatewayResult, 1)
	go func() {
		var result gatewayResult
		defer func() {
			result.panicValue = recover()
			results <- result
		}()
		result.result, result.err = call(ctx)
	}()
	for {
		var result gatewayResult
		select {
		case result = <-results:
		case <-s.ticks:
			if err := s.ping(); err != nil {
				s.cancel(err)
			}
			continue
		case <-ctx.Done():
			// Join the worker before returning, including any result created
			// concurrently with cancellation, so leases and bodies are released.
			result = <-results
		}
		if result.panicValue != nil {
			panic(result.panicValue)
		}
		if ctx.Err() != nil {
			if result.result != nil {
				_ = result.result.Body.Close()
				result.result.Finalize(gateway.Usage{}, "", classifyCopyError(ctx, context.Cause(ctx)))
			}
			return nil, context.Cause(ctx)
		}
		return result.result, result.err
	}
}

type streamReadResult struct {
	data       []byte
	err        error
	panicValue any
}

type keepAliveReader struct {
	source    io.ReadCloser
	stream    *streamKeepAlive
	results   chan streamReadResult
	requests  chan struct{}
	stop      chan struct{}
	done      chan struct{}
	pending   streamReadResult
	closeOnce sync.Once
	closeErr  error
	reading   bool
}

func newKeepAliveReader(source io.ReadCloser, s *streamKeepAlive) *keepAliveReader {
	r := &keepAliveReader{source: source, stream: s, results: make(chan streamReadResult), requests: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{})}
	go r.run()
	return r
}

func (r *keepAliveReader) run() {
	defer close(r.done)
	defer func() {
		if value := recover(); value != nil {
			select {
			case r.results <- streamReadResult{panicValue: value}:
			case <-r.stop:
			}
		}
	}()
	buffer := make([]byte, responseCopyBufferBytes)
	for {
		// Read only on demand. Downstream backpressure must not start an
		// upstream semantic-idle timer while the handler is writing a frame.
		select {
		case <-r.requests:
		case <-r.stop:
			return
		}
		n, err := r.source.Read(buffer)
		select {
		case r.results <- streamReadResult{data: append([]byte(nil), buffer[:n]...), err: err}:
		case <-r.stop:
			return
		}
		if err != nil {
			return
		}
	}
}

func (r *keepAliveReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	for len(r.pending.data) == 0 && r.pending.err == nil {
		if !r.reading {
			select {
			case r.requests <- struct{}{}:
				r.reading = true
			case <-r.stream.ctx.Done():
				return 0, context.Cause(r.stream.ctx)
			}
		}
		select {
		case result := <-r.results:
			r.reading = false
			if result.panicValue != nil {
				panic(result.panicValue)
			}
			r.pending = result
		case <-r.stream.ticks:
			if err := r.stream.ping(); err != nil {
				r.stream.cancel(err)
				return 0, err
			}
		case <-r.stream.ctx.Done():
			return 0, context.Cause(r.stream.ctx)
		}
	}
	n := copy(buffer, r.pending.data)
	r.pending.data = r.pending.data[n:]
	if len(r.pending.data) > 0 {
		return n, nil
	}
	return n, r.pending.err
}

func (r *keepAliveReader) Close() error {
	r.closeOnce.Do(func() {
		close(r.stop)
		r.closeErr = r.source.Close()
		<-r.done
	})
	return r.closeErr
}

// Hold partial SSE frames so heartbeats can continue between upstream reads
// without becoming part of JSON or dispatching an unfinished event. Buffering
// is bounded; oversized image events retain the original streaming behavior.
func (s *streamKeepAlive) frame(chunk []byte, final bool) []byte {
	boundary := 0
	prefix := len(s.pending)
	for index, b := range chunk {
		if b == '\n' && s.previousCR {
			s.previousCR = false
			if boundary == prefix+index {
				boundary++
			}
			continue
		}
		s.previousCR = b == '\r'
		if b == '\r' || b == '\n' {
			if !s.lineHasData {
				boundary = prefix + index + 1
			}
			s.lineHasData = false
		} else {
			s.lineHasData = true
		}
	}
	s.pending = append(s.pending, chunk...)
	if final || (boundary == 0 && (s.partialForwarded || len(s.pending) > maxStreamEventInspectionBytes)) {
		out := s.pending
		s.pending = nil
		s.partialForwarded = !final
		return out
	}
	if boundary == 0 {
		return nil
	}
	out := s.pending[:boundary]
	s.pending = append([]byte(nil), s.pending[boundary:]...)
	s.partialForwarded = false
	return out
}

func (s *streamKeepAlive) writeError(c *gin.Context, errorType, code, message string) {
	compat := &responsesCompatState{model: s.model}
	trailer := streamErrorTrailer(s.protocol, code, message, errorType, responseMetadata{}, compat)
	if setResponseWriteDeadline(s.writer) == nil {
		if _, err := s.writer.Write(trailer); err == nil {
			s.writer.Flush()
		}
	}
	c.Abort()
}
