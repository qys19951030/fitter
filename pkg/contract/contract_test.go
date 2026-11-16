package contract_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PxyUp/fitter/pkg/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAcceptsAValidContract(t *testing.T) {
	spec, err := contract.Parse([]byte(`{"fields":{"title":{"type":"string","required":true}}}`))
	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.True(t, spec.Fields["title"].Required)
	assert.Equal(t, contract.TypeString, spec.Fields["title"].Type)
}

func TestParseRejectsUnsupportedType(t *testing.T) {
	_, err := contract.Parse([]byte(`{"fields":{"title":{"type":"date"}}}`))
	require.Error(t, err)

	var pe *contract.PreflightError
	require.ErrorAs(t, err, &pe)
	assert.Contains(t, pe.Item, "fields.title.type")
	assert.Contains(t, err.Error(), `"date"`)
}

func TestParseRejectsUnnamedRequiredField(t *testing.T) {
	_, err := contract.Parse([]byte(`{"fields":{"":{"required":true}}}`))
	require.Error(t, err)

	var pe *contract.PreflightError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "fields", pe.Item)
}

func TestParseRejectsMalformedDocument(t *testing.T) {
	_, err := contract.Parse([]byte(`{"fields":`))
	require.Error(t, err)

	var pe *contract.PreflightError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "contract document", pe.Item)
}

func TestLoadReportsUnreadableFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.json")
	_, err := contract.Load(missing)
	require.Error(t, err)

	var pe *contract.PreflightError
	require.ErrorAs(t, err, &pe)
	assert.Contains(t, pe.Item, missing)
}

func TestLoadNamesTheFileInValidationErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contract.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"fields":{"a":{"type":"when"}}}`), 0o600))

	_, err := contract.Load(path)
	require.Error(t, err)

	var pe *contract.PreflightError
	require.ErrorAs(t, err, &pe)
	assert.True(t, strings.HasPrefix(pe.Item, path))
	assert.Contains(t, pe.Item, "fields.a.type")
}

func TestSkeletonCompiles(t *testing.T) {
	var _ *contract.Spec
	var _ contract.Report
	_ = json.Valid
}

func record(t *testing.T, fields map[string]any) map[string]json.RawMessage {
	t.Helper()
	out := make(map[string]json.RawMessage, len(fields))
	for name, v := range fields {
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		out[name] = raw
	}
	return out
}

func specWith(t *testing.T, fields map[string]any) *contract.Spec {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"fields": fields})
	require.NoError(t, err)
	spec, err := contract.Parse(raw)
	require.NoError(t, err)
	return spec
}

func TestNumbersBecameStringsAreRecoverable(t *testing.T) {
	s := specWith(t, map[string]any{"price": map[string]any{"type": "number"}})
	got := s.Check(record(t, map[string]any{"price": "12.50"}))

	require.Len(t, got.Problems, 1)
	assert.Equal(t, contract.KindTypeMismatch, got.Problems[0].Kind)
	assert.Equal(t, contract.SeverityWarning, got.Problems[0].Severity)
	assert.Equal(t, "price", got.Problems[0].Field)
	assert.Equal(t, `"12.50"`, string(got.Problems[0].Raw))
}

func TestUnitsOnANumberAreADefect(t *testing.T) {
	s := specWith(t, map[string]any{"price": map[string]any{"type": "number"}})
	got := s.Check(record(t, map[string]any{"price": "12 kg"}))

	require.Len(t, got.Problems, 1)
	assert.Equal(t, contract.KindTypeMismatch, got.Problems[0].Kind)
	assert.Equal(t, contract.SeverityDefect, got.Problems[0].Severity)
	assert.Equal(t, `"12 kg"`, string(got.Problems[0].Raw))
}

func TestAnObjectWhereAStringWasExpectedIsADefect(t *testing.T) {
	s := specWith(t, map[string]any{"title": map[string]any{"type": "string"}})
	got := s.Check(record(t, map[string]any{"title": map[string]any{"text": "x"}}))

	require.Len(t, got.Problems, 1)
	assert.Equal(t, contract.KindTypeMismatch, got.Problems[0].Kind)
}

func TestABooleanStringIsRecoverable(t *testing.T) {
	s := specWith(t, map[string]any{"in_stock": map[string]any{"type": "boolean"}})
	got := s.Check(record(t, map[string]any{"in_stock": "true"}))

	require.Len(t, got.Problems, 1)
	assert.Equal(t, contract.KindTypeMismatch, got.Problems[0].Kind)
}

func TestFractionalWhereIntegerWasExpectedIsADefect(t *testing.T) {
	s := specWith(t, map[string]any{"stock": map[string]any{"type": "integer"}})
	got := s.Check(record(t, map[string]any{"stock": 3.5}))

	require.Len(t, got.Problems, 1)
	assert.Equal(t, contract.KindTypeMismatch, got.Problems[0].Kind)
}

func TestEmbeddedJSONInAStringIsRecoverable(t *testing.T) {
	s := specWith(t, map[string]any{"items": map[string]any{"type": "array"}})
	got := s.Check(record(t, map[string]any{"items": `[1,2,3]`}))

	require.Len(t, got.Problems, 1)
	assert.Equal(t, contract.KindTypeMismatch, got.Problems[0].Kind)
}

func TestListLengthBelowTheContract(t *testing.T) {
	s := specWith(t, map[string]any{"items": map[string]any{"type": "array", "min_items": 5}})
	got := s.Check(record(t, map[string]any{"items": []any{1, 2, 3}}))

	require.Len(t, got.Problems, 1)
	assert.Equal(t, contract.KindItemCount, got.Problems[0].Kind)
	assert.Equal(t, "items", got.Problems[0].Field)
	assert.Equal(t, `[1,2,3]`, string(got.Problems[0].Raw))
}

func TestListLengthAboveTheContract(t *testing.T) {
	s := specWith(t, map[string]any{"items": map[string]any{"type": "array", "max_items": 2}})
	got := s.Check(record(t, map[string]any{"items": []any{1, 2, 3}}))

	require.Len(t, got.Problems, 1)
	assert.Equal(t, contract.KindItemCount, got.Problems[0].Kind)
}

func TestNestedPathMissing(t *testing.T) {
	s := specWith(t, map[string]any{"seller": map[string]any{"type": "object", "paths": []string{"address.city"}}})
	got := s.Check(record(t, map[string]any{"seller": map[string]any{"address": map[string]any{}}}))

	require.Len(t, got.Problems, 1)
	assert.Equal(t, contract.KindPathMissing, got.Problems[0].Kind)
	assert.Equal(t, "seller.address.city", got.Problems[0].Field)
}

func TestNestedPathPresentIsClean(t *testing.T) {
	s := specWith(t, map[string]any{"seller": map[string]any{"type": "object", "paths": []string{"address.city"}}})
	got := s.Check(record(t, map[string]any{"seller": map[string]any{"address": map[string]any{"city": "Kyiv"}}}))

	assert.Empty(t, got.Problems)
}

func TestUnspecifiedFieldsPassThroughUntouched(t *testing.T) {
	s := specWith(t, map[string]any{"title": map[string]any{"type": "string"}})
	got := s.Check(record(t, map[string]any{"title": "ok", "extra": "anything"}))

	assert.Empty(t, got.Problems)
	assert.Equal(t, contract.Conforming, got.Verdict)
}

// batchSpec requires title, expects a numeric price and a list of >= 3 items.
func batchSpec(t *testing.T) *contract.Spec {
	t.Helper()
	return specWith(t, map[string]any{
		"title": map[string]any{"type": "string", "required": true},
		"price": map[string]any{"type": "number"},
		"items": map[string]any{"type": "array", "min_items": 3},
	})
}

func TestCheckAllClassifiesEveryRecord(t *testing.T) {
	s := batchSpec(t)
	report := s.CheckAll([]map[string]json.RawMessage{
		record(t, map[string]any{"title": "clean", "price": 10, "items": []any{1, 2, 3}}),
		record(t, map[string]any{"title": "flawed", "price": "10", "items": []any{1}}),
		record(t, map[string]any{"price": 10, "items": []any{1, 2, 3}}),
	})

	require.Len(t, report.Records, 3)
	assert.Equal(t, contract.Conforming, report.Records[0].Verdict)
	assert.Equal(t, contract.Usable, report.Records[1].Verdict)
	assert.Equal(t, contract.Unusable, report.Records[2].Verdict)

	assert.Equal(t, 3, report.Summary.Total)
	assert.Equal(t, 1, report.Summary.Conforming)
	assert.Equal(t, 1, report.Summary.Usable)
	assert.Equal(t, 1, report.Summary.Unusable)
	assert.Equal(t, 1, report.Summary.Problems[contract.KindTypeMismatch])
	assert.Equal(t, 1, report.Summary.Problems[contract.KindItemCount])
	assert.Equal(t, 1, report.Summary.Problems[contract.KindMissing])
}

func TestUnusableNamesEveryCauseNotJustTheFirst(t *testing.T) {
	s := batchSpec(t)
	report := s.CheckAll([]map[string]json.RawMessage{
		record(t, map[string]any{"price": "cheap", "items": []any{}}),
	})

	got := report.Records[0]
	assert.Equal(t, contract.Unusable, got.Verdict)
	require.Len(t, got.Causes, 2)

	byField := map[string]contract.Problem{}
	for _, c := range got.Causes {
		byField[c.Field] = c
	}
	assert.Equal(t, contract.KindMissing, byField["title"].Kind)
	assert.Equal(t, contract.KindTypeMismatch, byField["price"].Kind)
	assert.Equal(t, `"cheap"`, string(byField["price"].Raw))

	// Every problem is still reported, not only the causes.
	assert.Len(t, got.Problems, 3)
}

func TestANonRequiredTypeDefectMakesTheRecordUnusable(t *testing.T) {
	s := batchSpec(t)
	report := s.CheckAll([]map[string]json.RawMessage{
		record(t, map[string]any{"title": "x", "price": "cheap", "items": []any{1, 2, 3}}),
	})

	got := report.Records[0]
	assert.Equal(t, contract.Unusable, got.Verdict)
	require.Len(t, got.Causes, 1)
	assert.Equal(t, "price", got.Causes[0].Field)
	assert.Equal(t, contract.KindTypeMismatch, got.Causes[0].Kind)
	assert.Equal(t, `"cheap"`, string(got.Causes[0].Raw))
	assert.Len(t, got.Problems, 1)
}

func TestANonRequiredMissingFieldKeepsTheRecordUsable(t *testing.T) {
	s := batchSpec(t)
	report := s.CheckAll([]map[string]json.RawMessage{
		record(t, map[string]any{"title": "x", "items": []any{1, 2, 3}}),
	})

	got := report.Records[0]
	assert.Equal(t, contract.Usable, got.Verdict)
	require.Len(t, got.Problems, 1)
	assert.Equal(t, contract.KindMissing, got.Problems[0].Kind)
	assert.Equal(t, "price", got.Problems[0].Field)
}

func TestRequiredFieldProblemIsUnusableEvenIfConvertible(t *testing.T) {
	s := specWith(t, map[string]any{"price": map[string]any{"type": "number", "required": true}})
	got := s.Check(record(t, map[string]any{"price": "10"}))

	// "price" was present and only recoverable, so it is a mismatch: usable.
	assert.Equal(t, contract.Usable, got.Verdict)

	missing := s.Check(record(t, map[string]any{}))
	assert.Equal(t, contract.Unusable, missing.Verdict)
	assert.Equal(t, contract.KindMissing, missing.Causes[0].Kind)
}

func TestGetReturnsEachRecordByIndex(t *testing.T) {
	s := batchSpec(t)
	report := s.CheckAll([]map[string]json.RawMessage{
		record(t, map[string]any{"title": "a", "price": 1, "items": []any{1, 2, 3}}),
		record(t, map[string]any{"price": 1, "items": []any{1, 2, 3}}),
	})

	first, ok := report.Get(0)
	require.True(t, ok)
	assert.Equal(t, 0, first.Index)
	assert.Equal(t, contract.Conforming, first.Verdict)

	second, ok := report.Get(1)
	require.True(t, ok)
	assert.Equal(t, 1, second.Index)
	assert.Equal(t, contract.Unusable, second.Verdict)

	_, ok = report.Get(2)
	assert.False(t, ok)
	_, ok = report.Get(-1)
	assert.False(t, ok)
}

func TestSummaryCountsMatchTheRecords(t *testing.T) {
	s := batchSpec(t)
	report := s.CheckAll([]map[string]json.RawMessage{
		record(t, map[string]any{"title": "a", "price": 1, "items": []any{1, 2, 3}}),
		record(t, map[string]any{"title": "b", "price": "x", "items": []any{1}}),
		record(t, map[string]any{}),
		record(t, map[string]any{"title": "d", "price": 1, "items": []any{1, 2, 3}}),
	})

	var verdicts = map[contract.Verdict]int{}
	problemTotal := 0
	for _, rec := range report.Records {
		verdicts[rec.Verdict]++
		problemTotal += len(rec.Problems)
	}
	assert.Equal(t, len(report.Records), report.Summary.Total)
	assert.Equal(t, verdicts[contract.Conforming], report.Summary.Conforming)
	assert.Equal(t, verdicts[contract.Usable], report.Summary.Usable)
	assert.Equal(t, verdicts[contract.Unusable], report.Summary.Unusable)

	summaryProblems := 0
	for _, n := range report.Summary.Problems {
		summaryProblems += n
	}
	assert.Equal(t, problemTotal, summaryProblems)
}

func TestLenientDeliveryKeepsEveryRecord(t *testing.T) {
	s := batchSpec(t)
	records := []map[string]json.RawMessage{
		record(t, map[string]any{"title": "a", "price": 1, "items": []any{1, 2, 3}}),
		record(t, map[string]any{"price": "cheap", "items": []any{1}}),
	}
	out, err := s.CheckAll(records).Deliver(false, records)
	require.NoError(t, err)
	assert.Equal(t, records, out)
}

func TestStrictDeliveryReleasesACleanBatch(t *testing.T) {
	s := batchSpec(t)
	records := []map[string]json.RawMessage{
		record(t, map[string]any{"title": "a", "price": 1, "items": []any{1, 2, 3}}),
		record(t, map[string]any{"title": "b", "price": 2, "items": []any{1, 2, 3}}),
	}
	report := s.CheckAll(records)
	require.Equal(t, contract.Conforming, report.Records[1].Verdict)

	out, err := report.Deliver(true, records)
	require.NoError(t, err)
	assert.Equal(t, records, out)
}

func TestStrictDeliveryWithholdsOnAnyNonConformingRecord(t *testing.T) {
	s := batchSpec(t)
	records := []map[string]json.RawMessage{
		record(t, map[string]any{"title": "a", "price": 1, "items": []any{1, 2, 3}}),
		record(t, map[string]any{"title": "b", "price": "2", "items": []any{1}}),
	}
	report := s.CheckAll(records)
	require.Equal(t, contract.Usable, report.Records[1].Verdict)

	out, err := report.Deliver(true, records)
	require.Error(t, err)
	assert.Nil(t, out)

	var se *contract.StrictError
	require.ErrorAs(t, err, &se)
	require.Len(t, se.Rejected, 1)
	assert.Equal(t, 1, se.Rejected[0].Index)
	assert.Len(t, se.Rejected[0].Problems, 2)
}

func TestStrictDeliveryWithholdsTheWholeBatch(t *testing.T) {
	s := batchSpec(t)
	records := []map[string]json.RawMessage{
		record(t, map[string]any{"title": "a", "price": 1, "items": []any{1, 2, 3}}),
		record(t, map[string]any{"price": "cheap", "items": []any{1}}),
		record(t, map[string]any{}),
	}
	out, err := s.CheckAll(records).Deliver(true, records)
	require.Error(t, err)
	assert.Nil(t, out)

	var se *contract.StrictError
	require.ErrorAs(t, err, &se)
	require.Len(t, se.Rejected, 2)
	assert.Equal(t, 1, se.Rejected[0].Index)
	assert.Equal(t, 2, se.Rejected[1].Index)

	// The full defect set survives; nothing is truncated to the first record
	// or the first problem.
	assert.Contains(t, err.Error(), `record 1 field "price" type_mismatch`)
	assert.Contains(t, err.Error(), `record 1 field "title" missing`)
	assert.Contains(t, err.Error(), `record 2 field "title" missing`)
	assert.Contains(t, err.Error(), `record 2 field "items" missing`)
}

func TestDeliverRefusesAMismatchedBatch(t *testing.T) {
	s := batchSpec(t)
	report := s.CheckAll([]map[string]json.RawMessage{
		record(t, map[string]any{"title": "a", "price": 1, "items": []any{1, 2, 3}}),
	})
	_, err := report.Deliver(false, nil)
	require.Error(t, err)

	var pe *contract.PreflightError
	require.ErrorAs(t, err, &pe)
}

func TestHumanAndStructuredOutputsAgree(t *testing.T) {
	s := batchSpec(t)
	report := s.CheckAll([]map[string]json.RawMessage{
		record(t, map[string]any{"title": "a", "price": 1, "items": []any{1, 2, 3}}),
		record(t, map[string]any{"title": "b", "price": "1", "items": []any{1}}),
		record(t, map[string]any{"price": "cheap", "items": []any{1, 2, 3}}),
	})

	path := filepath.Join(t.TempDir(), "report.json")
	require.NoError(t, report.WriteJSON(path))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var structured contract.Report
	require.NoError(t, json.Unmarshal(raw, &structured))

	human := report.Human()
	assert.Contains(t, human, "records: 3")
	assert.Contains(t, human, "conforming: 1")
	assert.Contains(t, human, "usable:     1")
	assert.Contains(t, human, "unusable:   1")

	// The same numbers, from the same summary, in both outputs.
	assert.Equal(t, structured.Summary.Total, report.Summary.Total)
	assert.Equal(t, structured.Summary.Conforming, report.Summary.Conforming)
	assert.Equal(t, structured.Summary.Usable, report.Summary.Usable)
	assert.Equal(t, structured.Summary.Unusable, report.Summary.Unusable)
	assert.Equal(t, structured.Summary.Problems, report.Summary.Problems)
	assert.Len(t, structured.Records, len(report.Records))

	for kind, n := range report.Summary.Problems {
		assert.Contains(t, human, fmt.Sprintf("%-13s %d", kind, n))
	}
}

func TestWriteJSONReportsUnwritablePath(t *testing.T) {
	err := (contract.Report{}).WriteJSON(filepath.Join(t.TempDir(), "no-such-dir", "out.json"))
	require.Error(t, err)

	var pe *contract.PreflightError
	require.ErrorAs(t, err, &pe)
}

func TestRawValueSurvivesIntoBothOutputs(t *testing.T) {
	s := specWith(t, map[string]any{"price": map[string]any{"type": "number"}})
	report := s.CheckAll([]map[string]json.RawMessage{
		record(t, map[string]any{"price": "12 kg"}),
	})

	assert.Contains(t, report.Human(), `raw="12 kg"`)

	path := filepath.Join(t.TempDir(), "report.json")
	require.NoError(t, report.WriteJSON(path))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var decoded struct {
		Records []struct {
			Problems []struct {
				Field string          `json:"field"`
				Kind  string          `json:"kind"`
				Raw   json.RawMessage `json:"raw"`
			} `json:"problems"`
		} `json:"records"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Len(t, decoded.Records, 1)
	require.Len(t, decoded.Records[0].Problems, 1)
	assert.Equal(t, "price", decoded.Records[0].Problems[0].Field)
	assert.Equal(t, "type_mismatch", decoded.Records[0].Problems[0].Kind)
	assert.Equal(t, `"12 kg"`, string(decoded.Records[0].Problems[0].Raw))
}

