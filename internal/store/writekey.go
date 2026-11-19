package store

import (
	"encoding/json"
)

// RecordWriteKey saves the CLI-generated replay key before a mutation can
// be dispatched. The invocation ID identifies the file even if the CLI exits
// before it can report the outcome.
func RecordWriteKey(root, key string) (string, error) {
	unlock, err := Lock(root)
	if err != nil {
		return "", err
	}
	defer unlock()
	id, err := newRandomID()
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(struct {
		SchemaVersion  int    `json:"schema_version"`
		IdempotencyKey string `json:"idempotency_key"`
	}{1, key})
	if err != nil {
		return "", err
	}
	return id, writeFileAtomic(root, "write-"+id+".json", append(data, '\n'))
}
