package bundle

import (
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/servtuner/servtuner/bench/internal/engine"
)

// Defaults for StreamOptions.
const (
	DefaultFlushRows     = 8192
	DefaultFlushInterval = 5 * time.Second
	DefaultQueueSize     = 4096
)

// ErrStreamClosed is returned for a write after Close.
var ErrStreamClosed = errors.New("stream already closed")

const (
	partPrefix    = "part-"
	partExtension = ".parquet"
	tempExtension = ".tmp"
	readBatchSize = 1024
)

// StreamOptions tunes a Stream. Zero values take the defaults.
type StreamOptions struct {
	// FlushRows flushes a part when this many rows are buffered.
	FlushRows int
	// FlushInterval flushes a part when rows have waited this long.
	FlushInterval time.Duration
	// QueueSize is the capacity of the channel between writers and the
	// goroutine that owns the buffer.
	QueueSize int
}

func (o StreamOptions) withDefaults() StreamOptions {
	if o.FlushRows <= 0 {
		o.FlushRows = DefaultFlushRows
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = DefaultFlushInterval
	}
	if o.QueueSize <= 0 {
		o.QueueSize = DefaultQueueSize
	}
	return o
}

// StreamStats is a snapshot of a Stream's counters.
type StreamStats struct {
	// Accepted rows were queued by Write.
	Accepted int64
	// Flushed rows are durable: they sit in a complete, fsynced part file.
	Flushed int64
	// Parts is the number of part files written.
	Parts int64
	// Blocked counts Writes that found the queue full and waited for room.
	Blocked int64
	// Lost counts queued rows that could not be persisted because a flush
	// failed. It is non-zero only together with an error from Close.
	Lost int64
}

// Stream appends rows of type T to a Parquet file (requests.parquet or
// client/samples.parquet) while a run is in progress, with bounded memory.
//
// Design. A Parquet file is only readable once its footer is written, and the
// footer is written at close, so a single growing file would lose every row if
// bench were killed. Instead the rows go to a sequence of small, complete
// Parquet part files in <name>.d/part-NNNN.parquet, each holding one row group.
// A part is written to a temp file, fsynced and renamed, so a part either
// exists whole or not at all. Close compacts the parts, in order, into one
// <name> file (also written atomically) and removes the part directory. After a
// crash the part directory is what is left: ReadRows and Recover read it, and
// every flushed row survives; only rows still in the buffer are lost. The
// footer-recovery alternative was rejected because parquet-go cannot read a
// file without its footer.
//
// Flushing happens when FlushRows rows are buffered or FlushInterval has passed
// since the first buffered row, whichever comes first. Memory is bounded by the
// queue plus one buffer, whatever the run length.
//
// Back-pressure. Write sends the row on a buffered channel to one goroutine
// that owns the buffer and the files. When the channel is full Write blocks
// until there is room and counts the event in StreamStats.Blocked; a row is
// never dropped. If a flush fails, the failure is sticky: later Writes return
// it, queued rows are counted in StreamStats.Lost, and Close returns it.
//
// A Stream is safe for concurrent use.
type Stream[T any] struct {
	dir     string // the <name>.d part directory
	final   string // the finalized file
	options StreamOptions

	mutex  sync.RWMutex // guards closed against sends on rows
	closed bool
	rows   chan T
	done   chan struct{}

	failure atomic.Pointer[error]

	accepted, flushed, parts, blocked, lost atomic.Int64

	closeOnce sync.Once
	closeErr  error
}

// NewStream starts a stream whose finalized file is path and whose parts go to
// path+PartsDirSuffix. It fails if either already exists, so a stream never
// mixes rows of two runs.
func NewStream[T any](path string, options StreamOptions) (*Stream[T], error) {
	partDir := path + PartsDirSuffix
	for _, existing := range []string{path, partDir} {
		if _, err := os.Stat(existing); err == nil {
			return nil, fmt.Errorf("start stream: %s already exists", existing)
		}
	}
	if err := os.MkdirAll(partDir, 0o755); err != nil {
		return nil, fmt.Errorf("start stream: %w", err)
	}
	options = options.withDefaults()
	stream := &Stream[T]{
		dir:     partDir,
		final:   path,
		options: options,
		rows:    make(chan T, options.QueueSize),
		done:    make(chan struct{}),
	}
	go stream.run()
	return stream, nil
}

// Write queues one row. It blocks while the queue is full and never drops the
// row. It returns ErrStreamClosed after Close and the flush failure, if any.
func (s *Stream[T]) Write(row T) error {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	if s.closed {
		return ErrStreamClosed
	}
	if failure := s.failure.Load(); failure != nil {
		return *failure
	}
	select {
	case s.rows <- row:
	default:
		s.blocked.Add(1)
		s.rows <- row
	}
	s.accepted.Add(1)
	return nil
}

// Stats returns the current counters.
func (s *Stream[T]) Stats() StreamStats {
	return StreamStats{
		Accepted: s.accepted.Load(),
		Flushed:  s.flushed.Load(),
		Parts:    s.parts.Load(),
		Blocked:  s.blocked.Load(),
		Lost:     s.lost.Load(),
	}
}

