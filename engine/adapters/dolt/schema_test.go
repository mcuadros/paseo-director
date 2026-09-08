// SPDX-License-Identifier: Apache-2.0

package dolt

import "testing"

func TestNormalizedSQLPreservesQuotedWhitespace(t *testing.T) {
	externalSpacing := "  INSERT  INTO parent_guard (identity)\nSELECT  'guard.parent missing'  FROM `guard table`  "
	want := "INSERT INTO parent_guard (identity) SELECT 'guard.parent missing' FROM `guard table`"
	if actual := normalizedSQL(externalSpacing); actual != want {
		t.Fatalf("normalizedSQL() = %q, want %q", actual, want)
	}
	changedLiteral := "INSERT INTO parent_guard (identity) SELECT 'guard.parent  missing' FROM `guard table`"
	if normalizedSQL(externalSpacing) == normalizedSQL(changedLiteral) {
		t.Fatal("normalization collapsed whitespace inside a SQL string literal")
	}
	changedQuotedIdentifier := "INSERT INTO parent_guard (identity) SELECT 'guard.parent missing' FROM `guard  table`"
	if normalizedSQL(externalSpacing) == normalizedSQL(changedQuotedIdentifier) {
		t.Fatal("normalization collapsed whitespace inside a quoted SQL identifier")
	}
}
