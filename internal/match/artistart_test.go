package match

import (
	"context"
	"testing"
)

// With no provider keys, both backfill clients are nil, so the picker's
// candidate aggregator contributes nothing and never touches the network.
func TestArtistImageCandidatesNoKeys(t *testing.T) {
	m := New(nil, t.TempDir(), "", "")
	if got := m.ArtistImageCandidates(context.Background(), "11111111-1111-1111-1111-111111111111"); got != nil {
		t.Fatalf("no-key candidates = %v, want nil", got)
	}
	if got := m.ArtistImageCandidates(context.Background(), ""); got != nil {
		t.Fatalf("empty-mbid candidates = %v, want nil", got)
	}
}
