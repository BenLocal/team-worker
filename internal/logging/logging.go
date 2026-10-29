// Package logging builds team-workerd's logger: level, text or JSON format,
// and output to stderr or a size-rotated file.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Options struct {
	Level  string // debug, info, warn or error
	Format string // text or json
	// File is the log path; empty writes to stderr.
	File string
	// MaxSizeMB rotates the file when it would grow past this size;
	// MaxFiles is how many rotated files (file.1 … file.N) are kept.
	MaxSizeMB int
	MaxFiles  int
}

// New returns the logger and a closer for its file.
func New(opts Options) (*slog.Logger, io.Closer, error) {
	var level slog.Level
	switch strings.ToLower(opts.Level) {
	case "", "info":
		level = slog.LevelInfo
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, nil, fmt.Errorf("invalid log level %q: use debug, info, warn or error", opts.Level)
	}
	var out io.Writer = os.Stderr
	var closer io.Closer = io.NopCloser(nil)
	if opts.File != "" {
		w, err := OpenRotating(opts.File, opts.MaxSizeMB, opts.MaxFiles)
		if err != nil {
			return nil, nil, err
		}
		out, closer = w, w
	}
	handlerOpts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	switch strings.ToLower(opts.Format) {
	case "", "text":
		h = slog.NewTextHandler(out, handlerOpts)
	case "json":
		h = slog.NewJSONHandler(out, handlerOpts)
	default:
		closer.Close()
		return nil, nil, fmt.Errorf("invalid log format %q: use text or json", opts.Format)
	}
	return slog.New(h), closer, nil
}

// Rotating is a file writer that moves the file to file.1 (and older files
// one step further, dropping the oldest) when a write would pass the limit.
type Rotating struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	maxFiles int
	f        *os.File
	size     int64
}

func OpenRotating(path string, maxSizeMB, maxFiles int) (*Rotating, error) {
	if maxSizeMB <= 0 {
		maxSizeMB = 20
	}
	if maxFiles <= 0 {
		maxFiles = 5
	}
	r := &Rotating{path: path, maxBytes: int64(maxSizeMB) << 20, maxFiles: maxFiles}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return r, r.open()
}

func (r *Rotating) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *Rotating) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *Rotating) rotate() error {
	r.f.Close()
	os.Remove(fmt.Sprintf("%s.%d", r.path, r.maxFiles))
	for i := r.maxFiles - 1; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	if err := os.Rename(r.path, r.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return r.open()
}

func (r *Rotating) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}
