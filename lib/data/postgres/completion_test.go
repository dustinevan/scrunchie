package postgres

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dustinevan/scrunchie/lib/logic/sqledit"
)

func TestCompletionNamesListNonSystemRelationsAndSearchPath(t *testing.T) {
	session := testSession(t)
	executeSQL(t, session, "CREATE TEMP TABLE completion_parts (a int) PARTITION BY RANGE (a); CREATE TEMP TABLE completion_part_one PARTITION OF completion_parts FOR VALUES FROM (0) TO (10); CREATE TEMP SEQUENCE completion_sequence;")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	names, err := session.CompletionNames(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(names.SearchPath, "public") || !slices.Contains(names.SearchPath, "pg_catalog") {
		t.Fatalf("search path = %q", names.SearchPath)
	}
	for _, schema := range []string{"claims", "public", "pg_catalog", "information_schema"} {
		if !slices.Contains(names.Schemas, schema) {
			t.Fatalf("schemas %q lack %s", names.Schemas, schema)
		}
	}
	kinds := make(map[string]string)
	temp := ""
	for _, relation := range names.Relations {
		kinds[relation.Schema+"."+relation.Name] = relation.Kind
		if relation.Schema == "pg_catalog" || relation.Schema == "information_schema" {
			t.Fatalf("system relation %s.%s was listed", relation.Schema, relation.Name)
		}
		if relation.Name == "completion_parts" {
			temp = relation.Schema
		}
	}
	if kinds["claims.beneficiary"] != "table" || kinds["claims.md_beneficiaries"] != "view" || kinds["public.Mixed Case"] != "table" {
		t.Fatalf("relations = %v", kinds)
	}
	if temp == "" || kinds[temp+".completion_parts"] != "partitioned table" || kinds[temp+".completion_part_one"] != "" || kinds[temp+".completion_sequence"] != "" {
		t.Fatalf("partitions or sequences were listed, or the temporary schema was not: %v", kinds)
	}
	if !slices.Contains(names.Listed, "claims") || !slices.Contains(names.Listed, temp) || slices.Contains(names.Listed, "pg_catalog") {
		t.Fatalf("listed schemas = %q", names.Listed)
	}
}

func TestCompletionNamesCapEachSchema(t *testing.T) {
	session := testSession(t)
	executeSQL(t, session, "DO $$ BEGIN FOR i IN 1..2001 LOOP EXECUTE format('CREATE TEMP TABLE completion_cap_%s ()', i); END LOOP; END $$")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	names, err := session.CompletionNames(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int)
	temp := ""
	for _, relation := range names.Relations {
		counts[relation.Schema]++
		if strings.HasPrefix(relation.Name, "completion_cap_") {
			temp = relation.Schema
		}
	}
	if temp == "" || counts[temp] != completionSchemaRelations || slices.Contains(names.Listed, temp) || !slices.Contains(names.Listed, "claims") {
		t.Fatalf("temporary schema kept %d relations; listed %q", counts[temp], names.Listed)
	}
}

func TestQuoteIdentifierQuotesEveryKeywordThisServerReserves(t *testing.T) {
	session := testSession(t)
	batch := executeSQL(t, session, "SELECT word FROM pg_catalog.pg_get_keywords() WHERE catcode <> 'U' ORDER BY word")
	if len(batch.Results) != 1 || len(batch.Results[0].Rows) < 50 {
		t.Fatal("pg_get_keywords returned no reserved words")
	}
	for _, row := range batch.Results[0].Rows {
		word := row[0].Text
		if sqledit.QuoteIdentifier(word) == word {
			t.Errorf("%s is reserved by the server but completion inserts it bare", word)
		}
	}
	unreserved := executeSQL(t, session, "SELECT word FROM pg_catalog.pg_get_keywords() WHERE catcode = 'U' AND word IN ('name', 'type', 'key', 'comment', 'data', 'value')")
	for _, row := range unreserved.Results[0].Rows {
		if sqledit.QuoteIdentifier(row[0].Text) != row[0].Text {
			t.Errorf("unreserved %s was quoted", row[0].Text)
		}
	}
}
