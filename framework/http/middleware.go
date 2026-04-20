package httpuow

import (
	"bufio"
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
		cfg.handleError(newResponseRecorder(w), r, err)
		return
	}
	baseCtx, err = cfg.decorateContext(baseCtx, r)
	if err != nil {
		cfg.handleError(newResponseRecorder(w), r, err)
		return
	}

	recorder := newResponseRecorder(w)
	runErr := manager.Do(baseCtx, execCfg, func(execCtx context.Context) error {
		next.ServeHTTP(recorder, r.WithContext(execCtx))
		if cfg.RollbackOnStatus != nil && cfg.RollbackOnStatus(recorder.StatusCode()) {
			return markRollbackOnly(execCtx, recorder.StatusCode())
		}
		return nil
	})
	if runErr != nil {
		cfg.handleError(recorder, r, runErr)
	}
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
