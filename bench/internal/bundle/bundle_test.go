package bundle

import (
	"bytes"
	"strings"

	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/format"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/storage"
)

// recordingStore remembers the order of Put calls and can be told to fail.
type recordingStore struct {
	storage.BundleStore
	puts    []string
	failOn  string
	failure error
}

func (s *recordingStore) Put(ctx context.Context, key string, content io.Reader) error {
	if key == s.failOn {
		return s.failure
	}
	s.puts = append(s.puts, key)
	return s.BundleStore.Put(ctx, key, content)
}

func newRecordingStore(t *testing.T) *recordingStore {
	t.Helper()
	return &recordingStore{BundleStore: storage.NewFileStore(t.TempDir())}
}

func validManifest() spec.Manifest {
	return spec.Manifest{
		RunId:        "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Status:       spec.RunStatusSucceeded,
		StartedAt:    spec.NewUTCTimestamp(time.Date(2026, 10, 28, 7, 0, 0, 0, time.UTC)),
		BenchVersion: "test",
	}
}

func TestManifestIsWrittenLastWithChecksums(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore(t)
	writer := NewWriter(store)
	if err := writer.WriteFile(ctx, SpecYAML, []byte("name: x\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteDocument(ctx, PhasesJSON, spec.SchemaPhases, spec.Phases{Phases: []spec.Phase{}}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteManifest(ctx, validManifest()); err != nil {
		t.Fatal(err)
	}
	if want := []string{SpecYAML, PhasesJSON, ManifestJSON}; !slices.Equal(store.puts, want) {
		t.Fatalf("put order = %v, want %v", store.puts, want)
	}

	reader, err := store.Get(ctx, ManifestJSON)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	content, _ := io.ReadAll(reader)
	manifest, err := spec.Parse[spec.Manifest](spec.SchemaManifest, content)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 2 || manifest.Files[0].Path != PhasesJSON || manifest.Files[1].Path != SpecYAML {
		t.Fatalf("manifest files = %+v", manifest.Files)
	}
	wantChecksum := sha256.Sum256([]byte("name: x\n"))
	if got := manifest.Files[1].Sha256; got != hex.EncodeToString(wantChecksum[:]) {
		t.Errorf("checksum = %q", got)
	}
	if manifest.Files[1].SizeBytes != len("name: x\n") {
		t.Errorf("size = %d", manifest.Files[1].SizeBytes)
	}
}

func TestWritesAfterManifestAreRefused(t *testing.T) {
	ctx := context.Background()
	writer := NewWriter(storage.NewFileStore(t.TempDir()))
	if err := writer.WriteManifest(ctx, validManifest()); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteFile(ctx, SummaryJSON, []byte("{}")); !errors.Is(err, ErrFinalized) {
		t.Errorf("WriteFile after manifest = %v, want ErrFinalized", err)
	}
	if err := writer.WriteManifest(ctx, validManifest()); !errors.Is(err, ErrFinalized) {
		t.Errorf("second manifest = %v, want ErrFinalized", err)
	}
}

func TestStoreFailureIsABundleWriteError(t *testing.T) {
	store := newRecordingStore(t)
	store.failOn = SummaryJSON
	store.failure = errors.New("disk full")
	writer := NewWriter(store)
	err := writer.WriteFile(context.Background(), SummaryJSON, []byte("{}"))
	if !errors.Is(err, ErrWrite) {
		t.Errorf("error = %v, want ErrWrite", err)
	}
}

func TestInvalidDocumentIsNeverWritten(t *testing.T) {
	store := newRecordingStore(t)
	writer := NewWriter(store)
	err := writer.WriteDocument(context.Background(), SummaryJSON, spec.SchemaSummary, map[string]string{"unexpected": "field"})
	if err == nil {
		t.Fatal("document that breaks its schema was written")
	}
	if len(store.puts) != 0 {
		t.Errorf("puts = %v, want none", store.puts)
	}
}

// requestRecordSchemaPath is the one schema file; the Parquet columns must
// match it in name, optionality and type.
const requestRecordSchemaPath = "../../../schema/json/bundle/request-record.schema.json"

type schemaProperty struct {
	Type    string          `json:"type"`
	Ref     string          `json:"$ref"`
	Items   json.RawMessage `json:"items"`
	Minimum *float64        `json:"minimum"`
}

func loadRequestRecordSchema(t *testing.T) (properties map[string]schemaProperty, required []string) {
	t.Helper()
	return loadSchemaProperties(t, requestRecordSchemaPath)
}

func loadSchemaProperties(t *testing.T, path string) (properties map[string]schemaProperty, required []string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]schemaProperty `json:"properties"`
		Required   []string                  `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	return schema.Properties, schema.Required
}

// TestEmptyRequestsParquetMatchesRequestRecordSchema derives the expected
// column set, optionality and physical type from request-record.schema.json:
// required properties are non-optional columns, integers are INT64, numbers
// are DOUBLE, arrays are lists, and timestamps (a $ref to utcTimestamp) are
// INT64 TIMESTAMP.
func TestEmptyRequestsParquetMatchesRequestRecordSchema(t *testing.T) {
	content, err := NewRequestSink().Finish()
	if err != nil {
		t.Fatal(err)
	}
	file, err := parquet.OpenFile(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	properties, required := loadRequestRecordSchema(t)

	columns := map[string]parquet.Field{}
	for _, field := range file.Schema().Fields() {
		columns[field.Name()] = field
	}
	for name := range properties {
		if _, found := columns[name]; !found {
			t.Errorf("schema property %s has no parquet column", name)
		}
	}
	var optionalColumns []string
	for name, column := range columns {
		property, found := properties[name]
		if !found {
			t.Errorf("parquet column %s is not a schema property", name)
			continue
		}
		if column.Optional() {
			optionalColumns = append(optionalColumns, name)
		}
		checkColumnType(t, name, property, column)
	}
	var optionalProperties []string
	for name := range properties {
		if !slices.Contains(required, name) {
			optionalProperties = append(optionalProperties, name)
		}
	}
	slices.Sort(optionalColumns)
	slices.Sort(optionalProperties)
	// A list column is "optional" in the sense of the schema when it is absent
	// from required; parquet represents that with an empty list, not NULL.
	optionalColumns = slices.DeleteFunc(optionalColumns, func(name string) bool { return properties[name].Type == "array" })
	optionalProperties = slices.DeleteFunc(optionalProperties, func(name string) bool { return properties[name].Type == "array" })
	if !slices.Equal(optionalColumns, optionalProperties) {
		t.Errorf("optional parquet columns %v != schema properties outside required %v", optionalColumns, optionalProperties)
	}

	rows, err := ReadRequests(content)
	if err != nil || len(rows) != 0 {
		t.Errorf("empty file read back as %d rows, %v", len(rows), err)
	}
}

func isLogicalType[T format.LogicalTypeValue](logical *format.LogicalType) bool {
	if logical == nil {
		return false
	}
	_, matches := logical.Value.(T)
	return matches
}

func checkColumnType(t *testing.T, name string, property schemaProperty, column parquet.Field) {
	t.Helper()
	logical := column.Type().LogicalType()
	switch {
	case strings.HasSuffix(property.Ref, "#/definitions/utcTimestamp"):
		if column.Type().Kind() != parquet.Int64 || !isLogicalType[*format.TimestampType](logical) {
			t.Errorf("%s: schema timestamp must be INT64 TIMESTAMP, got %v", name, column.Type())
		}
	case property.Type == "integer":
		if column.Type().Kind() != parquet.Int64 {
			t.Errorf("%s: schema integer must be INT64, got %v", name, column.Type())
		}
	case property.Type == "number":
		if column.Type().Kind() != parquet.Double {
			t.Errorf("%s: schema number must be DOUBLE, got %v", name, column.Type())
		}
	case property.Type == "string":
		if column.Type().Kind() != parquet.ByteArray || !isLogicalType[*format.StringType](logical) {
			t.Errorf("%s: schema string must be a UTF8 BYTE_ARRAY, got %v", name, column.Type())
		}
	case property.Type == "array":
		if !isLogicalType[*format.ListType](logical) {
			t.Errorf("%s: schema array must be a LIST, got %v", name, column.Type())
			return
		}
		element := column.Fields()[0].Fields()[0] // list > repeated group > element
		if element.Type().Kind() != parquet.Double {
			t.Errorf("%s: list of numbers must hold DOUBLE, got %v", name, element.Type())
		}
	default:
		t.Errorf("%s: test does not know schema type %+v", name, property)
	}
}

func TestRequestRowsRoundTrip(t *testing.T) {
	scheduled := time.Date(2026, 10, 28, 7, 0, 0, 5, time.UTC)
	ttft := 0.25
	tokens := int64(7)
	failure := "timeout"
	sink := NewRequestSink()
	lag, firstContent := 0.001, 0.9
	stageOne, stageZero := int64(1), int64(0)
	rows := []engine.RequestRow{
		{ScheduledAt: &scheduled, SentAt: scheduled.Add(time.Millisecond), SendLagSeconds: &lag, TTFTSeconds: &ttft, FirstContentSeconds: &firstContent, ITLSeconds: []float64{0.01, 0.02}, E2ESeconds: 1.5, OutputTokens: &tokens, Status: 200, Phase: engine.RowPhaseStage, StageIndex: &stageOne},
		{ScheduledAt: &scheduled, SentAt: scheduled, E2ESeconds: 0.1, Error: &failure, Phase: engine.RowPhaseStage, StageIndex: &stageZero},
		{SentAt: scheduled, E2ESeconds: 0.2, Status: 200, Phase: engine.RowPhaseWarmup},
	}
	for _, row := range rows {
		if err := sink.Write(row); err != nil {
			t.Fatal(err)
		}
	}
	content, err := sink.Finish()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadRequests(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Status != 200 || got[0].StageIndex == nil || *got[0].StageIndex != 1 || len(got[0].ITLSeconds) != 2 ||
		got[0].TTFTSeconds == nil || *got[0].TTFTSeconds != ttft || got[0].ScheduledAt == nil || !got[0].ScheduledAt.Equal(scheduled) ||
		got[0].FirstContentSeconds == nil || *got[0].FirstContentSeconds != firstContent || got[0].Phase != engine.RowPhaseStage ||
		got[1].Error == nil || *got[1].Error != failure || got[1].TTFTSeconds != nil || got[1].FirstContentSeconds != nil {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if warmup := got[2]; warmup.Phase != engine.RowPhaseWarmup || warmup.StageIndex != nil || warmup.ScheduledAt != nil || warmup.SendLagSeconds != nil {
		t.Errorf("warmup row must keep its nulls: %+v", warmup)
	}
	if err := sink.Write(rows[0]); err == nil {
		t.Error("write after Finish accepted")
	}
}
