package progress

import (
    "encoding/json"
    "os"
)

// Checkpoint represents embedding progress persisted to disk by the embedder.
// next_offset is the index in the Scryfall bulk list where the next batch should start.
type Checkpoint struct {
    NextOffset   int    `json:"next_offset"`
    Total        int    `json:"total"`
    LastBatchOut string `json:"last_batch_out"`
    Model        string `json:"model,omitempty"`
}

// ReadCheckpoint loads the checkpoint JSON file if present.
func ReadCheckpoint(path string) (Checkpoint, error) {
    var cp Checkpoint
    f, err := os.Open(path)
    if err != nil {
        return cp, err
    }
    defer f.Close()
    dec := json.NewDecoder(f)
    err = dec.Decode(&cp)
    return cp, err
}

