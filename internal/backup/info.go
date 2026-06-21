package backup

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// BackupInfo is one parsed backup from `pgbackrest info --output=json`.
//
// pgBackRest reports several distinct sizes per backup; we surface the three
// the panel cares about:
//
//   - DatabaseSize: the original (uncompressed) size of the database cluster.
//   - Size: the logical size of this backup's contribution (the "delta" — what
//     this backup actually copied; equals DatabaseSize for a full backup).
//   - RepoSize: the compressed bytes this backup occupies in the repository.
type BackupInfo struct {
	Label        string
	Type         Type
	StartTime    time.Time
	StopTime     time.Time
	Size         int64 // backup size (bytes) — this backup's delta
	DatabaseSize int64 // original database size (bytes)
	RepoSize     int64 // compressed size in repo (bytes)
	WALStart     string
	WALStop      string
}

// Duration is how long the backup took (StopTime - StartTime). It is zero when
// either timestamp is unset.
func (b BackupInfo) Duration() time.Duration {
	if b.StartTime.IsZero() || b.StopTime.IsZero() {
		return 0
	}
	d := b.StopTime.Sub(b.StartTime)
	if d < 0 {
		return 0
	}
	return d
}

// infoDocument mirrors the top-level array returned by `pgbackrest info
// --output=json`: one element per stanza.
type infoDocument []infoStanza

type infoStanza struct {
	Name   string       `json:"name"`
	Backup []infoBackup `json:"backup"`
	Status *infoStatus  `json:"status,omitempty"`
}

type infoStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type infoBackup struct {
	Label     string         `json:"label"`
	Type      string         `json:"type"`
	Timestamp infoTimestamp  `json:"timestamp"`
	Info      infoBackupInfo `json:"info"`
	Archive   infoArchive    `json:"archive"`
}

type infoTimestamp struct {
	Start int64 `json:"start"`
	Stop  int64 `json:"stop"`
}

// infoBackupInfo carries the size figures. pgBackRest reports:
//
//	"info": {
//	  "size":  <original database size>,
//	  "delta": <bytes this backup copied>,
//	  "repository": { "size": <repo full size>, "delta": <repo bytes this backup added> }
//	}
type infoBackupInfo struct {
	Size       int64              `json:"size"`
	Delta      int64              `json:"delta"`
	Repository infoBackupRepoSize `json:"repository"`
}

type infoBackupRepoSize struct {
	Size  int64 `json:"size"`
	Delta int64 `json:"delta"`
}

type infoArchive struct {
	Start string `json:"start"`
	Stop  string `json:"stop"`
}

// ParseInfoJSON parses pgbackrest info JSON for the given stanza into
// BackupInfo, newest first (by start time, with label as a stable tiebreaker).
//
// It returns *core.Error (CodeValidation) on malformed JSON, and
// *core.Error (CodeNotFound) when the requested stanza is absent from the
// document. An empty stanza matches the first (and typically only) stanza.
func ParseInfoJSON(data []byte, stanza string) ([]BackupInfo, error) {
	var doc infoDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, core.ValidationError("malformed pgbackrest info JSON").Wrap(err)
	}

	var st *infoStanza
	for i := range doc {
		if stanza == "" || doc[i].Name == stanza {
			st = &doc[i]
			break
		}
	}
	if st == nil {
		return nil, core.NotFoundError("stanza %q not found in pgbackrest info", stanza)
	}

	out := make([]BackupInfo, 0, len(st.Backup))
	for _, b := range st.Backup {
		t, err := ParseType(b.Type)
		if err != nil {
			// Tolerate an unknown type label rather than failing the whole
			// listing; record it verbatim so the UI can still show the backup.
			t = Type(b.Type)
		}
		out = append(out, BackupInfo{
			Label:        b.Label,
			Type:         t,
			StartTime:    unixOrZero(b.Timestamp.Start),
			StopTime:     unixOrZero(b.Timestamp.Stop),
			Size:         b.Info.Delta,
			DatabaseSize: b.Info.Size,
			RepoSize:     b.Info.Repository.Delta,
			WALStart:     b.Archive.Start,
			WALStop:      b.Archive.Stop,
		})
	}

	// Newest first. pgBackRest emits oldest-first; sort defensively so callers
	// never depend on emission order.
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].StartTime.Equal(out[j].StartTime) {
			return out[i].StartTime.After(out[j].StartTime)
		}
		return out[i].Label > out[j].Label
	})

	return out, nil
}

// unixOrZero converts a unix-second timestamp to a UTC time.Time, returning the
// zero time for a non-positive value (pgBackRest omits/zeroes unknown stamps).
func unixOrZero(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}
