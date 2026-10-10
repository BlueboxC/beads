package dolt

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// loadMetadataSchema reads the metadata validation config from YAML and
// returns a parsed schema. Returns mode "none" with empty fields if config
// is not initialized, mode is empty/unknown, or no fields are defined.
func loadMetadataSchema() storage.MetadataSchemaConfig {
	mode := config.MetadataValidationMode()
	if mode == "none" {
		return storage.MetadataSchemaConfig{Mode: "none"}
	}

	rawFields := config.MetadataSchemaFields()
	if rawFields == nil {
		return storage.MetadataSchemaConfig{Mode: "none"}
	}

	fields := make(map[string]storage.MetadataFieldSchema)
	for name, raw := range rawFields {
		fieldMap, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		schema := parseFieldSchema(fieldMap)
		fields[name] = schema
	}

	if len(fields) == 0 {
		return storage.MetadataSchemaConfig{Mode: "none"}
	}

	return storage.MetadataSchemaConfig{
		Mode:   mode,
		Fields: fields,
	}
}

// parseFieldSchema shares conversion with the other storage adapters.
func parseFieldSchema(m map[string]interface{}) storage.MetadataFieldSchema {
	return issueops.ParseFieldSchema(m)
}

// validateMetadataIfConfigured checks metadata against the schema from config.
// In "warn" mode, prints warnings to stderr and returns nil.
// In "error" mode, returns the first validation error.
// In "none" mode (or if config is not initialized), does nothing.
func validateMetadataIfConfigured(metadata json.RawMessage) error {
	schema := loadMetadataSchema()
	if schema.Mode == "none" {
		return nil
	}

	errs := storage.ValidateMetadataSchema(metadata, schema)
	if len(errs) == 0 {
		return nil
	}

	if schema.Mode == "warn" {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "warning: %s\n", e.Error())
		}
		return nil
	}

	// mode == "error"
	return fmt.Errorf("metadata schema violation: %s", errs[0].Error())
}
