package bundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/storage"
)

// ErrWrite marks a failure to write the bundle. The CLI maps it to exit code 4.
var ErrWrite = errors.New("bundle write failed")

// ErrFinalized is returned for a write after the manifest: the manifest is the
// last file, so the api never indexes a half-written bundle.
var ErrFinalized = errors.New("bundle already finalized")

// Writer writes the files of one bundle through a BundleStore and remembers
// each file's checksum for the manifest.
type Writer struct {
	store     storage.BundleStore
	files     []spec.ManifestFile
	finalized bool
}

// NewWriter returns a writer whose keys are file names relative to the bundle.
func NewWriter(store storage.BundleStore) *Writer {
	return &Writer{store: store}
}

// WriteFile stores raw bytes under name and records its checksum.
func (w *Writer) WriteFile(ctx context.Context, name string, content []byte) error {
	if w.finalized {
		return fmt.Errorf("write %s: %w", name, ErrFinalized)
	}
	if err := w.put(ctx, name, content); err != nil {
		return err
	}
	checksum := sha256.Sum256(content)
	w.files = slices.DeleteFunc(w.files, func(file spec.ManifestFile) bool { return file.Path == name })
	w.files = append(w.files, spec.ManifestFile{Path: name, Sha256: hex.EncodeToString(checksum[:]), SizeBytes: len(content)})
	return nil
}

// RecordExistingFile adds a file already present in the store, such as a
// finalized stream (requests.parquet), to the manifest's checksum list. It
// reads the file back through the store, so the checksum is of what was stored.
func (w *Writer) RecordExistingFile(ctx context.Context, name string) error {
	if w.finalized {
		return fmt.Errorf("record %s: %w", name, ErrFinalized)
	}
	reader, err := w.store.Get(ctx, name)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrWrite, name, err)
	}
	defer reader.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, reader)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrWrite, name, err)
	}
	w.files = slices.DeleteFunc(w.files, func(file spec.ManifestFile) bool { return file.Path == name })
	w.files = append(w.files, spec.ManifestFile{Path: name, Sha256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: int(size)})
	return nil
}

// WriteDocument encodes document as indented JSON, checks it against its
// schema, and only then stores it. A file that breaks the contract is a bug in
// bench, so it is never written.
func (w *Writer) WriteDocument(ctx context.Context, name, schemaName string, document any) error {
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	encoded = append(encoded, '\n')
	if err := spec.Validate(schemaName, encoded); err != nil {
		return fmt.Errorf("%s does not match its schema (bench bug): %w", name, err)
	}
	return w.WriteFile(ctx, name, encoded)
}

// WriteManifest lists every file written so far, validates the manifest and
// stores it. It must be the last write: later writes fail with ErrFinalized.
func (w *Writer) WriteManifest(ctx context.Context, manifest spec.Manifest) error {
	manifest.BundleVersion = BundleVersion
	manifest.Files = slices.Clone(w.files)
	slices.SortFunc(manifest.Files, func(a, b spec.ManifestFile) int { return strings.Compare(a.Path, b.Path) })
	if manifest.Files == nil {
		manifest.Files = []spec.ManifestFile{}
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", ManifestJSON, err)
	}
	encoded = append(encoded, '\n')
	if err := spec.Validate(spec.SchemaManifest, encoded); err != nil {
		return fmt.Errorf("%s does not match its schema (bench bug): %w", ManifestJSON, err)
	}
	if w.finalized {
		return fmt.Errorf("write %s: %w", ManifestJSON, ErrFinalized)
	}
	if err := w.put(ctx, ManifestJSON, encoded); err != nil {
		return err
	}
	w.finalized = true
	return nil
}

func (w *Writer) put(ctx context.Context, name string, content []byte) error {
	if err := w.store.Put(ctx, name, bytes.NewReader(content)); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrWrite, name, err)
	}
	return nil
}