// Close flushes what is buffered, writes the finalized file and removes the
// part directory. It returns the first failure of the stream. Calling it again
// returns the same result.
func (s *Stream[T]) Close() error {
	s.closeOnce.Do(func() {
		s.mutex.Lock()
		s.closed = true
		close(s.rows)
		s.mutex.Unlock()
		<-s.done
		if failure := s.failure.Load(); failure != nil {
			s.closeErr = *failure
			return
		}
		s.closeErr = finalizeParts[T](s.final, s.options.FlushRows)
	})
	return s.closeErr
}

func (s *Stream[T]) fail(err error) {
	s.failure.CompareAndSwap(nil, &err)
}

// run is the goroutine that owns the buffer.
func (s *Stream[T]) run() {
	defer close(s.done)
	buffer := make([]T, 0, s.options.FlushRows)
	timer := time.NewTimer(s.options.FlushInterval)
	defer timer.Stop()
	timer.Stop()

	flush := func() {
		if len(buffer) == 0 {
			return
		}
		if s.failure.Load() == nil {
			if err := writePart(s.dir, int(s.parts.Load()), buffer); err != nil {
				s.fail(err)
			} else {
				s.parts.Add(1)
				s.flushed.Add(int64(len(buffer)))
			}
		}
		if s.failure.Load() != nil && int64(len(buffer)) > 0 {
			s.lost.Add(int64(len(buffer)))
		}
		clear(buffer) // release slices held by rows
		buffer = buffer[:0]
	}

	for {
		select {
		case row, open := <-s.rows:
			if !open {
				flush()
				return
			}
			if len(buffer) == 0 {
				timer.Reset(s.options.FlushInterval)
			}
			buffer = append(buffer, row)
			if len(buffer) >= s.options.FlushRows {
				timer.Stop()
				flush()
			}
		case <-timer.C:
			flush()
		}
	}
}

// partName is the file name of part number index; four digits sort correctly up
// to 10000 parts and the reader sorts numerically beyond that.
func partName(index int) string {
	return fmt.Sprintf("%s%04d%s", partPrefix, index, partExtension)
}

// writePart writes rows as one complete Parquet file, atomically.
func writePart[T any](dir string, index int, rows []T) (err error) {
	final := filepath.Join(dir, partName(index))
	temp := final + tempExtension
	return writeParquetAtomically(final, temp, func(file io.Writer) error {
		writer := parquet.NewGenericWriter[T](file)
		if _, err := writer.Write(rows); err != nil {
			return err
		}
		return writer.Close()
	})
}

// writeParquetAtomically writes through temp, fsyncs, renames to final and
// fsyncs the directory.
func writeParquetAtomically(final, temp string, write func(io.Writer) error) (err error) {
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("write %s: %w", final, err)
	}
	defer func() {
		if err != nil {
			_ = file.Close()
			_ = os.Remove(temp)
		}
	}()
	if err = write(file); err != nil {
		return fmt.Errorf("write %s: %w", final, err)
	}
	if err = file.Sync(); err != nil {
		return fmt.Errorf("write %s: %w", final, err)
	}
	if err = file.Close(); err != nil {
		return fmt.Errorf("write %s: %w", final, err)
	}
	if err = os.Rename(temp, final); err != nil {
		return fmt.Errorf("write %s: %w", final, err)
	}
	return syncDir(filepath.Dir(final))
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

// listParts returns the complete part files of the directory for path, in
// order. Temp files (a write that never finished) are not parts.
func listParts(path string) ([]string, error) {
	entries, err := os.ReadDir(path + PartsDirSuffix)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, partPrefix) && strings.HasSuffix(name, partExtension) {
			names = append(names, name)
		}
	}
	// Numeric order: shorter names first, then lexical, so part-10000 follows part-9999.
	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) != len(names[j]) {
			return len(names[i]) < len(names[j])
		}
		return names[i] < names[j]
	})
	paths := make([]string, len(names))
	for i, name := range names {
		paths[i] = filepath.Join(path+PartsDirSuffix, name)
	}
	return paths, nil
}

// readFile yields every row of one Parquet file.
func readFile[T any](path string) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		file, err := os.Open(path)
		if err != nil {
			yield(zero, err)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			yield(zero, err)
			return
		}
		parquetFile, err := parquet.OpenFile(file, info.Size())
		if err != nil {
			yield(zero, fmt.Errorf("open %s: %w", filepath.Base(path), err))
			return
		}
		reader := parquet.NewGenericReader[T](parquetFile)
		defer reader.Close()
		batch := make([]T, readBatchSize)
		for {
			count, err := reader.Read(batch)
			for _, row := range batch[:count] {
				if !yield(row, nil) {
					return
				}
			}
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				yield(zero, fmt.Errorf("read %s: %w", filepath.Base(path), err))
				return
			}
		}
	}
}

