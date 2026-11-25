package extmeta

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/xJogger/fake-komga-115/internal/bangumi"
	"github.com/xJogger/fake-komga-115/internal/database"
)

func TestGalleryCoreTitleAndEditionMatching(t *testing.T) {
	ctx := t.Context()
	_, m, root := newTestManager(t, ManagerOptions{})
	upstream := filepath.Join(root, "gallery.sqlite")
	const rawTitle = "(Expo) [Studio (Artist One)] Silver River [Chinese] [Digital]"
	createManagerGallery(t, upstream, rawTitle)
	db, err := sql.Open("sqlite", upstream)
	if err != nil {
		t.Fatal(err)
	}
	execManagerFixture(t, db, `
UPDATE gallery SET title_jpn='(Expo) [Studio (Artist One)] 銀の川 [Digital]' WHERE gid=101;
INSERT INTO gallery(gid,title,title_jpn,category,expunged,removed,replaced,rating,root_gid) VALUES
  (201, '(Expo) [Studio (Artist One)] Moonlight Harbor [Chinese]', '', 'Non-H', 1,0,0,5.0,200),
  (202, '[Studio (Artist One)] 月光港 [Digital]', '', 'Non-H', 0,0,0,4.0,200),
  (203, '[Studio (Artist One)] 月光港 [Digital]', '', 'Non-H', 0,0,0,4.0,200),
  (204, '[Studio (Artist One)] 月光港 [Digital]', '', 'Non-H', 0,0,0,3.0,200),
  (205, '[Studio (Artist One)] 月光港 [Digital]', '', 'Non-H', 0,0,1,5.0,200),
  (206, '[Studio (Artist One)] 月光港 [Digital]', '', 'Non-H', 0,1,0,5.0,200),
  (301, '[Shared Circle (Artist One)] Golden Train [English]', '', 'Non-H', 0,0,0,5.0,0),
  (302, '[Shared Circle (Artist Two)] Golden Train [Digital]', '', 'Non-H', 0,0,0,4.0,0),
  (401, '[Artist One] A Very Long Name for the Quiet Library [Digital]', '', 'Non-H', 0,0,0,4.0,0),
  (402, '[Artist One] A Very Long Name for the Quiet Library: West [Digital]', '', 'Non-H', 0,0,0,4.0,0),
  (501, '[Artist One] Starry Garden [Digital]', '', 'Non-H', 0,0,0,2.0,0),
  (502, 'Starry Garden [Digital]', '', 'Non-H', 0,0,0,5.0,0),
  (601, '[Artist One] Bright Field [Chinese]', '[Artist One] 明るい野原 [Digital]', 'Non-H', 1,0,0,5.0,0),
  (602, '[Artist One] 明るい野原 [Digital]', '', 'Non-H', 0,0,0,4.0,0);
`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	src := addManagerSource(t, m, database.MetadataSource{
		Kind: KindOfflineSQLite, Name: "Fixture gallery", LocalPath: upstream, SchemaType: SchemaGalleryV1,
	})
	syncManagerFixture(t, m, src, false, "success")
	m.mu.RLock()
	sidecar := m.sidecars[src.ID]
	m.mu.RUnlock()

	for _, query := range []string{
		"Silver River", "銀の川", rawTitle, "[Artist One] Silver River [Digital]",
	} {
		hits, err := sidecar.SearchByTitle(ctx, query, 1)
		if err != nil || len(hits) != 1 || hits[0].EntryID != "101" || hits[0].Score < 0.96 {
			t.Errorf("SearchByTitle(%q) = %+v, err = %v", query, hits, err)
		}
	}
	hits, err := sidecar.SearchWithHints(ctx, []string{"銀の川"}, []string{"Artist One"}, 1)
	if err != nil || len(hits) != 1 || hits[0].Score != 0.99 {
		t.Fatalf("nested artist credit was not scored: %+v, %v", hits, err)
	}

	for _, tc := range []struct {
		name, folder, want string
		aliases            []string
	}{
		{name: "raw folder without release tags", folder: "[Artist One] Silver River", want: "101"},
		{name: "translated and Japanese editions connected by root", folder: "Moonlight Harbor", aliases: []string{"月光港"}, want: "203"},
		{name: "shared Japanese core selects active edition", folder: "明るい野原", want: "602"},
		{name: "author wins before rating", folder: "[Artist One] Starry Garden", want: "501"},
		{name: "explicit author selects matching homonym", folder: "[Artist One] Golden Train", want: "301"},
		{name: "single hit rejects conflicting explicit artist", folder: "[Artist Three] Silver River"},
		{name: "same title with conflicting artists", folder: "Golden Train"},
		{name: "close scores with distinct titles", folder: "A Very Long Name for the Quiet Library"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed := bangumi.ParseSeriesName(tc.folder)
			titles := append(parsed.Candidates, tc.folder)
			titles = append(titles, tc.aliases...)
			entry, _, err := m.matchSidecar(ctx, src.ID, titles, parsed.Authors, "", nil)
			var id string
			if entry != nil {
				id = entry.EntryID
			}
			if err != nil || id != tc.want {
				t.Fatalf("match = %q, want %q, err = %v", id, tc.want, err)
			}
		})
	}
}
