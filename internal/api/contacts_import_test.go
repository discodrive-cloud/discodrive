package api

import (
	"strings"
	"testing"
)

func TestSplitVCards(t *testing.T) {
	raw := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Иван\r\nUID:a1\r\nEND:VCARD\r\n" +
		"\r\nмусор между\r\n" +
		"BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Пётр\r\nUID:b2\r\nEND:VCARD\r\n"
	blocks := splitVCards(raw)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d: %#v", len(blocks), blocks)
	}
	if !strings.Contains(blocks[0], "FN:Иван") || !strings.Contains(blocks[1], "FN:Пётр") {
		t.Fatalf("unexpected blocks: %#v", blocks)
	}
	if !strings.HasPrefix(blocks[0], "BEGIN:VCARD") || !strings.HasSuffix(strings.TrimRight(blocks[0], "\r\n"), "END:VCARD") {
		t.Fatalf("block is not wrapped in BEGIN/END: %q", blocks[0])
	}
	if len(splitVCards("")) != 0 {
		t.Fatal("empty input → 0 blocks")
	}
}

func TestImportVCardsFlow(t *testing.T) {
	svc, abID, ctx := setupContacts(t) // helper from contacts_test.go: (*dav.Service, string, context.Context)
	raw := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Иван\r\nUID:imp-1\r\nEND:VCARD\r\n" +
		"BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Пётр\r\nUID:imp-2\r\nEND:VCARD\r\n"
	imp, skip := importVCards(ctx, svc, abID, raw)
	if imp != 2 || skip != 0 {
		t.Fatalf("expected imported=2 skipped=0, got %d/%d", imp, skip)
	}
	objs, _ := svc.ListAddressbookObjects(ctx, abID)
	if len(objs) != 2 {
		t.Fatalf("expected 2 in the addressbook, got %d", len(objs))
	}
	// re-importing the same UIDs → upsert, still 2
	importVCards(ctx, svc, abID, raw)
	objs, _ = svc.ListAddressbookObjects(ctx, abID)
	if len(objs) != 2 {
		t.Fatalf("expected 2 after re-import (upsert), got %d", len(objs))
	}
	// a card without UID → gets a generated UID and is saved
	noUID := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Без Уида\r\nEND:VCARD\r\n"
	imp, _ = importVCards(ctx, svc, abID, noUID)
	if imp != 1 {
		t.Fatalf("card without UID: expected imported=1, got %d", imp)
	}
	objs, _ = svc.ListAddressbookObjects(ctx, abID)
	if len(objs) != 3 {
		t.Fatalf("expected 3 after importing the UID-less card, got %d", len(objs))
	}
	// export contains both named cards and proper BEGIN/END wrapping
	out := exportVCards(objs)
	if !strings.Contains(out, "FN:Иван") || !strings.Contains(out, "FN:Без Уида") || strings.Count(out, "BEGIN:VCARD") != 3 {
		t.Fatalf("export is incomplete:\n%s", out)
	}
}

// A UTF-8 byte order mark (Windows/Outlook exports) used to hide the first card.
func TestSplitVCardsSkipsBOM(t *testing.T) {
	raw := "\uFEFFBEGIN:VCARD\r\nVERSION:3.0\r\nFN:A\r\nEND:VCARD\r\nBEGIN:VCARD\r\nVERSION:3.0\r\nFN:B\r\nEND:VCARD\r\n"
	if got := splitVCards(raw); len(got) != 2 {
		t.Fatalf("cards from a file with a BOM: %d, want 2", len(got))
	}
}