// ReadRows yields every row recorded for path (for example the requests.parquet
// of a bundle directory), in write order. It reads the finalized file when it
// exists. Otherwise it reads the part files a crashed run left behind, which
// holds every flushed row; a last part that cannot be read, from a disk that
// lost data at the crash, ends the iteration quietly, as that part was being
// written when the run died. A path with neither yields nothing and an error.
func ReadRows[T any](path string) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		if _, err := os.Stat(path); err == nil {
			for row, err := range readFile[T](path) {
				if !yield(row, err) || err != nil {
					return
				}
			}
			return
		}
		parts, err := listParts(path)
		if err != nil {
			yield(zero, err)
			return
		}
		if parts == nil {
			yield(zero, fmt.Errorf("read %s: %w", filepath.Base(path), os.ErrNotExist))
			return
		}
		for index, part := range parts {
			isLast := index == len(parts)-1
			if isLast && !partReadable[T](part) {
				return
			}
			for row, err := range readFile[T](part) {
				if !yield(row, err) || err != nil {
					return
				}
			}
		}
	}
}

// partReadable reports whether a part opens as Parquet.
func partReadable[T any](path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false
	}
	_, err = parquet.OpenFile(file, info.Size())
	return err == nil
}

// Recover finalizes a stream that was never closed (a crash): it compacts the
// part files into path and removes the part directory, keeping every flushed
// row. It does nothing when path already exists.
func Recover[T any](path string, rowsPerGroup int) error {
	if _, err := os.Stat(path); err == nil {
		_ = os.RemoveAll(path + PartsDirSuffix)
		return nil
	}
	return finalizeParts[T](path, rowsPerGroup)
}

// finalizeParts compacts the parts into path through a streaming copy, so the
// memory used does not depend on the run length.
func finalizeParts[T any](path string, rowsPerGroup int) error {
	if rowsPerGroup <= 0 {
		rowsPerGroup = DefaultFlushRows
	}
	err := writeParquetAtomically(path, path+tempExtension, func(file io.Writer) error {
		writer := parquet.NewGenericWriter[T](file, parquet.MaxRowsPerRowGroup(int64(rowsPerGroup)))
		parts, err := listParts(path)
		if err != nil {
			return err
		}
		batch := make([]T, 0, readBatchSize)
		writeBatch := func() error {
			_, err := writer.Write(batch)
			clear(batch)
			batch = batch[:0]
			return err
		}
		for index, part := range parts {
			isLast := index == len(parts)-1
			if isLast && !partReadable[T](part) {
				break
			}
			for row, err := range readFile[T](part) {
				if err != nil {
					return err
				}
				batch = append(batch, row)
				if len(batch) == cap(batch) {
					if err := writeBatch(); err != nil {
						return err
					}
				}
			}
		}
		if err := writeBatch(); err != nil {
			return err
		}
		return writer.Close()
	})
	if err != nil {
		return fmt.Errorf("finalize %s: %w", filepath.Base(path), err)
	}
	if err := os.RemoveAll(path + PartsDirSuffix); err != nil {
		return fmt.Errorf("finalize %s: remove parts: %w", filepath.Base(path), err)
	}
	return nil
}

// RequestStream is the sink engines write request rows to.
type RequestStream struct{ *Stream[engine.RequestRow] }

var _ engine.Sink = RequestStream{}

// NewRequestStream streams to <bundleDir>/requests.parquet.
func NewRequestStream(bundleDir string, options StreamOptions) (RequestStream, error) {
	stream, err := NewStream[engine.RequestRow](filepath.Join(bundleDir, RequestsParquet), options)
	return RequestStream{stream}, err
}

// ReadRequestRows yields the request rows of a bundle directory, finalized or
// not.
func ReadRequestRows(bundleDir string) iter.Seq2[engine.RequestRow, error] {
	return ReadRows[engine.RequestRow](filepath.Join(bundleDir, RequestsParquet))
}

// ClientStream is the sink engines write client samples to.
type ClientStream struct {
	*Stream[engine.ClientSampleRow]
}

var _ engine.ClientSink = ClientStream{}

// WriteSample appends one sample.
func (s ClientStream) WriteSample(sample engine.ClientSampleRow) error { return s.Write(sample) }

// NewClientStream streams to <bundleDir>/client/samples.parquet.
func NewClientStream(bundleDir string, options StreamOptions) (ClientStream, error) {
	path := filepath.Join(bundleDir, filepath.FromSlash(ClientSamplesParquet))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ClientStream{}, fmt.Errorf("start client stream: %w", err)
	}
	stream, err := NewStream[engine.ClientSampleRow](path, options)
	return ClientStream{stream}, err
}

// ReadClientSamples yields the client samples of a bundle directory, finalized
// or not.
func ReadClientSamples(bundleDir string) iter.Seq2[engine.ClientSampleRow, error] {
	return ReadRows[engine.ClientSampleRow](filepath.Join(bundleDir, filepath.FromSlash(ClientSamplesParquet)))
}
