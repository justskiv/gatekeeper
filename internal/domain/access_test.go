package domain

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// accessSourceFile is the file whose const blocks are the subject here. It is
// read from disk on purpose; the test below explains why.
const accessSourceFile = "access.go"

// TestAllActionTypesCoversEveryDeclaredConstant checks the registry against the
// declarations it claims to enumerate.
//
// The expectation is parsed out of the source file rather than built from
// AllActionTypes, and that is the entire point. A test that iterates the helper
// to validate the helper proves only that a slice equals itself: it stays green
// through exactly the failure that matters — a new `ActionType` constant that
// nobody added to the list — while the metrics endpoint silently stops
// emitting the pair. Go cannot enumerate a string enum, so the compiler is of
// no help here and something has to read the declarations independently.
func TestAllActionTypesCoversEveryDeclaredConstant(t *testing.T) {
	declared := declaredConstants(t, "ActionType")
	require.NotEmpty(t, declared, "the const block must be found and parsed")

	registered := make([]string, 0, len(AllActionTypes()))
	for _, actionType := range AllActionTypes() {
		registered = append(registered, string(actionType))
	}

	assert.ElementsMatch(t, declared, registered,
		"AllActionTypes must list every declared ActionType and nothing else")
}

// TestAllActionStatusesCoversEveryDeclaredConstant is the same guard for the
// status enum; see the test above for why it parses instead of iterating.
func TestAllActionStatusesCoversEveryDeclaredConstant(t *testing.T) {
	declared := declaredConstants(t, "ActionStatus")
	require.NotEmpty(t, declared, "the const block must be found and parsed")

	registered := make([]string, 0, len(AllActionStatuses()))
	for _, status := range AllActionStatuses() {
		registered = append(registered, string(status))
	}

	assert.ElementsMatch(t, declared, registered,
		"AllActionStatuses must list every declared ActionStatus")
}

// declaredConstants returns the string values of every constant declared with
// the named type in accessSourceFile.
func declaredConstants(t *testing.T, typeName string) []string {
	t.Helper()

	file, err := parser.ParseFile(
		token.NewFileSet(), accessSourceFile, nil, 0)
	require.NoError(t, err, "parse %s", accessSourceFile)

	var values []string

	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}

		for _, spec := range genDecl.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}

			ident, ok := valueSpec.Type.(*ast.Ident)
			if !ok || ident.Name != typeName {
				continue
			}

			values = append(values, constValues(t, valueSpec)...)
		}
	}

	return values
}

// constValues unquotes the string literals a single const spec assigns.
func constValues(t *testing.T, spec *ast.ValueSpec) []string {
	t.Helper()

	values := make([]string, 0, len(spec.Values))

	for _, value := range spec.Values {
		literal, ok := value.(*ast.BasicLit)
		require.True(t, ok, "constant %v is not a literal", spec.Names)
		require.Equal(t, token.STRING, literal.Kind,
			"constant %v is not a string", spec.Names)

		unquoted, err := strconv.Unquote(literal.Value)
		require.NoError(t, err, "unquote %s", literal.Value)

		values = append(values, unquoted)
	}

	return values
}
