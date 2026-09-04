package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docs/PROMPT_ASSET_INVENTORY.md has a row per registered asset, and
// nothing made that true.
//
// The file says so about itself. Its count sentence went stale in
// v0.76.0 and again in v0.94.0, and both times the remedy was a better
// sentence. A better sentence does not help: nobody reads a paragraph
// when adding an r.Add call. So the table is checked instead.
//
// The check is by asset ID, not by counting rows. A count tells you the
// number is wrong and not which asset is missing, which is the same
// failure the prose had.
func TestTheInventoryListsEveryRegisteredAsset(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "PROMPT_ASSET_INVENTORY.md"))
	if err != nil {
		t.Fatalf("read PROMPT_ASSET_INVENTORY.md: %v", err)
	}
	text := string(doc)

	assets := promptRegistry().All()
	if len(assets) < 10 {
		t.Fatalf("only %d assets registered, so this test is checking almost nothing", len(assets))
	}
	var missing []string
	for _, a := range assets {
		if !strings.Contains(text, "`"+a.ID+"`") {
			missing = append(missing, a.ID)
		}
	}
	if len(missing) > 0 {
		t.Errorf("promptRegistry() registers %v and docs/PROMPT_ASSET_INVENTORY.md has no row for them. "+
			"Add one each to the registered-assets table, and update the count sentence under it.", missing)
	}
}