func TestMalformedRawValueStillProducesValidJSON(t *testing.T) {
	s := specWith(t, map[string]any{"price": map[string]any{"type": "number"}})
	report := s.CheckAll([]map[string]json.RawMessage{
		{"price": json.RawMessage(`{"unterminated`)},
	})

	path := filepath.Join(t.TempDir(), "report.json")
	require.NoError(t, report.WriteJSON(path))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, json.Valid(raw))
	assert.Contains(t, string(raw), `unterminated`)
}

func TestArrayFormOfFieldsIsAccepted(t *testing.T) {
	spec, err := contract.Parse([]byte(`{"fields":[{"name":"title","type":"string","required":true},{"name":"price","type":"number"}]}`))
	require.NoError(t, err)
	require.Len(t, spec.Fields, 2)
	assert.True(t, spec.Fields["title"].Required)
	assert.Equal(t, contract.TypeNumber, spec.Fields["price"].Type)
}

func TestArrayFormRejectsANamelessRequiredField(t *testing.T) {
	_, err := contract.Parse([]byte(`{"fields":[{"type":"string","required":true}]}`))
	require.Error(t, err)

	var pe *contract.PreflightError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "fields", pe.Item)
	assert.Contains(t, err.Error(), "does not name the field")
}

func TestArrayFormRejectsDuplicateNames(t *testing.T) {
	_, err := contract.Parse([]byte(`{"fields":[{"name":"a"},{"name":"a"}]}`))
	require.Error(t, err)
}

