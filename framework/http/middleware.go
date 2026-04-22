package httpuow

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/pakasa-io/uow"
)

// Middleware returns a net/http middleware that injects a managed UnitOfWork.
func Middleware(manager *uow.Manager, cfg Config) func(http.Handler) http.Handler {
	if manager == nil {
		panic("httpuow: nil manager")
	}
	return func(next http.Handler) http.Handler {
		if next == nil {
			panic("httpuow: nil handler")
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serve(manager, cfg, next, w, r)
		})
	}
}

// Wrap applies Config to one HTTP handler.
func Wrap(manager *uow.Manager, cfg Config, next http.Handler) http.Handler {
	return Middleware(manager, cfg)(next)
}

func serve(manager *uow.Manager, cfg Config, next http.Handler, w http.ResponseWriter, r *http.Request) {
	if r == nil {
		r = (&http.Request{}).WithContext(context.Background())
	}
	baseCtx := r.Context()
	if baseCtx == nil {
		baseCtx = context.Background()
	}

	execCfg, err := cfg.execution(r)
	if err != nil {
		cfg.handleError(w, r, err, http.StatusOK, false)
		return
	}
	baseCtx, err = cfg.decorateContext(baseCtx, r)
	if err != nil {
		cfg.handleError(w, r, err, http.StatusOK, false)
		return
	}

	if execCfg.Transactional != uow.TransactionalOff {
		serveBuffered(manager, cfg, execCfg, next, w, r, baseCtx)
		return
	}

	recorder := newResponseRecorder(w)
	runErr := manager.Run(baseCtx, execCfg, func(execCtx context.Context) error {
		next.ServeHTTP(recorder, r.WithContext(execCtx))
		if cfg.RollbackOnStatus != nil && cfg.RollbackOnStatus(recorder.StatusCode()) {
			return markRollbackOnly(execCtx, recorder.StatusCode())
		}
		return nil
	})
	if runErr != nil {
		cfg.handleError(recorder, r, runErr, recorder.StatusCode(), recorder.Started())
	}
}

func serveBuffered(manager *uow.Manager, cfg Config, execCfg uow.ExecutionConfig, next http.Handler, w http.ResponseWriter, r *http.Request, baseCtx context.Context) {
	buffered := newBufferedResponse()
	runErr := manager.Run(baseCtx, execCfg, func(execCtx context.Context) error {
		next.ServeHTTP(buffered, r.WithContext(execCtx))
		if cfg.RollbackOnStatus != nil && cfg.RollbackOnStatus(buffered.StatusCode()) {
			return markRollbackOnly(execCtx, buffered.StatusCode())
		}
		return nil
	})
	if runErr != nil {
		cfg.handleError(w, r, runErr, buffered.StatusCode(), false)
		return
	}
	buffered.FlushTo(w)
}

func markRollbackOnly(ctx context.Context, statusCode int) error {
	work, ok := uow.From(ctx)
	if !ok || !work.InTransaction() {
		return nil
	}
	if err := work.SetRollbackOnly(&StatusError{StatusCode: statusCode}); err != nil && !errors.Is(err, uow.ErrNoActiveTransaction) {
		return err
	}
	return nil
}

type responseRecorder struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
	written     int64
}

func newResponseRecorder(w http.ResponseWriter) *responseRecorder {
	return &responseRecorder{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
	}
}

func (r *responseRecorder) Header() http.Header {
	return r.ResponseWriter.Header()
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	if r.wroteHeader {
		r.ResponseWriter.WriteHeader(statusCode)
		return
	}
	r.statusCode = statusCode
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(statusCode)
}

func (r *responseRecorder) Write(data []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(data)
	r.written += int64(n)
	return n, err
}

func (r *responseRecorder) ReadFrom(src io.Reader) (int64, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if readerFrom, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		n, err := readerFrom.ReadFrom(src)
		r.written += n
		return n, err
	}
	n, err := io.Copy(r.ResponseWriter, src)
	r.written += n
	return n, err
}

func (r *responseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		if !r.wroteHeader {
			r.WriteHeader(http.StatusOK)
		}
		flusher.Flush()
	}
}

func (r *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("httpuow: response writer does not implement http.Hijacker")
	}
	return hijacker.Hijack()
}

func (r *responseRecorder) Push(target string, opts *http.PushOptions) error {
	pusher, ok := r.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (r *responseRecorder) StatusCode() int {
	return r.statusCode
}

func (r *responseRecorder) Started() bool {
	return r.wroteHeader || r.written > 0
}

type bufferedResponse struct {
	header      http.Header
	body        bytes.Buffer
	statusCode  int
	wroteHeader bool
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{
		header:     make(http.Header),
		statusCode: http.StatusOK,
	}
}

func (b *bufferedResponse) Header() http.Header {
	return b.header
}

func (b *bufferedResponse) WriteHeader(statusCode int) {
	if b.wroteHeader {
		return
	}
	b.statusCode = statusCode
	b.wroteHeader = true
}

func (b *bufferedResponse) Write(data []byte) (int, error) {
	if !b.wroteHeader {
		b.WriteHeader(http.StatusOK)
	}
	return b.body.Write(data)
}

func (b *bufferedResponse) StatusCode() int {
	return b.statusCode
}

func (b *bufferedResponse) FlushTo(w http.ResponseWriter) {
	copyHeaders(w.Header(), b.header)
	w.WriteHeader(b.statusCode)
	if b.body.Len() == 0 {
		return
	}
	_, _ = w.Write(b.body.Bytes())
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		copied := make([]string, len(values))
		copy(copied, values)
		dst[key] = copied
	}
}
