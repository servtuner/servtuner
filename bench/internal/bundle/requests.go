package bundle

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/parquet-go/parquet-go"

	"github.com/servtuner/servtuner/bench/internal/engine"
)

// RequestSink collects request rows into an in-memory requests.parquet.
type RequestSink struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
	writer *parquet.GenericWriter[engine.RequestRow]
	closed bool
}

var _ engine.Sink = (*RequestSink)(nil)

// NewRequestSink returns an empty sink.
func NewRequestSink() *RequestSink {
	sink := &RequestSink{}
	sink.writer = parquet.NewGenericWriter[engine.RequestRow](&sink.buffer)
	return sink
}

// Write appends one row.
func (s *RequestSink) Write(row engine.RequestRow) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.closed {
		return errors.New("write request row: sink already finished")
	}
	if _, err := s.writer.Write([]engine.RequestRow{row}); err != nil {
		return fmt.Errorf("write request row: %w", err)
	}
	return nil
}

// Finish ends the file and returns its bytes. A sink with no rows still yields
// a valid Parquet file with the request-record columns.
func (s *RequestSink) Finish() ([]byte, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.closed {
		return nil, errors.New("finish requests.parquet: already finished")
	}
	s.closed = true
	if err := s.writer.Close(); err != nil {
		return nil, fmt.Errorf("finish requests.parquet: %w", err)
	}
	return s.buffer.Bytes(), nil
}

// ReadRequests decodes a requests.parquet file.
func ReadRequests(content []byte) ([]engine.RequestRow, error) {
	file, err := parquet.OpenFile(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, fmt.Errorf("open requests.parquet: %w", err)
	}
	reader := parquet.NewGenericReader[engine.RequestRow](file)
	defer reader.Close()
	var rows []engine.RequestRow
	batch := make([]engine.RequestRow, 256)
	for {
		count, err := reader.Read(batch)
		rows = append(rows, batch[:count]...)
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read requests.parquet: %w", err)
		}
	}
}

// ParquetColumnNames returns the top-level column names of a Parquet file, for
// checking it against the request-record schema.
func ParquetColumnNames(content []byte) ([]string, error) {
	file, err := parquet.OpenFile(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, fmt.Errorf("open requests.parquet: %w", err)
	}
	var names []string
	for _, field := range file.Schema().Fields() {
		names = append(names, field.Name())
	}
	return names, nil
}
