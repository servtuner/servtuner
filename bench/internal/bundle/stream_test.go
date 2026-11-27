package bundle

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/storage"
)

const crashDirEnv = "BUNDLE_STREAM_CRASH_DIR"

var streamEpoch = time.Date(2026, 10, 28, 7, 0, 0, 0, time.UTC)

func numberedRow(n int) engine.RequestRow {
	ttft := float64(n) / 1000
	tokens, stage := int64(n), int64(n)
	scheduled := streamEpoch.Add(time.Duration(n) * time.Millisecond)
	return engine.RequestRow{
		ScheduledAt: &scheduled,
		SentAt:      streamEpoch.Add(time.Duration(n) * time.Millisecond),
		TTFTSeconds: &ttft, OutputTokens: &tokens,
		ITLSeconds: []float64{0.01, 0.02}, E2ESeconds: 1, Status: 200,
		Phase:      engine.RowPhaseStage,
		StageIndex: &stage,
	}
}

// collectRequests reads every row and returns each row's StageIndex, which
// numberedRow sets to the row number.
func collectRequests(t *testing.T, dir string) []int64 {
	t.Helper()
	var numbers []int64
	for row, err := range ReadRequestRows(dir) {
		if err != nil {
			t.Fatal(err)
		}
		numbers = append(numbers, *row.StageIndex)
	}
	return numbers
}

