package services

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"windshift/internal/database"
	"windshift/internal/models"
)

// customFieldIDs returns the definitions' ids in the order the service emitted
// them, so assertions can compare the exact published set.
func customFieldIDs(fields []models.CustomFieldDefinition) []int {
	ids := make([]int, 0, len(fields))
	for _, field := range fields {
		ids = append(ids, field.ID)
	}
	return ids
}

func TestPortalRowVisibleFailsClosedForMalformedRestrictions(t *testing.T) {
	malformed := sql.NullString{String: `{"unexpected":true}`, Valid: true}
	if portalRowVisible(malformed, sql.NullString{}, nil, nil) {
		t.Fatal("malformed group restrictions unexpectedly treated as unrestricted")
	}
	if portalRowVisible(sql.NullString{}, malformed, nil, nil) {
		t.Fatal("malformed organisation restrictions unexpectedly treated as unrestricted")
	}

	groups := sql.NullString{String: `[4,7]`, Valid: true}
	if !portalRowVisible(groups, sql.NullString{}, []int{7}, nil) {
		t.Fatal("matching group restriction was not visible")
	}
	if portalRowVisible(groups, sql.NullString{}, []int{9}, nil) {
		t.Fatal("non-matching group restriction was visible")
	}
}

func TestGetCustomFieldsForChannelOnlyReturnsFieldsBoundToCreateScreen(t *testing.T) {
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "portal-custom-fields.db"))
	if err != nil {
		t.Fatalf("NewSQLiteDB: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	insertID := func(label, query string, args ...interface{}) int {
		t.Helper()
		var id int
		if err := db.QueryRow(query, args...).Scan(&id); err != nil {
			t.Fatalf("insert %s: %v", label, err)
		}
		return id
	}

	workspaceID := insertID("workspace", `
		INSERT INTO workspaces (name, key)
		VALUES ('Portal custom-field workspace', 'PCFW') RETURNING id
	`)
	channelID := insertID("channel", `
		INSERT INTO channels (name, type, direction, status, config)
		VALUES ('Portal custom-field channel', 'portal', 'inbound', 'enabled', ?) RETURNING id
	`, fmt.Sprintf(`{"portal_workspace_ids":[%d]}`, workspaceID))
	itemTypeID := insertID("item type", `
		INSERT INTO item_types (name)
		VALUES ('Portal custom-field item type') RETURNING id
	`)
	configSetID := insertID("configuration set", `
		INSERT INTO configuration_sets (name)
		VALUES ('Portal custom-field configuration') RETURNING id
	`)
	screenID := insertID("screen", `
		INSERT INTO screens (name)
		VALUES ('Portal custom-field create screen') RETURNING id
	`)
	allowedFieldID := insertID("allowed custom field", `
		INSERT INTO custom_field_definitions (name, field_type, description)
		VALUES ('Portal-visible field', 'text', 'allowed definition') RETURNING id
	`)
	hiddenFieldID := insertID("unbound custom field", `
		INSERT INTO custom_field_definitions (name, field_type, description)
		VALUES ('Portal-hidden field', 'text', 'must not leak') RETURNING id
	`)
	// Asset references are portal-fillable since asset custom fields gained
	// portal support (they resolve through the portal's own asset endpoints);
	// every other reference type still needs an internal picker.
	assetFieldID := insertID("asset custom field", `
		INSERT INTO custom_field_definitions (name, field_type, description)
		VALUES ('Unsupported asset', 'asset', 'requires an internal picker') RETURNING id
	`)
	unsupportedFieldIDs := make([]int, 0, 8)
	for _, fieldType := range []string{
		"user", "multi_user", "milestone", "iteration",
		"portalcustomer", "customerorganisation", "linking", "combobox",
	} {
		unsupportedFieldIDs = append(unsupportedFieldIDs, insertID("unsupported public custom field", `
			INSERT INTO custom_field_definitions (name, field_type, description)
			VALUES (?, ?, 'requires an internal picker') RETURNING id
		`, "Unsupported "+fieldType, fieldType))
	}

	for label, statement := range map[string]string{
		"workspace configuration": `INSERT INTO workspace_configuration_sets (workspace_id, configuration_set_id) VALUES (?, ?)`,
		"item-type screen":        `INSERT INTO configuration_set_item_types (configuration_set_id, item_type_id, create_screen_id) VALUES (?, ?, ?)`,
	} {
		var args []interface{}
		switch label {
		case "workspace configuration":
			args = []interface{}{workspaceID, configSetID}
		case "item-type screen":
			args = []interface{}{configSetID, itemTypeID, screenID}
		}
		if _, err := db.ExecWrite(statement, args...); err != nil {
			t.Fatalf("insert %s: %v", label, err)
		}
	}
	for _, fieldID := range append([]int{allowedFieldID, assetFieldID}, unsupportedFieldIDs...) {
		if _, err := db.ExecWrite(`
			INSERT INTO screen_fields (screen_id, field_type, field_identifier)
			VALUES (?, 'custom', ?)
		`, screenID, strconv.Itoa(fieldID)); err != nil {
			t.Fatalf("insert screen field %d: %v", fieldID, err)
		}
	}

	requestTypeID := insertID("request type", `
		INSERT INTO request_types (channel_id, name, item_type_id, workspace_id, is_active)
		VALUES (?, 'Portal custom-field request', ?, ?, true) RETURNING id
	`, channelID, itemTypeID, workspaceID)
	for _, fieldID := range append([]int{allowedFieldID, assetFieldID, hiddenFieldID}, unsupportedFieldIDs...) {
		if _, err := db.ExecWrite(`
			INSERT INTO request_type_fields
				(request_type_id, field_identifier, field_type, is_required, display_order)
			VALUES (?, ?, 'custom', ?, ?)
		`, requestTypeID, strconv.Itoa(fieldID),
			fieldID != allowedFieldID && fieldID != assetFieldID && fieldID != hiddenFieldID, fieldID); err != nil {
			t.Fatalf("insert request type field %d: %v", fieldID, err)
		}
	}

	fields, err := NewPortalService(db).GetCustomFieldsForChannel(context.Background(), channelID, nil, nil, true)
	if err != nil {
		t.Fatalf("GetCustomFieldsForChannel: %v", err)
	}
	if ids := customFieldIDs(fields); !slices.Equal(ids, []int{allowedFieldID, assetFieldID}) {
		t.Fatalf("custom fields = %v, want the bound text field %d and asset field %d (not hidden %d)",
			ids, allowedFieldID, assetFieldID, hiddenFieldID)
	}

	requestFields, err := NewPortalService(db).GetRequestTypeFields(context.Background(), requestTypeID)
	if err != nil {
		t.Fatalf("GetRequestTypeFields: %v", err)
	}
	if len(requestFields) != 2 ||
		requestFields[0].FieldIdentifier != strconv.Itoa(allowedFieldID) ||
		requestFields[1].FieldIdentifier != strconv.Itoa(assetFieldID) {
		t.Fatalf("request fields = %+v, want the bound text field %d and asset field %d", requestFields, allowedFieldID, assetFieldID)
	}
	requestDefinitions, err := NewPortalService(db).GetCustomFieldsForRequestType(context.Background(), requestTypeID)
	if err != nil {
		t.Fatalf("GetCustomFieldsForRequestType: %v", err)
	}
	if ids := customFieldIDs(requestDefinitions); !slices.Equal(ids, []int{allowedFieldID, assetFieldID}) {
		t.Fatalf("request definitions = %v, want the bound text field %d and asset field %d", ids, allowedFieldID, assetFieldID)
	}

	submittedFields := map[string]interface{}{
		strconv.Itoa(allowedFieldID): "allowed value",
		strconv.Itoa(assetFieldID):   1,
		strconv.Itoa(hiddenFieldID):  "hidden value",
	}
	for _, fieldID := range unsupportedFieldIDs {
		submittedFields[strconv.Itoa(fieldID)] = 1
	}
	validationResult, err := ValidateAndSeparateRequestFields(context.Background(), db, &requestTypeID, "Title", "", submittedFields)
	if err != nil {
		t.Fatalf("ValidateAndSeparateRequestFields: %v", err)
	}
	if len(validationResult.CustomFieldValues) != 2 ||
		validationResult.CustomFieldValues[strconv.Itoa(allowedFieldID)] != "allowed value" ||
		validationResult.CustomFieldValues[strconv.Itoa(assetFieldID)] != 1 {
		t.Fatalf("validated custom fields = %+v, want only the bound fields %d and %d",
			validationResult.CustomFieldValues, allowedFieldID, assetFieldID)
	}
}

// GetCustomFieldsForRequestType returns only the definitions consumed by one
// public request form.
func (s *PortalService) GetCustomFieldsForRequestType(ctx context.Context, requestTypeID int) ([]models.CustomFieldDefinition, error) {
	_, definitions, err := s.GetRequestTypeForm(ctx, requestTypeID)
	return definitions, err
}
