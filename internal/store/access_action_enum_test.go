package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
)

// TestActionEnumsMatchSchemaCheckConstraints holds the domain registry against
// the database, which is the other authority on what an action can be.
//
// The expectation is read out of the live schema, never out of
// domain.AllActionTypes: a test that iterates the helper it is validating
// proves nothing — it would stay green through precisely the drift that
// matters, a value the schema accepts and the registry has forgotten, which
// costs the metrics endpoint a whole series. Two lists exist here as a matter
// of fact (SQL cannot import Go), so the fix is not to pretend otherwise but to
// make any divergence between them fail here.
func TestActionEnumsMatchSchemaCheckConstraints(t *testing.T) {
	db := newTestDB(t)

	var schema string

	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT sql FROM sqlite_master
		WHERE type = 'table' AND name = 'access_actions'`,
	).Scan(&schema), "read the access_actions schema")

	registeredTypes := make([]string, 0, len(domain.AllActionTypes()))
	for _, actionType := range domain.AllActionTypes() {
		registeredTypes = append(registeredTypes, string(actionType))
	}

	assert.ElementsMatch(t,
		checkedValues(t, schema, "action_type"), registeredTypes,
		"domain.AllActionTypes and the action_type CHECK must agree")

	registeredStatuses := make([]string, 0, len(domain.AllActionStatuses()))
	for _, status := range domain.AllActionStatuses() {
		registeredStatuses = append(registeredStatuses, string(status))
	}

	assert.ElementsMatch(t,
		checkedValues(t, schema, "status"), registeredStatuses,
		"domain.AllActionStatuses and the status CHECK must agree")
}

// checkedValues extracts the literals of a `CHECK (<column> IN (...))`
// constraint from a CREATE TABLE statement.
func checkedValues(t *testing.T, schema, column string) []string {
	t.Helper()

	marker := "CHECK (" + column + " IN ("

	start := strings.Index(schema, marker)
	require.GreaterOrEqual(t, start, 0,
		"no CHECK constraint found for column %s", column)

	rest := schema[start+len(marker):]

	end := strings.Index(rest, ")")
	require.GreaterOrEqual(t, end, 0,
		"unterminated CHECK constraint for column %s", column)

	literals := strings.Split(rest[:end], ",")

	values := make([]string, 0, len(literals))
	for _, literal := range literals {
		values = append(values, strings.Trim(strings.TrimSpace(literal), "'"))
	}

	return values
}
