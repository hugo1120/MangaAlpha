package oneonefive

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/OpenListTeam/115-sdk-go"
)

func makeFilesResp(count int64, items ...sdk.GetFilesResp_File) *sdk.GetFilesResp {
	return &sdk.GetFilesResp{
		Resp:  sdk.Resp[[]sdk.GetFilesResp_File]{Data: items},
		Count: count,
	}
}

func TestCollectDirectoryPagesShortPageAdvancesByActualCount(t *testing.T) {
	var offsets []int64
	fetch := func(_ context.Context, cid string, _ int64, offset int64) (*sdk.GetFilesResp, error) {
		offsets = append(offsets, offset)
		switch offset {
		case 0:
			return makeFilesResp(3,
				sdk.GetFilesResp_File{Fid: "101", Pid: cid, Fn: "DirA", Fc: "0"},
				sdk.GetFilesResp_File{Fid: "102", Pid: cid, Fn: "DirB", Fc: "0"},
			), nil
		case 2:
			return makeFilesResp(3,
				sdk.GetFilesResp_File{Fid: "103", Pid: cid, Fn: "Vol01.cbz", Fc: "1", FS: 1024},
			), nil
		default:
			t.Fatalf("unexpected offset %d", offset)
			return nil, nil
		}
	}

	files, err := collectDirectoryPages(context.Background(), "0", fetch)
	if err != nil {
		t.Fatalf("collectDirectoryPages failed: %v", err)
	}
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != 2 {
		t.Fatalf("offsets = %v, want [0 2]", offsets)
	}
	if len(files) != 3 || files[0].ID != "101" || !files[0].IsDir || files[2].ID != "103" || files[2].IsDir {
		t.Fatalf("unexpected files: %+v", files)
	}
}

func TestCollectDirectoryPagesEarlyEmptyErrorsWhenCountRemaining(t *testing.T) {
	fetch := func(_ context.Context, cid string, _ int64, offset int64) (*sdk.GetFilesResp, error) {
		if offset == 0 {
			return makeFilesResp(5,
				sdk.GetFilesResp_File{Fid: "201", Pid: cid, Fn: "OnlyOne", Fc: "0"},
			), nil
		}
		return makeFilesResp(5), nil
	}

	_, err := collectDirectoryPages(context.Background(), "999", fetch)
	if err == nil || !strings.Contains(err.Error(), "stopped early") {
		t.Fatalf("expected early-stop error, got %v", err)
	}
}

func TestCollectDirectoryPagesEarlyEmptyZeroCountRemembersEarlierTotal(t *testing.T) {
	fetch := func(_ context.Context, cid string, _ int64, offset int64) (*sdk.GetFilesResp, error) {
		if offset == 0 {
			return makeFilesResp(5,
				sdk.GetFilesResp_File{Fid: "301", Pid: cid, Fn: "First", Fc: "0"},
				sdk.GetFilesResp_File{Fid: "302", Pid: cid, Fn: "Second", Fc: "0"},
			), nil
		}
		return makeFilesResp(0), nil
	}

	_, err := collectDirectoryPages(context.Background(), "888", fetch)
	if err == nil || !strings.Contains(err.Error(), "stopped early (2/5 entries)") {
		t.Fatalf("expected early-stop error preserving earlier total, got %v", err)
	}
}

func TestCollectDirectoryPagesEmptyDirectoryOK(t *testing.T) {
	fetch := func(_ context.Context, _ string, _ int64, _ int64) (*sdk.GetFilesResp, error) {
		return makeFilesResp(0), nil
	}
	files, err := collectDirectoryPages(context.Background(), "0", fetch)
	if err != nil {
		t.Fatalf("empty directory should not error, got %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected 0 files, got %d", len(files))
	}
}