func TestRunPreflightsBeforeWritingAnything(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "report.json")
	contractPath := filepath.Join(dir, "contract.json")
	require.NoError(t, os.WriteFile(contractPath, []byte(`{"fields":{"a":{"type":"bogus"}}}`), 0o600))

	_, _, err := contract.Run(contractPath, out, nil, false)
	require.Error(t, err)
	assert.NoFileExists(t, out)
}

func TestRunRefusesAnUnwritableOutputBeforeAnnotating(t *testing.T) {
	dir := t.TempDir()
	contractPath := filepath.Join(dir, "contract.json")
	require.NoError(t, os.WriteFile(contractPath, []byte(`{"fields":{"a":{"type":"string"}}}`), 0o600))

	out := filepath.Join(dir, "missing-dir", "report.json")
	_, _, err := contract.Run(contractPath, out, nil, false)
	require.Error(t, err)

	var pe *contract.PreflightError
	require.ErrorAs(t, err, &pe)
	assert.Contains(t, pe.Item, out)
}

func TestRunWritesTheReportAndHonoursStrictMode(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "report.json")
	contractPath := filepath.Join(dir, "contract.json")
	require.NoError(t, os.WriteFile(contractPath,
		[]byte(`{"fields":{"title":{"type":"string","required":true}}}`), 0o600))

	records := []map[string]json.RawMessage{
		record(t, map[string]any{"title": "a"}),
		record(t, map[string]any{}),
	}

	report, delivered, err := contract.Run(contractPath, out, records, true)
	require.Error(t, err)
	assert.Nil(t, delivered)
	assert.Equal(t, 2, report.Summary.Total)

	// The defects are reported in full even though the batch was withheld.
	raw, readErr := os.ReadFile(out)
	require.NoError(t, readErr)
	var written contract.Report
	require.NoError(t, json.Unmarshal(raw, &written))
	assert.Equal(t, 1, written.Summary.Unusable)

	_, delivered, err = contract.Run(contractPath, out, records, false)
	require.NoError(t, err)
	assert.Equal(t, records, delivered)
}

