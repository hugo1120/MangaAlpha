package httpserver

import (
	"encoding/json"
	"testing"

	"github.com/xJogger/fake-komga-115/internal/oneonefive"
)

func TestFilter115FoldersSerializesIDAndFileID(t *testing.T) {
	in := []oneonefive.File{
		{ID: "2819948123", ParentID: "0", Name: "漫画主库", PickCode: "pc1", IsDir: true},
		{ID: "2819948124", ParentID: "0", Name: "Vol01.cbz", PickCode: "pc2", IsDir: false},
		{ID: "   ", ParentID: "0", Name: "空ID异常目录", IsDir: true},
	}
	got := filter115Folders(in)
	if len(got) != 1 {
		t.Fatalf("expected 1 valid folder, got %d: %+v", len(got), got)
	}
	raw, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["id"] != "2819948123" || obj["fileId"] != "2819948123" || obj["name"] != "漫画主库" {
		t.Fatalf("unexpected serialized folder payload: %s", string(raw))
	}
}
