package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/steveyegge/beads/memoryops"
)

const (
	catalogKey         = Prefix + "catalog"
	catalogPartPrefix  = Prefix + "catalog-part/"
	maxCatalogRowBytes = 60 * 1024
	maxCatalogBytes    = 8 * 1024 * 1024
	maxCatalogParts    = maxCatalogBytes/(maxCatalogRowBytes-utf8.UTFMax) + 1
)

type catalogManifest struct {
	Version int      `json:"knowledge_version"`
	Parts   []string `json:"catalog_parts"`
	Bytes   int      `json:"catalog_bytes"`
}

func catalogDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// LoadCatalog reads legacy single values and complete content-addressed catalogs.
// Missing/corrupt parts withhold the entire selection, never a partial catalog.
func LoadCatalog(plane map[string]string) (Catalog, error) {
	value, ok := plane[catalogKey]
	if !ok {
		return Catalog{}, nil
	}
	if len(value) > maxCatalogBytes {
		return Catalog{}, errors.New("catalog exceeds 8 MiB")
	}
	var header catalogManifest
	if err := json.Unmarshal([]byte(value), &header); err != nil {
		return Catalog{}, errors.New("invalid catalog encoding")
	}
	if header.Version == 2 {
		if len(value) > maxCatalogRowBytes || len(header.Parts) == 0 || len(header.Parts) > maxCatalogParts || header.Bytes <= maxCatalogRowBytes || header.Bytes > maxCatalogBytes {
			return Catalog{}, errors.New("invalid catalog manifest bounds")
		}
		var joined strings.Builder
		joined.Grow(header.Bytes)
		for _, id := range header.Parts {
			part, exists := plane[catalogPartPrefix+id]
			if !exists || len(part) == 0 || len(part) > maxCatalogRowBytes || !utf8.ValidString(part) || catalogDigest(part) != id {
				return Catalog{}, errors.New("missing or corrupt catalog part")
			}
			if joined.Len()+len(part) > header.Bytes {
				return Catalog{}, errors.New("catalog parts exceed declared size")
			}
			joined.WriteString(part)
		}
		if joined.Len() != header.Bytes {
			return Catalog{}, errors.New("incomplete catalog size")
		}
		value = joined.String()
	}
	var legacy envelope
	if err := json.Unmarshal([]byte(value), &legacy); err != nil || legacy.Version != 1 || legacy.Catalog == nil || len(legacy.Catalog.Roots) > MaxSources || len(legacy.Catalog.Sources) > MaxSources {
		return Catalog{}, errors.New("unsupported or invalid catalog")
	}
	return *legacy.Catalog, nil
}

// SaveCatalog prepares bounded parts before publishing one manifest. Old parts
// remain for rollback and concurrent readers; no human entries are touched.
func SaveCatalog(ctx context.Context, memories memoryops.Memories, catalog Catalog) error {
	if len(catalog.Roots) > MaxSources || len(catalog.Sources) > MaxSources {
		return fmt.Errorf("catalog roots and sources are limited to %d each", MaxSources)
	}
	data, err := json.Marshal(envelope{Version: 1, Catalog: &catalog})
	if err != nil {
		return err
	}
	if len(data) > maxCatalogBytes {
		return errors.New("catalog exceeds 8 MiB; narrow selected roots")
	}
	value := string(data)
	var parts []string
	if len(data) > maxCatalogRowBytes {
		manifest := catalogManifest{Version: 2, Bytes: len(data)}
		for start := 0; start < len(data); {
			end := min(start+maxCatalogRowBytes, len(data))
			for end < len(data) && !utf8.RuneStart(data[end]) {
				end--
			}
			part := string(data[start:end])
			parts = append(parts, part)
			manifest.Parts = append(manifest.Parts, catalogDigest(part))
			start = end
		}
		encoded, err := json.Marshal(manifest)
		if err != nil {
			return err
		}
		value = string(encoded)
	}
	for _, part := range parts {
		if err := saveCatalogValue(ctx, memories, catalogPartPrefix+catalogDigest(part), part); err != nil {
			return err
		}
	}
	return saveCatalogValue(ctx, memories, catalogKey, value)
}

func saveCatalogValue(ctx context.Context, memories memoryops.Memories, key, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	previous, err := memories.Recall(ctx, memoryops.RecallRequest{Key: key})
	if err != nil {
		return err
	}
	if previous.Found && previous.Value == value {
		return nil
	}
	if key != catalogKey && previous.Found {
		return errors.New("conflicting catalog part; previous catalog retained")
	}
	_, err = memories.Remember(ctx, memoryops.RememberRequest{Key: key, Content: value})
	return err
}
