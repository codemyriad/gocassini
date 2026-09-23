package operator

import "testing"

func TestArchiveInventorySeparatesOpusFromLegacyEntriesAndRequiresCatalogPresence(t *testing.T) {
	facts := ncArchiveFacts{
		Probed: true, CatalogProbed: true, Catalog: true,
		Entries: []davEntry{{Name: "LIVE.opus"}, {Name: "LEGACY"}, {Name: "OTHER.txt"}},
	}
	inventory := inventoryFromArchiveFacts(facts, "2026-09-22T12:00:00Z")
	if len(inventory.OpusNames) != 1 || inventory.OpusNames[0] != "LIVE.opus" || inventory.Unsupported != 2 || !inventory.CatalogPresent || inventory.CheckedAt == "" {
		t.Fatalf("inventory = %+v", inventory)
	}
	facts.CatalogProbed = false
	if inventory := inventoryFromArchiveFacts(facts, "2026-09-22T12:00:00Z"); inventory.CatalogPresent {
		t.Fatalf("unobserved catalog allowed a complete verdict: %+v", inventory)
	}
}

func TestArchiveInventoryUsesTheSelectedStorageRoot(t *testing.T) {
	probe := ncStorageProbe{
		ACLArchive: ncArchiveFacts{Probed: true, CatalogProbed: true, Catalog: true,
			Entries: []davEntry{{Name: "ACL.opus"}}},
		DefaultArchive: ncArchiveFacts{Probed: true, CatalogProbed: true, Catalog: true,
			Entries: []davEntry{{Name: "DEFAULT.opus"}}},
	}
	for _, tc := range []struct {
		accessControlled bool
		want             string
	}{{true, "ACL.opus"}, {false, "DEFAULT.opus"}} {
		inventory, known := inventoryFromStorageProbe(probe, tc.accessControlled, "2026-09-22T12:00:00Z")
		if !known || len(inventory.OpusNames) != 1 || inventory.OpusNames[0] != tc.want {
			t.Errorf("mode accessControlled=%t inventory=%+v known=%t, want %s", tc.accessControlled, inventory, known, tc.want)
		}
	}
	probe.ACLArchive.Probed = false
	if inventory, known := inventoryFromStorageProbe(probe, true, "2026-09-22T12:00:00Z"); known {
		t.Fatalf("unprobed selected root read as empty: %+v", inventory)
	}
}
