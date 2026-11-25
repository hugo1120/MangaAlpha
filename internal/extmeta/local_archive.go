package extmeta

import (
	"archive/zip"
	"context"
	"io"
	"path"
	"strings"

	"github.com/nwaples/rardecode/v2"

	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/localfs"
)

// LocalArchiveMetaReader opens only the first local archive selected by the
// manager and reads one bounded, unencrypted metadata entry on demand.
func LocalArchiveMetaReader(store *database.Store) ArchiveMetaReader {
	return func(ctx context.Context, book database.Book) (*EmbeddedMeta, error) {
		library, err := store.LibraryBasic(ctx, book.LibraryID)
		if err != nil || library.ProviderType != database.ProviderLocal {
			return nil, err
		}
		file, err := localfs.OpenBook(library.RootPath, book.FileID)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return nil, err
		}
		size := info.Size()
		if size <= 0 {
			size = book.Size
		}
		files, err := zip.NewReader(file, size)
		if err == nil {
			return readZIPMetadata(ctx, files)
		}
		rar, err := rardecode.NewReader(file, rardecode.MaxDictionarySize(16<<20))
		if err != nil {
			return nil, err
		}
		for ctx.Err() == nil {
			header, err := rar.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if header.Solid {
				break
			}
			parse := embeddedParser(header.Name)
			if parse != nil && !header.IsDir && !header.Encrypted && !header.UnKnownSize &&
				header.UnPackedSize >= 0 && header.UnPackedSize <= maxEmbeddedMetaBytes {
				return parse(rar)
			}
		}
		return nil, ctx.Err()
	}
}

func readZIPMetadata(ctx context.Context, files *zip.Reader) (*EmbeddedMeta, error) {
	for _, entry := range files.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		parse := embeddedParser(entry.Name)
		if parse == nil || entry.FileInfo().IsDir() || entry.Flags&1 != 0 || entry.UncompressedSize64 > uint64(maxEmbeddedMetaBytes) {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			continue
		}
		meta, err := parse(reader)
		_ = reader.Close()
		if err == nil {
			return meta, nil
		}
	}
	return nil, nil
}

func embeddedParser(name string) func(io.Reader) (*EmbeddedMeta, error) {
	switch strings.ToLower(path.Base(strings.ReplaceAll(name, "\\", "/"))) {
	case "comicinfo.xml":
		return ParseComicInfoXML
	case "galleryinfo.txt":
		return ParseGalleryInfoTXT
	default:
		return nil
	}
}