func sequence(count int) []int64 {
	numbers := make([]int64, count)
	for i := range numbers {
		numbers[i] = int64(i)
	}
	return numbers
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStreamRoundTripKeepsFieldsAndOrder(t *testing.T) {
	dir := t.TempDir()
	stream, err := NewRequestStream(dir, StreamOptions{FlushRows: 10})
	if err != nil {
		t.Fatal(err)
	}
	for n := range 35 {
		if err := stream.Write(numberedRow(n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, RequestsParquet+PartsDirSuffix)); !os.IsNotExist(err) {
		t.Errorf("part directory still exists after Close: %v", err)
	}
	var got []engine.RequestRow
	for row, err := range ReadRequestRows(dir) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	if len(got) != 35 {
		t.Fatalf("rows = %d, want 35", len(got))
	}
	want := numberedRow(7)
	row := got[7]
	if !row.ScheduledAt.Equal(*want.ScheduledAt) || *row.TTFTSeconds != *want.TTFTSeconds || *row.OutputTokens != 7 ||
		row.TTFBSeconds != nil || !slices.Equal(row.ITLSeconds, want.ITLSeconds) || row.Status != 200 {
		t.Errorf("row 7 = %+v", row)
	}
	if !slices.Equal(collectRequests(t, dir), sequence(35)) {
		t.Error("rows came back out of order")
	}
	content, err := os.ReadFile(filepath.Join(dir, RequestsParquet))
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := ReadRequests(content); err != nil || len(rows) != 35 {
		t.Errorf("ReadRequests on the finalized file = %d rows, %v", len(rows), err)
	}
	if err := stream.Write(numberedRow(0)); err == nil {
		t.Error("Write after Close succeeded")
	}
	if err := stream.Close(); err != nil {
		t.Errorf("second Close = %v", err)
	}
}

func TestStreamFlushesOnRowCount(t *testing.T) {
	stream, err := NewRequestStream(t.TempDir(), StreamOptions{FlushRows: 100, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for n := range 250 {
		_ = stream.Write(numberedRow(n))
	}
	waitFor(t, "two parts", func() bool { return stream.Stats().Parts == 2 })
	time.Sleep(50 * time.Millisecond)
	if stats := stream.Stats(); stats.Flushed != 200 || stats.Parts != 2 {
		t.Errorf("stats = %+v, want 200 rows in 2 parts (50 still buffered)", stats)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamFlushesOnInterval(t *testing.T) {
	stream, err := NewRequestStream(t.TempDir(), StreamOptions{FlushRows: 1_000_000, FlushInterval: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	for n := range 3 {
		_ = stream.Write(numberedRow(n))
	}
	waitFor(t, "time-based flush", func() bool { return stream.Stats().Flushed == 3 })
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestStreamCrashHelper is the child of TestStreamKeepsFlushedRowsAfterKill. It
// does nothing in a normal run.
func TestStreamCrashHelper(t *testing.T) {
	dir := os.Getenv(crashDirEnv)
	if dir == "" {
		t.Skip("helper process for TestStreamKeepsFlushedRowsAfterKill")
	}
	stream, err := NewRequestStream(dir, StreamOptions{FlushRows: 1000, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for n := range 2500 {
		_ = stream.Write(numberedRow(n))
	}
	for stream.Stats().Flushed != 2000 {
		time.Sleep(time.Millisecond)
	}
	os.Stdout.WriteString("flushed\n")
	time.Sleep(time.Minute) // never Close: the parent kills this process
}

func TestStreamKeepsFlushedRowsAfterKill(t *testing.T) {
	dir := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestStreamCrashHelper$")
	command.Env = append(os.Environ(), crashDirEnv+"="+dir)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "flushed\n" {
		_ = command.Process.Kill()
		t.Fatalf("helper said %q, %v", line, err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()

	if _, err := os.Stat(filepath.Join(dir, RequestsParquet)); err == nil {
		t.Fatal("killed process left a finalized file")
	}
	if got := collectRequests(t, dir); !slices.Equal(got, sequence(2000)) {
		t.Fatalf("recovered %d rows, want the 2000 flushed ones in order", len(got))
	}
	path := filepath.Join(dir, RequestsParquet)
	if err := Recover[engine.RequestRow](path, 1000); err != nil {
		t.Fatal(err)
	}
	if got := collectRequests(t, dir); !slices.Equal(got, sequence(2000)) {
		t.Fatalf("after Recover: %d rows, want 2000", len(got))
	}
	if _, err := os.Stat(path + PartsDirSuffix); !os.IsNotExist(err) {
		t.Errorf("Recover left the part directory: %v", err)
	}
}

func TestReaderSkipsAnUnfinishedLastPartAndTempFiles(t *testing.T) {
	dir := t.TempDir()
	stream, err := NewRequestStream(dir, StreamOptions{FlushRows: 10, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for n := range 30 {
		_ = stream.Write(numberedRow(n))
	}
	waitFor(t, "three parts", func() bool { return stream.Stats().Parts == 3 })
	// No Close: the directory is what a crash leaves. Cut the last part short
	// and leave a temp file, as a disk that lost data at the crash would.
	parts := filepath.Join(dir, RequestsParquet+PartsDirSuffix)
	last := filepath.Join(parts, partName(2))
	content, err := os.ReadFile(last)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(last, content[:len(content)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parts, partName(3)+tempExtension), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := collectRequests(t, dir); !slices.Equal(got, sequence(20)) {
		t.Errorf("recovered %d rows, want the 20 in the intact parts", len(got))
	}
	// An unreadable part that is not the last is corruption, not a crash.
	if err := os.WriteFile(filepath.Join(parts, partName(0)), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	var sawError bool
	for _, err := range ReadRequestRows(dir) {
		sawError = sawError || err != nil
	}
	if !sawError {
		t.Error("a corrupt middle part was read without an error")
	}
}

func TestReadOfMissingFileIsAnError(t *testing.T) {
	for _, err := range ReadRequestRows(t.TempDir()) {
		if err == nil {
			t.Fatal("expected an error")
		}
		return
	}
	t.Fatal("no iteration result")
}

func TestStreamConcurrentWritersLoseNothingWhenTheQueueIsFull(t *testing.T) {
	dir := t.TempDir()
	stream, err := NewRequestStream(dir, StreamOptions{FlushRows: 50, QueueSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	const writers, perWriter = 8, 500
	var group sync.WaitGroup
	for w := range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			for n := range perWriter {
				if err := stream.Write(numberedRow(w*perWriter + n)); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	group.Wait()
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	stats := stream.Stats()
	if stats.Accepted != writers*perWriter || stats.Flushed != writers*perWriter || stats.Lost != 0 {
		t.Errorf("stats = %+v", stats)
	}
	got := collectRequests(t, dir)
	slices.Sort(got)
	if !slices.Equal(got, sequence(writers*perWriter)) {
		t.Errorf("read back %d rows, want %d distinct", len(got), writers*perWriter)
	}
	t.Logf("writers blocked on a full queue %d times", stats.Blocked)
}

func TestStreamFlushFailureIsStickyAndCounted(t *testing.T) {
	dir := t.TempDir()
	stream, err := NewRequestStream(dir, StreamOptions{FlushRows: 5, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, RequestsParquet+PartsDirSuffix)); err != nil {
		t.Fatal(err)
	}
	for n := range 5 {
		_ = stream.Write(numberedRow(n))
	}
	waitFor(t, "failure to surface", func() bool { return stream.Write(numberedRow(0)) != nil })
	if err := stream.Close(); err == nil {
		t.Error("Close hid the flush failure")
	}
	if stream.Stats().Lost == 0 {
		t.Error("rows that were not persisted were not counted as lost")
	}
}

func TestStreamRefusesToMixRuns(t *testing.T) {
	dir := t.TempDir()
	stream, err := NewRequestStream(dir, StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRequestStream(dir, StreamOptions{}); err == nil {
		t.Error("second stream over a finalized file was allowed")
	}
}

func TestClientSamplesStreamLikeRequests(t *testing.T) {
	dir := t.TempDir()
	stream, err := NewClientStream(dir, StreamOptions{FlushRows: 3})
	if err != nil {
		t.Fatal(err)
	}
	stage := int64(2)
	for n := range 7 {
		sample := engine.ClientSampleRow{SampledAt: streamEpoch.Add(time.Duration(n) * time.Second), Source: "loadgen", Goroutines: int64(n), InFlightRequests: 4, SendLagMaxSeconds: 0.5, SendLagP99Seconds: 0.1}
		if n == 6 {
			sample.StageIndex = &stage
		}
		if err := stream.WriteSample(sample); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	var got []engine.ClientSampleRow
	for sample, err := range ReadClientSamples(dir) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, sample)
	}
	if len(got) != 7 || got[6].Goroutines != 6 || got[6].StageIndex == nil || *got[6].StageIndex != 2 || got[0].StageIndex != nil || got[3].Source != "loadgen" {
		t.Errorf("samples = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "client", "samples.parquet")); err != nil {
		t.Error(err)
	}
}

// TestClientSampleColumnsMatchSchema checks the Parquet columns against
// client-sample.schema.json the way the request record is checked.
func TestClientSampleColumnsMatchSchema(t *testing.T) {
	dir := t.TempDir()
	stream, err := NewClientStream(dir, StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "client", "samples.parquet"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := parquet.OpenFile(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	properties, required := loadSchemaProperties(t, "../../../schema/json/bundle/client-sample.schema.json")
	columns := map[string]parquet.Field{}
	for _, field := range file.Schema().Fields() {
		columns[field.Name()] = field
	}
	for name, property := range properties {
		column, found := columns[name]
		if !found {
			t.Errorf("schema property %s has no parquet column", name)
			continue
		}
		if column.Optional() == slices.Contains(required, name) {
			t.Errorf("%s: optional = %v but required = %v", name, column.Optional(), slices.Contains(required, name))
		}
		checkColumnType(t, name, property, column)
	}
	for name := range columns {
		if _, found := properties[name]; !found {
			t.Errorf("parquet column %s is not a schema property", name)
		}
	}
}

func TestRecordExistingFileChecksumsAStreamIntoTheManifest(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stream, err := NewRequestStream(root, StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Write(numberedRow(1))
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	writer := NewWriter(storage.NewFileStore(root))
	if err := writer.RecordExistingFile(ctx, RequestsParquet); err != nil {
		t.Fatal(err)
	}
	if len(writer.files) != 1 || writer.files[0].Path != RequestsParquet || writer.files[0].SizeBytes == 0 || len(writer.files[0].Sha256) != 64 {
		t.Errorf("files = %+v", writer.files)
	}
	if err := writer.RecordExistingFile(ctx, "missing"); err == nil {
		t.Error("missing file recorded")
	}
}

// TestStreamMemoryStaysBoundedFor1MRows streams a million rows, each carrying a
// 16-value ITL list, and checks the live heap never grows with the row count:
// holding them all would take hundreds of MB, the stream holds one buffer.
func TestStreamMemoryStaysBoundedFor1MRows(t *testing.T) {
	const total = 1_000_000
	const limit = 64 << 20
	dir := t.TempDir()
	stream, err := NewRequestStream(dir, StreamOptions{FlushRows: 8192, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	liveHeap := func() uint64 {
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		return stats.HeapAlloc
	}
	baseline := liveHeap()
	var peak uint64
	for n := range total {
		row := numberedRow(n)
		row.ITLSeconds = make([]float64, 16)
		if err := stream.Write(row); err != nil {
			t.Fatal(err)
		}
		if n%100_000 == 99_999 {
			peak = max(peak, liveHeap())
		}
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	peak = max(peak, liveHeap())
	growth := int64(peak) - int64(baseline)
	t.Logf("heap growth over %d rows: %d KiB, %d parts, %d blocked writes", total, growth/1024, stream.Stats().Parts, stream.Stats().Blocked)
	if growth > limit {
		t.Errorf("heap grew by %d MiB, limit %d MiB", growth>>20, limit>>20)
	}
	count := 0
	for _, err := range ReadRequestRows(dir) {
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != total {
		t.Errorf("read back %d rows, want %d", count, total)
	}
}