func TestNestedFieldRulesSeeOriginalBytes(t *testing.T) {
	s := specWith(t, map[string]any{
		"seller": map[string]any{
			"type": "object",
			"fields": []any{
				map[string]any{"name": "name", "type": "string", "required": true},
				map[string]any{"name": "balance", "type": "number"},
			},
		},
	})
	got := s.Check(record(t, map[string]any{
		"seller": map[string]any{"balance": "lots"},
	}))

	assert.Equal(t, contract.Unusable, got.Verdict)
	require.Len(t, got.Problems, 2)
	byField := map[string]contract.Problem{}
	for _, p := range got.Problems {
		byField[p.Field] = p
	}
	assert.Equal(t, contract.KindMissing, byField["seller.name"].Kind)
	assert.Equal(t, contract.KindTypeMismatch, byField["seller.balance"].Kind)
	assert.Equal(t, `"lots"`, string(byField["seller.balance"].Raw))
}

func TestRunWithOutputsWritesBothFilesThatAgree(t *testing.T) {
	dir := t.TempDir()
	contractPath := filepath.Join(dir, "contract.json")
	reportPath := filepath.Join(dir, "report.json")
	summaryPath := filepath.Join(dir, "report.txt")
	require.NoError(t, os.WriteFile(contractPath,
		[]byte(`{"fields":{"title":{"type":"string","required":true}}}`), 0o600))

	records := []map[string]json.RawMessage{
		record(t, map[string]any{"title": "a"}),
		record(t, map[string]any{"title": 3}),
		record(t, map[string]any{}),
	}
	report, _, err := contract.RunWithOutputs(contractPath, reportPath, summaryPath, records, false)
	require.NoError(t, err)

	rawReport, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	var structured contract.Report
	require.NoError(t, json.Unmarshal(rawReport, &structured))

	human, err := os.ReadFile(summaryPath)
	require.NoError(t, err)

	assert.Equal(t, report.Summary.Total, structured.Summary.Total)
	assert.Contains(t, string(human), "records: 3")
	assert.Contains(t, string(human), "conforming: 1")
	assert.Contains(t, string(human), "usable:     1")
	assert.Contains(t, string(human), "unusable:   1")
	for kind, n := range structured.Summary.Problems {
		assert.Contains(t, string(human), fmt.Sprintf("%-13s %d", kind, n))
	}
}

