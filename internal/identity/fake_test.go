package identity

import (
	"context"
	"sync"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// fakeObjectStore is an in-memory ObjectStore for tests. It records call counts
// and can be primed to fail Get/Put/Delete to exercise error paths.
type fakeObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte

	gets    int
	puts    int
	deletes int

	getErr func(key string) error // optional override for GetObject
	putErr error                  // forced error for PutObject
	delErr error                  // forced error for DeleteObject
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: map[string][]byte{}}
}

func (f *fakeObjectStore) GetObject(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.getErr != nil {
		if err := f.getErr(key); err != nil {
			return nil, err
		}
	}
	data, ok := f.objects[key]
	if !ok {
		return nil, core.NotFoundError("object %q not found", key)
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}

func (f *fakeObjectStore) PutObject(_ context.Context, key string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	if f.putErr != nil {
		return f.putErr
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	f.objects[key] = cp
	return nil
}

func (f *fakeObjectStore) DeleteObject(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	if f.delErr != nil {
		return f.delErr
	}
	delete(f.objects, key)
	return nil
}

// raw returns the stored bytes for a key (test helper).
func (f *fakeObjectStore) raw(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[key]
	return data, ok
}

// set stores raw bytes for a key, bypassing PutObject accounting (test helper).
func (f *fakeObjectStore) set(key string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = data
}
