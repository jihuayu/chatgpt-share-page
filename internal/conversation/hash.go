package conversation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// canonicalContent is the subset of a snapshot that determines content
// identity. Import timestamps and storage IDs are deliberately excluded so
// re-importing identical content produces the same hash.
type canonicalContent struct {
	Version  int               `json:"version"`
	Title    string            `json:"title"`
	Source   SnapshotSource    `json:"source"`
	Messages []ConversationMsg `json:"messages"`
	Metadata SnapshotMetadata  `json:"metadata"`
}

// ContentHash returns the SHA-256 digest of the canonical snapshot JSON.
// encoding/json emits struct fields in declaration order, so the encoding is
// deterministic.
func ContentHash(snapshot *ConversationSnapshot) string {
	canonical := canonicalContent{
		Version:  snapshot.Version,
		Title:    snapshot.Title,
		Source:   snapshot.Source,
		Messages: snapshot.Messages,
		Metadata: snapshot.Metadata,
	}
	encoded, _ := json.Marshal(canonical)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// RevisionID derives a stable, content-addressed revision identifier.
func RevisionID(contentHash string) string {
	if len(contentHash) > 16 {
		return contentHash[:16]
	}
	return contentHash
}