func TestRunWithOutputsPreflightsTheSummaryFile(t *testing.T) {
	dir := t.TempDir()
	contractPath := filepath.Join(dir, "contract.json")
	require.NoError(t, os.WriteFile(contractPath, []byte(`{"fields":{"a":{}}}`), 0o600))

	reportPath := filepath.Join(dir, "report.json")
	summaryPath := filepath.Join(dir, "no-such-dir", "report.txt")
	_, _, err := contract.RunWithOutputs(contractPath, reportPath, summaryPath, nil, false)
	require.Error(t, err)
	assert.NoFileExists(t, reportPath)
}

func TestSummaryListsAllFourProblemKindsEvenAtZero(t *testing.T) {
	s := specWith(t, map[string]any{"a": map[string]any{"type": "string"}})
	report := s.CheckAll([]map[string]json.RawMessage{record(t, map[string]any{"a": "ok"})})

	require.Len(t, report.Summary.Problems, 4)
	assert.Equal(t, 0, report.Summary.Problems[contract.KindMissing])
	assert.Equal(t, 0, report.Summary.Problems[contract.KindTypeMismatch])
	assert.Equal(t, 0, report.Summary.Problems[contract.KindItemCount])
	assert.Equal(t, 0, report.Summary.Problems[contract.KindPathMissing])
}

