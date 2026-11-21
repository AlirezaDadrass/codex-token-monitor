package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

type Store struct{ Dir string }

// Put atomically replaces a device snapshot. Retrying a sync does not add usage,
// and an older delayed request cannot replace a newer snapshot.
func (s Store) Put(ctx context.Context, snapshot Snapshot) (bool, error) {
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return false, err
	}
	key := hash(snapshot.Account + "/" + snapshot.MachineID)
	lock := flock.New(filepath.Join(s.Dir, key+".lock"))
	ok, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, errors.New("snapshot lock unavailable")
	}
	defer lock.Close()
	path := filepath.Join(s.Dir, key+".json")
	b, err := os.ReadFile(path)
	if err == nil {
		var old Snapshot
		if err := json.Unmarshal(b, &old); err != nil {
			return false, err
		}
		if old.CollectedAt.After(snapshot.CollectedAt) {
			return false, nil
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	return true, atomicJSON(path, snapshot)
}

func (s Store) List(account string) ([]Snapshot, error) {
	paths, err := filepath.Glob(filepath.Join(s.Dir, "*.json"))
	if err != nil {
		return nil, err
	}
	result := []Snapshot{}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var snapshot Snapshot
		if err := json.Unmarshal(b, &snapshot); err != nil {
			return nil, fmt.Errorf("invalid stored snapshot: %w", err)
		}
		if snapshot.Account == account {
			result = append(result, snapshot)
		}
	}
	return result, nil
}
