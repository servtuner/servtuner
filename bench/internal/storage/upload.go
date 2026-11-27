package storage

import (
	"context"
	"fmt"
	"slices"
)

// Upload copies every object of from into to, with last copied after all the
// others: the manifest, so a reader of to never indexes a half-copied bundle.
func Upload(ctx context.Context, from, to BundleStore, last string) error {
	keys, err := from.List(ctx, "")
	if err != nil {
		return err
	}
	keys = slices.DeleteFunc(keys, func(key string) bool { return key == last })
	if _, err := from.Stat(ctx, last); err == nil {
		keys = append(keys, last)
	}
	for _, key := range keys {
		if err := copyObject(ctx, from, to, key); err != nil {
			return err
		}
	}
	return nil
}

func copyObject(ctx context.Context, from, to BundleStore, key string) error {
	body, err := from.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("upload %s: %w", key, err)
	}
	defer body.Close()
	return to.Put(ctx, key, body)
}