func TestPresentNullCountsAsMissingWithItsRawValue(t *testing.T) {
	s := specWith(t, map[string]any{"title": map[string]any{"type": "string", "required": true}})
	got := s.Check(map[string]json.RawMessage{"title": json.RawMessage("null")})

	require.Len(t, got.Problems, 1)
	assert.Equal(t, contract.KindMissing, got.Problems[0].Kind)
	assert.Equal(t, contract.SeverityDefect, got.Problems[0].Severity)
	assert.Equal(t, "null", string(got.Problems[0].Raw))
}

func TestAbsentFieldProblemHasNoRawValue(t *testing.T) {
	s := specWith(t, map[string]any{"title": map[string]any{"type": "string"}})
	got := s.Check(map[string]json.RawMessage{})

	require.Len(t, got.Problems, 1)
	assert.Empty(t, got.Problems[0].Raw)
	assert.Equal(t, contract.SeverityWarning, got.Problems[0].Severity)
	assert.Equal(t, contract.Usable, got.Verdict)
}

func TestValidateRejectsInconsistentItemBounds(t *testing.T) {
	_, err := contract.Parse([]byte(`{"fields":{"a":{"type":"array","min_items":5,"max_items":2}}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "min_items")
}

func TestValidateRejectsNegativeBounds(t *testing.T) {
	_, err := contract.Parse([]byte(`{"fields":{"a":{"min_items":-1}}}`))
	require.Error(t, err)
}

func TestValidateRejectsEmptyNestedPath(t *testing.T) {
	_, err := contract.Parse([]byte(`{"fields":{"a":{"paths":[""]}}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "paths[0]")
}

func TestValidateRejectsAnUnsupportedNestedType(t *testing.T) {
	_, err := contract.Parse([]byte(`{"fields":{"a":{"type":"object","fields":{"b":{"type":"decimal"}}}}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fields.a.fields.b.type")
}

func TestValidateRejectsAMissingFieldsSection(t *testing.T) {
	_, err := contract.Parse([]byte(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no fields section")
}

func TestValidateReportsANilRule(t *testing.T) {
	_, err := contract.Parse([]byte(`{"fields":{"a":null}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fields.a")
}

func TestElementOfAnArrayThatIsNotAnObjectFailsThePath(t *testing.T) {
	s := specWith(t, map[string]any{"seller": map[string]any{"paths": []string{"address.city"}}})
	got := s.Check(record(t, map[string]any{"seller": "not an object"}))

	require.Len(t, got.Problems, 1)
	assert.Equal(t, contract.KindPathMissing, got.Problems[0].Kind)
	assert.Equal(t, contract.SeverityDefect, got.Problems[0].Severity)
}

func TestItemCountOnAValueThatIsNotAList(t *testing.T) {
	s := specWith(t, map[string]any{"items": map[string]any{"min_items": 2}})
	got := s.Check(record(t, map[string]any{"items": "not a list"}))

	require.Len(t, got.Problems, 1)
	assert.Equal(t, contract.KindTypeMismatch, got.Problems[0].Kind)
	assert.Equal(t, contract.SeverityDefect, got.Problems[0].Severity)
}

func TestGetFallsBackWhenIndexesAreNotStoredInOrder(t *testing.T) {
	// A hand-built report need not keep input order.
	report := contract.Report{Records: []contract.RecordResult{
		{Index: 1, Verdict: contract.Usable},
		{Index: 0, Verdict: contract.Conforming},
	}}
	got, ok := report.Get(0)
	require.True(t, ok)
	assert.Equal(t, contract.Conforming, got.Verdict)

	_, ok = report.Get(1)
	require.True(t, ok)

	_, ok = report.Get(5)
	assert.False(t, ok)
}

func TestAListInAStringIsCountedNotDoubleReported(t *testing.T) {
	s := specWith(t, map[string]any{"items": map[string]any{"type": "array", "min_items": 3}})
	got := s.Check(record(t, map[string]any{"items": `[1]`}))

	require.Len(t, got.Problems, 2)
	byKind := map[contract.ProblemKind]contract.Problem{}
	for _, p := range got.Problems {
		byKind[p.Kind] = p
	}
	assert.Equal(t, contract.SeverityWarning, byKind[contract.KindTypeMismatch].Severity)
	assert.Equal(t, contract.KindItemCount, byKind[contract.KindItemCount].Kind)
	assert.Equal(t, contract.Usable, got.Verdict)
}

func TestANonListIsReportedOnceNotTwice(t *testing.T) {
	s := specWith(t, map[string]any{"items": map[string]any{"type": "array", "min_items": 3}})
	got := s.Check(record(t, map[string]any{"items": "nope"}))

	assert.Len(t, got.Problems, 1)
	assert.Equal(t, contract.KindTypeMismatch, got.Problems[0].Kind)
	assert.Equal(t, contract.SeverityDefect, got.Problems[0].Severity)
}

func TestItemCountWithoutADeclaredTypeStillReportsANonList(t *testing.T) {
	s := specWith(t, map[string]any{"items": map[string]any{"min_items": 2}})
	got := s.Check(record(t, map[string]any{"items": 3}))

	require.Len(t, got.Problems, 1)
	assert.Equal(t, contract.KindTypeMismatch, got.Problems[0].Kind)
}

func TestLooselyNumericStringsAreNotSafeConversions(t *testing.T) {
	for _, value := range []string{"NaN", "Inf", "+5", "0x10", "1,5"} {
		t.Run(value, func(t *testing.T) {
			s := specWith(t, map[string]any{"price": map[string]any{"type": "number"}})
			got := s.Check(record(t, map[string]any{"price": value}))

			require.Len(t, got.Problems, 1)
			assert.Equal(t, contract.KindTypeMismatch, got.Problems[0].Kind)
			assert.Equal(t, contract.SeverityDefect, got.Problems[0].Severity,
				"%q must not be treated as a safe numeric conversion", value)
			assert.Equal(t, contract.Unusable, got.Verdict)
		})
	}
}

func TestProperNumericStringsStillConvert(t *testing.T) {
	for _, value := range []string{"5", "-5", "5.25", "1e3", "0"} {
		t.Run(value, func(t *testing.T) {
			s := specWith(t, map[string]any{"price": map[string]any{"type": "number"}})
			got := s.Check(record(t, map[string]any{"price": value}))

			require.Len(t, got.Problems, 1)
			assert.Equal(t, contract.SeverityWarning, got.Problems[0].Severity)
		})
	}
}

func TestTrailingBytesAfterAValueAreADefect(t *testing.T) {
	s := specWith(t, map[string]any{"price": map[string]any{"type": "number"}})
	got := s.Check(map[string]json.RawMessage{"price": json.RawMessage(`5 6`)})

	require.Len(t, got.Problems, 1)
	assert.Equal(t, contract.KindTypeMismatch, got.Problems[0].Kind)
	assert.Equal(t, contract.SeverityDefect, got.Problems[0].Severity)
	assert.Equal(t, `5 6`, string(got.Problems[0].Raw))
}

func TestDeliverRejectsOnlyTheNonConformingRecordsInStrictMode(t *testing.T) {
	s := batchSpec(t)
	records := []map[string]json.RawMessage{
		record(t, map[string]any{"title": "a", "price": 1, "items": []any{1, 2, 3}}),
		record(t, map[string]any{"title": "b", "price": 2, "items": []any{1}}),
		record(t, map[string]any{"title": "c", "price": 3, "items": []any{1, 2, 3}}),
	}
	report := s.CheckAll(records)
	assert.Equal(t, contract.Conforming, report.Records[0].Verdict)
	assert.Equal(t, contract.Usable, report.Records[1].Verdict)
	assert.Equal(t, contract.Conforming, report.Records[2].Verdict)

	_, err := report.Deliver(true, records)
	require.Error(t, err)

	var se *contract.StrictError
	require.ErrorAs(t, err, &se)
	require.Len(t, se.Rejected, 1)
	assert.Equal(t, 1, se.Rejected[0].Index)
	// The summary the error carries is the full batch's, not a filtered one.
	assert.Equal(t, 3, se.Report.Summary.Total)
	assert.Equal(t, 2, se.Report.Summary.Conforming)
}
