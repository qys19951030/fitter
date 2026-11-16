// Package contract validates already-extracted records against a field
// contract and annotates each record with completeness information.
//
// It deliberately sits outside the extraction pipeline: it only consumes the
// records a run has already produced (a field-name to value mapping per
// record), never touches connectors, parsers, builders or the run-time
// configuration, and leaves fields the contract does not mention exactly as
// they are.
package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Type is a type name accepted in a contract.
type Type string

const (
	TypeString  Type = "string"
	TypeNumber  Type = "number"
	TypeInteger Type = "integer"
	TypeBoolean Type = "boolean"
	TypeObject  Type = "object"
	TypeArray   Type = "array"
)

// Valid reports whether t is a type the contract checker understands.
func (t Type) Valid() bool {
	switch t {
	case TypeString, TypeNumber, TypeInteger, TypeBoolean, TypeObject, TypeArray:
		return true
	}
	return false
}

// PreflightError reports a problem found before any record is examined, or
// before a partially annotated batch could be written out. Item names the
// exact contract entry, file or record position at fault.
type PreflightError struct {
	// Item is the contract path, file path or record index at fault.
	Item string
	// Err is the underlying cause, when there is one.
	Err error
}

func (e *PreflightError) Error() string {
	if e.Err == nil {
		return e.Item
	}
	return e.Item + ": " + e.Err.Error()
}

func (e *PreflightError) Unwrap() error { return e.Err }

func preflightf(item, format string, args ...any) *PreflightError {
	return &PreflightError{Item: item, Err: fmt.Errorf(format, args...)}
}

// FieldRule is one field's expectations.
type FieldRule struct {
	// Type is the expected type. It is optional.
	Type Type `json:"type"`
	// Required marks a field whose absence makes the record unusable.
	Required bool `json:"required"`
	// MinItems / MaxItems bound a list's length. Zero means unbounded.
	MinItems int `json:"min_items"`
	MaxItems int `json:"max_items"`
	// Paths are nested paths that must exist inside the field's value.
	Paths []string `json:"paths"`
	// Fields are rules for nested object fields.
	Fields Fields `json:"fields"`
}

// Fields is the contract's per-field rule set.
//
// In JSON it may be written either as an object keyed by field name or as an
// array of entries carrying a "name". The array form is what lets a contract
// state a name-less field, which preflight then rejects.
type Fields map[string]*FieldRule

// fieldEntry is the array form of a field declaration.
type fieldEntry struct {
	Name string `json:"name"`
	FieldRule
}

// UnmarshalJSON accepts both the object and the array spelling.
func (f *Fields) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*f = nil
		return nil
	}
	if trimmed[0] == '[' {
		var entries []fieldEntry
		if err := json.Unmarshal(trimmed, &entries); err != nil {
			return err
		}
		out := make(Fields, len(entries))
		for _, entry := range entries {
			if _, dup := out[entry.Name]; dup {
				return fmt.Errorf("field %q is declared more than once", entry.Name)
			}
			rule := entry.FieldRule
			out[entry.Name] = &rule
		}
		*f = out
		return nil
	}

	var mapped map[string]*FieldRule
	if err := json.Unmarshal(trimmed, &mapped); err != nil {
		return err
	}
	*f = mapped
	return nil
}

// Spec is the whole contract document.
type Spec struct {
	Fields Fields `json:"fields"`
}

// ProblemKind classifies a single contract violation. There are exactly four
// kinds; a type mismatch carries its severity as a second layer.
type ProblemKind string

const (
	// KindMissing reports a contracted field absent from the record.
	KindMissing ProblemKind = "missing"
	// KindTypeMismatch reports a value whose type does not match the contract.
	// Its Severity says whether the value can be safely converted.
	KindTypeMismatch ProblemKind = "type_mismatch"
	// KindItemCount reports a list whose length falls outside the contract.
	KindItemCount ProblemKind = "item_count"
	// KindPathMissing reports a contracted nested path that does not exist.
	KindPathMissing ProblemKind = "path_missing"
)

// problemKindOrder fixes the order kinds appear in both outputs.
var problemKindOrder = []ProblemKind{
	KindMissing,
	KindTypeMismatch,
	KindItemCount,
	KindPathMissing,
}

// Severity is the second layer: a problem either only marks the record or
// makes it unusable.
type Severity string

const (
	// SeverityWarning is a problem the record can still be used with.
	SeverityWarning Severity = "warning"
	// SeverityDefect makes the record unusable.
	SeverityDefect Severity = "defect"
)

// Problem is one field-level finding with the value that produced it.
type Problem struct {
	Field    string      `json:"field"`
	Kind     ProblemKind `json:"kind"`
	Severity Severity    `json:"severity"`
	Required bool        `json:"required"`
	// Expectation describes what the contract asked for.
	Expectation string `json:"expectation"`
	// Raw is the value as it was extracted, never rewritten or dropped.
	Raw json.RawMessage `json:"raw"`
}

// MarshalJSON keeps the structured report valid JSON even when the original
// value was malformed enough to fail decoding: such bytes are preserved as a
// JSON string rather than silently replaced.
func (p Problem) MarshalJSON() ([]byte, error) {
	type plain Problem
	out := plain(p)
	if len(out.Raw) > 0 && !json.Valid(out.Raw) {
		quoted, err := json.Marshal(string(out.Raw))
		if err != nil {
			return nil, err
		}
		out.Raw = quoted
	}
	return json.Marshal(out)
}

// Verdict is the record-level classification.
type Verdict string

const (
	// Conforming records satisfy every contracted field.
	Conforming Verdict = "conforming"
	// Usable records have only warnings and can still be used.
	Usable Verdict = "usable"
	// Unusable records must not be delivered.
	Unusable Verdict = "unusable"
)

// RecordResult is the annotation of one input record.
type RecordResult struct {
	Index    int       `json:"index"`
	Verdict  Verdict   `json:"verdict"`
	Problems []Problem `json:"problems"`
	// Causes names the problems that made a record unusable.
	Causes []Problem `json:"causes,omitempty"`
}

// Report is the complete annotation of a batch.
type Report struct {
	Records []RecordResult `json:"records"`
	Summary Summary        `json:"summary"`
}

// Summary aggregates a Report.
type Summary struct {
	Total      int                 `json:"total"`
	Conforming int                 `json:"conforming"`
	Usable     int                 `json:"usable"`
	Unusable   int                 `json:"unusable"`
	Problems   map[ProblemKind]int `json:"problems"`
}

// Load reads and validates a contract document.
func Load(path string) (*Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &PreflightError{Item: path, Err: err}
	}
	spec, err := Parse(raw)
	if err != nil {
		if pe, ok := err.(*PreflightError); ok {
			pe.Item = path + ": " + pe.Item
			return nil, pe
		}
		return nil, &PreflightError{Item: path, Err: err}
	}
	return spec, nil
}

// Parse decodes and validates a contract document.
func Parse(raw []byte) (*Spec, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	spec := &Spec{}
	if err := dec.Decode(spec); err != nil {
		return nil, preflightf("contract document", "cannot parse: %v", err)
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	return spec, nil
}

// Validate checks the contract on its own, before any record is touched.
func (s *Spec) Validate() error {
	if s == nil {
		return preflightf("contract document", "is nil")
	}
	if s.Fields == nil {
		return preflightf("contract document", "has no fields section")
	}
	for _, name := range sortedFieldNames(s.Fields) {
		if strings.TrimSpace(name) == "" {
			if rule := s.Fields[name]; rule != nil && rule.Required {
				return preflightf("fields", "is marked required but the contract does not name the field")
			}
			return preflightf("fields", "contains a field with an empty name")
		}
		if err := validateRule("fields."+name, s.Fields[name]); err != nil {
			return err
		}
	}
	return nil
}

func validateRule(path string, rule *FieldRule) error {
	if rule == nil {
		return preflightf(path, "has no rule")
	}
	if rule.Type != "" && !rule.Type.Valid() {
		return preflightf(path+".type", "unsupported type %q", rule.Type)
	}
	if rule.MinItems < 0 {
		return preflightf(path+".min_items", "is negative (%d)", rule.MinItems)
	}
	if rule.MaxItems < 0 {
		return preflightf(path+".max_items", "is negative (%d)", rule.MaxItems)
	}
	if rule.MaxItems > 0 && rule.MinItems > rule.MaxItems {
		return preflightf(path, "min_items %d exceeds max_items %d", rule.MinItems, rule.MaxItems)
	}
	for i, p := range rule.Paths {
		if strings.TrimSpace(p) == "" {
			return preflightf(fmt.Sprintf("%s.paths[%d]", path, i), "is empty")
		}
	}
	for _, name := range sortedFieldNames(rule.Fields) {
		if strings.TrimSpace(name) == "" {
			return preflightf(path+".fields", "contains a field with an empty name")
		}
		if err := validateRule(path+".fields."+name, rule.Fields[name]); err != nil {
			return err
		}
	}
	return nil
}

func sortedFieldNames(fields Fields) []string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Check annotates one record against the contract. Fields the contract does
// not mention are ignored; they are never removed, rewritten or classified.
func (s *Spec) Check(record map[string]json.RawMessage) RecordResult {
	return s.check(indexUnknown, record)
}

const indexUnknown = -1

func (s *Spec) check(index int, record map[string]json.RawMessage) RecordResult {
	result := RecordResult{Index: index}
	if s != nil {
		checkFields("", s.Fields, record, &result.Problems)
	}
	result.Verdict, result.Causes = classify(result.Problems)
	return result
}

// checkFields walks a rule set against one record level.
func checkFields(prefix string, fields Fields, record map[string]json.RawMessage, problems *[]Problem) {
	for _, name := range sortedFieldNames(fields) {
		rule := fields[name]
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		raw, present := record[name]
		if !present || isJSONNull(raw) {
			// A contracted field the record does not carry is missing; whether
			// that makes the record unusable depends on Required, decided in
			// classify. A present null keeps its raw bytes: the original value
			// must survive even when it is absent-by-null.
			if !present {
				raw = nil
			}
			*problems = append(*problems, Problem{
				Field:       path,
				Kind:        KindMissing,
				Severity:    missingSeverity(rule.Required),
				Required:    rule.Required,
				Expectation: "field present",
				Raw:         raw,
			})
			continue
		}
		checkValue(path, rule, raw, problems)

		if len(rule.Fields) > 0 {
			nested, err := objectFields(raw)
			if err == nil {
				checkFields(path, rule.Fields, nested, problems)
			}
		}
	}
}

// objectFields splits an object into its members without re-encoding them, so
// nested rules see exactly the bytes the extractor produced.
func objectFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// isJSONNull reports whether raw is JSON null (or no value at all).
func isJSONNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || string(trimmed) == "null"
}

// classify derives the record verdict and, when unusable, the exact causes.
//
// A record is unusable when any problem is a defect: a required field is
// missing, a value's type cannot be safely converted, or a contracted nested
// path does not exist. Warnings — a missing non-required field, a list length
// outside the contract, a recoverable type difference — leave the record
// usable.
func classify(problems []Problem) (Verdict, []Problem) {
	if len(problems) == 0 {
		return Conforming, nil
	}
	var causes []Problem
	for _, p := range problems {
		if p.Severity == SeverityDefect {
			causes = append(causes, p)
		}
	}
	if len(causes) > 0 {
		return Unusable, causes
	}
	return Usable, nil
}

// missingSeverity: absence is a defect only for a required field.
func missingSeverity(required bool) Severity {
	if required {
		return SeverityDefect
	}
	return SeverityWarning
}

// jsonKind is the JSON-level kind of an extracted value.
type jsonKind string

const (
	kindNull    jsonKind = "null"
	kindString  jsonKind = "string"
	kindNumber  jsonKind = "number"
	kindBoolean jsonKind = "boolean"
	kindArray   jsonKind = "array"
	kindObject  jsonKind = "object"
)

// inspect decodes raw just enough to classify it. The raw bytes are never
// rewritten: problems always carry the original value.
func inspect(raw json.RawMessage) (jsonKind, any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", nil, err
	}
	// Trailing bytes mean this was not a single JSON value; classifying the
	// prefix would call a corrupt value well-formed.
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return "", nil, fmt.Errorf("trailing data after JSON value")
	}
	switch v.(type) {
	case nil:
		return kindNull, v, nil
	case string:
		return kindString, v, nil
	case json.Number:
		return kindNumber, v, nil
	case bool:
		return kindBoolean, v, nil
	case []any:
		return kindArray, v, nil
	case map[string]any:
		return kindObject, v, nil
	}
	return "", v, fmt.Errorf("unsupported JSON value")
}

// matches reports whether a value of kind k already satisfies t.
func matches(t Type, k jsonKind, v any) bool {
	switch t {
	case TypeString:
		return k == kindString
	case TypeNumber:
		return k == kindNumber
	case TypeInteger:
		if k != kindNumber {
			return false
		}
		n, _ := v.(json.Number)
		return isIntegral(n)
	case TypeBoolean:
		return k == kindBoolean
	case TypeObject:
		return k == kindObject
	case TypeArray:
		return k == kindArray
	}
	return false
}

// convertible reports whether a value of the wrong kind can be converted to t
// without losing information or guessing. Only these cases count as a
// recoverable mismatch; everything else is a defect.
func convertible(t Type, k jsonKind, v any) bool {
	switch t {
	case TypeString:
		return k == kindNumber || k == kindBoolean
	case TypeNumber:
		_, ok := parseNumberString(k, v)
		return ok
	case TypeInteger:
		if n, ok := parseNumberString(k, v); ok {
			return isIntegral(n)
		}
		return false
	case TypeBoolean:
		if s, ok := v.(string); ok && k == kindString {
			return isBoolString(s)
		}
		return false
	case TypeObject:
		_, ok := parseStructuredString(k, v, kindObject)
		return ok
	case TypeArray:
		_, ok := parseStructuredString(k, v, kindArray)
		return ok
	}
	return false
}

func parseNumberString(k jsonKind, v any) (json.Number, bool) {
	if k == kindNumber {
		return v.(json.Number), true
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	n := json.Number(strings.TrimSpace(s))
	if !isJSONNumber(string(n)) {
		return "", false
	}
	return n, true
}

// isJSONNumber reports whether s is a valid JSON number literal. Go's float
// parser is looser than JSON: it also accepts NaN, Inf and a leading "+", none
// of which a safe conversion may treat as a number.
func isJSONNumber(s string) bool {
	if s == "" {
		return false
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var n json.Number
	if err := dec.Decode(&n); err != nil {
		return false
	}
	var trailing any
	return dec.Decode(&trailing) == io.EOF
}

// isIntegral reports whether n denotes a whole number without losing anything.
func isIntegral(n json.Number) bool {
	f, err := n.Float64()
	if err != nil {
		return false
	}
	return f == math.Trunc(f) && f >= math.MinInt64 && f <= math.MaxInt64
}

// isBoolString reports whether s is the canonical textual form of a bool.
func isBoolString(s string) bool {
	return s == "true" || s == "false"
}

// parseStructuredString decodes a string that embeds a JSON value of want.
func parseStructuredString(k jsonKind, v any, want jsonKind) (any, bool) {
	if k != kindString {
		return nil, false
	}
	s, _ := v.(string)
	got, parsed, err := inspect(json.RawMessage(s))
	if err != nil || got != want {
		return nil, false
	}
	return parsed, true
}

// checkValue inspects one present value against rule and appends problems.
func checkValue(path string, rule *FieldRule, raw json.RawMessage, problems *[]Problem) {
	k, v, err := inspect(raw)
	if err != nil {
		*problems = append(*problems, Problem{
			Field:       path,
			Kind:        KindTypeMismatch,
			Severity:    SeverityDefect,
			Required:    rule.Required,
			Expectation: fmt.Sprintf("a JSON value decodable as %s", ruleTypeLabel(rule)),
			Raw:         raw,
		})
		return
	}

	if rule.Type != "" && !matches(rule.Type, k, v) {
		severity := SeverityDefect
		if convertible(rule.Type, k, v) {
			severity = SeverityWarning
		}
		*problems = append(*problems, Problem{
			Field:       path,
			Kind:        KindTypeMismatch,
			Severity:    severity,
			Required:    rule.Required,
			Expectation: fmt.Sprintf("type %s, got %s", rule.Type, k),
			Raw:         raw,
		})
	}

	if rule.MinItems > 0 || rule.MaxItems > 0 {
		checkItemCount(path, rule, k, v, raw, problems)
	}

	for _, p := range rule.Paths {
		checkPath(path, p, k, v, raw, rule.Required, problems)
	}
}

func ruleTypeLabel(rule *FieldRule) string {
	if rule.Type == "" {
		return "any type"
	}
	return string(rule.Type)
}

func checkItemCount(path string, rule *FieldRule, k jsonKind, v any, raw json.RawMessage, problems *[]Problem) {
	items, ok := v.([]any)
	if !ok || k != kindArray {
		// A string holding a JSON array is exactly the recoverable case the
		// type check already reported as a warning; count from the decoded
		// array instead of raising a second, contradicting problem.
		if decoded, parsed := parseStructuredString(k, v, kindArray); parsed {
			items, ok = decoded.([]any)
		}
		if !ok {
			// When the contract already demanded an array, the type check
			// reported this value; do not report the same fault twice.
			if rule.Type != TypeArray {
				*problems = append(*problems, Problem{
					Field:       path,
					Kind:        KindTypeMismatch,
					Severity:    SeverityDefect,
					Required:    rule.Required,
					Expectation: fmt.Sprintf("an array to check its item count, got %s", k),
					Raw:         raw,
				})
			}
			return
		}
	}
	n := len(items)
	bad := (rule.MinItems > 0 && n < rule.MinItems) || (rule.MaxItems > 0 && n > rule.MaxItems)
	if !bad {
		return
	}
	*problems = append(*problems, Problem{
		Field:       path,
		Kind:        KindItemCount,
		Severity:    SeverityWarning,
		Required:    rule.Required,
		Expectation: itemCountLabel(rule),
		Raw:         raw,
	})
}

func itemCountLabel(rule *FieldRule) string {
	switch {
	case rule.MinItems > 0 && rule.MaxItems > 0:
		return fmt.Sprintf("between %d and %d items", rule.MinItems, rule.MaxItems)
	case rule.MinItems > 0:
		return fmt.Sprintf("at least %d items", rule.MinItems)
	default:
		return fmt.Sprintf("at most %d items", rule.MaxItems)
	}
}

// checkPath walks a dotted path inside a value without inventing anything.
func checkPath(field, path string, k jsonKind, v any, raw json.RawMessage, required bool, problems *[]Problem) {
	cur := v
	curKind := k
	for _, segment := range strings.Split(path, ".") {
		if curKind != kindObject {
			appendPathProblem(field, path, raw, required, problems)
			return
		}
		obj, _ := cur.(map[string]any)
		next, ok := obj[segment]
		if !ok {
			appendPathProblem(field, path, raw, required, problems)
			return
		}
		cur = next
		switch cur.(type) {
		case nil:
			curKind = kindNull
		case string:
			curKind = kindString
		case json.Number:
			curKind = kindNumber
		case bool:
			curKind = kindBoolean
		case []any:
			curKind = kindArray
		case map[string]any:
			curKind = kindObject
		}
	}
}

func appendPathProblem(field, path string, raw json.RawMessage, required bool, problems *[]Problem) {
	*problems = append(*problems, Problem{
		Field:       field + "." + path,
		Kind:        KindPathMissing,
		Severity:    SeverityDefect,
		Required:    required,
		Expectation: "nested path " + path,
		Raw:         raw,
	})
}

// Run is the package's entry point: it loads the contract, applies every
// preflight check before annotating anything, annotates the batch, writes the
// structured report (and the human summary alongside it), and returns them.
// Nothing is written when a preflight check fails, so a half-parsed contract
// never produces a report.
//
// When strict is set, any record that does not fully conform withholds the
// whole batch from the returned records, but the report is still written with
// every defect.
func Run(contractPath, outputPath string, records []map[string]json.RawMessage, strict bool) (Report, []map[string]json.RawMessage, error) {
	return RunWithOutputs(contractPath, outputPath, "", records, strict)
}

// RunWithOutputs is Run with a separate human-summary file. An empty
// summaryPath writes the structured report only. Both outputs are preflighted
// before any record is annotated.
func RunWithOutputs(contractPath, reportPath, summaryPath string, records []map[string]json.RawMessage, strict bool) (Report, []map[string]json.RawMessage, error) {
	spec, err := Load(contractPath)
	if err != nil {
		return Report{}, nil, err
	}
	outputs := []string{reportPath}
	if summaryPath != "" && summaryPath != reportPath {
		outputs = append(outputs, summaryPath)
	}
	for _, path := range outputs {
		if err := checkWritable(path); err != nil {
			return Report{}, nil, err
		}
	}

	report := spec.CheckAll(records)
	delivered, deliverErr := report.Deliver(strict, records)
	// Write the full report even when strict mode withholds the batch: the
	// defects are the point of the run.
	if err := report.WriteOutputs(reportPath, summaryPath); err != nil {
		return Report{}, nil, err
	}
	return report, delivered, deliverErr
}

// checkWritable fails early if path cannot be written, so the run does not
// spend time annotating a batch it could never report on. It leaves no trace:
// a file it had to create is removed again, so a preflight failure can never be
// mistaken for an empty-but-complete report.
func checkWritable(path string) error {
	_, statErr := os.Stat(path)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return &PreflightError{Item: path, Err: err}
	}
	if closeErr := f.Close(); closeErr != nil {
		return &PreflightError{Item: path, Err: closeErr}
	}
	if os.IsNotExist(statErr) {
		if rmErr := os.Remove(path); rmErr != nil {
			return &PreflightError{Item: path, Err: rmErr}
		}
	}
	return nil
}

// CheckAll annotates every record and builds the batch summary.
func (s *Spec) CheckAll(records []map[string]json.RawMessage) Report {
	problems := make(map[ProblemKind]int, len(problemKindOrder))
	for _, kind := range problemKindOrder {
		problems[kind] = 0
	}
	report := Report{
		Records: make([]RecordResult, 0, len(records)),
		Summary: Summary{Total: len(records), Problems: problems},
	}
	for i, record := range records {
		result := s.check(i, record)
		report.Records = append(report.Records, result)
		switch result.Verdict {
		case Conforming:
			report.Summary.Conforming++
		case Usable:
			report.Summary.Usable++
		case Unusable:
			report.Summary.Unusable++
		}
		for _, p := range result.Problems {
			report.Summary.Problems[p.Kind]++
		}
	}
	return report
}

// Get returns the annotation for the record at index.
func (r Report) Get(index int) (RecordResult, bool) {
	if index < 0 || index >= len(r.Records) {
		return RecordResult{}, false
	}
	result := r.Records[index]
	if result.Index != index {
		// Results are stored in input order; a mismatch means the report was
		// built by hand or truncated, so fall back to a linear lookup.
		for _, candidate := range r.Records {
			if candidate.Index == index {
				return candidate, true
			}
		}
		return RecordResult{}, false
	}
	return result, true
}

// Deliver decides what a strict run releases from the batch.
//
// In lenient mode every record is released unchanged. In strict mode a single
// record that does not fully conform withholds the whole batch and the returned
// error carries the complete set of defects for every rejected record, not just
// the first one. A record count that does not match the report is a preflight
// failure: delivering a partial mapping would silently pair annotations with
// the wrong values.
func (r Report) Deliver(strict bool, records []map[string]json.RawMessage) ([]map[string]json.RawMessage, error) {
	if len(records) != r.Summary.Total || len(r.Records) != len(records) {
		return nil, preflightf("records",
			"batch has %d records but the report covers %d", len(records), len(r.Records))
	}
	if !strict {
		return records, nil
	}

	var rejected []RecordResult
	for _, rec := range r.Records {
		if rec.Verdict != Conforming {
			rejected = append(rejected, rec)
		}
	}
	if len(rejected) == 0 {
		return records, nil
	}
	return nil, &StrictError{Report: r, Rejected: rejected}
}

// StrictError reports a strict batch withheld because records did not conform.
// Rejected holds every rejected record with all of its defects.
type StrictError struct {
	Report   Report
	Rejected []RecordResult
}

func (e *StrictError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "strict mode withheld the batch: %d of %d records do not conform",
		len(e.Rejected), e.Report.Summary.Total)
	for _, rec := range e.Rejected {
		for _, p := range rec.Problems {
			fmt.Fprintf(&b, "; record %d field %q %s", rec.Index, p.Field, p.Kind)
		}
	}
	return b.String()
}

// WriteJSON writes the structured report to path. The file appears complete or
// not at all: the report is written to a sibling temporary file and renamed.
func (r Report) WriteJSON(path string) error {
	return r.WriteOutputs(path, "")
}

// WriteOutputs writes the structured report and the human summary to
// separate files, or to the report path alone when summaryPath is empty. The
// human file is rendered from the same Summary, so the two counts cannot
// disagree.
func (r Report) WriteOutputs(path, summaryPath string) error {
	if err := r.writeReport(path); err != nil {
		return err
	}
	if summaryPath == "" || summaryPath == path {
		return nil
	}
	return r.writeSummary(summaryPath)
}

func (r Report) writeReport(path string) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return &PreflightError{Item: path, Err: err}
	}
	raw = append(raw, '\n')
	return writeFileAtomic(path, raw)
}

// writeFileAtomic makes the destination appear complete or not at all: the
// bytes go to a sibling temporary file which is then renamed into place.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return &PreflightError{Item: path, Err: err}
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return &PreflightError{Item: path, Err: err}
	}
	if err := tmp.Close(); err != nil {
		return &PreflightError{Item: path, Err: err}
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return &PreflightError{Item: path, Err: err}
	}
	if err := os.Rename(tmpName, path); err != nil {
		return &PreflightError{Item: path, Err: err}
	}
	return nil
}

func (r Report) writeSummary(path string) error {
	return writeFileAtomic(path, []byte(r.Human()))
}

// Human renders the human-readable summary. Its counts are read from the same
// Summary the structured output uses, so the two cannot disagree.
func (r Report) Human() string {
	var b strings.Builder
	fmt.Fprintf(&b, "records: %d\n", r.Summary.Total)
	fmt.Fprintf(&b, "  conforming: %d\n", r.Summary.Conforming)
	fmt.Fprintf(&b, "  usable:     %d\n", r.Summary.Usable)
	fmt.Fprintf(&b, "  unusable:   %d\n", r.Summary.Unusable)

	fmt.Fprintf(&b, "problems:\n")
	for _, kind := range problemKindOrder {
		fmt.Fprintf(&b, "  %-13s %d\n", kind, r.Summary.Problems[kind])
	}

	if len(r.Records) == 0 {
		return b.String()
	}
	fmt.Fprintf(&b, "details:\n")
	for _, rec := range r.Records {
		if len(rec.Problems) == 0 {
			continue
		}
		fmt.Fprintf(&b, "  record %d: %s\n", rec.Index, rec.Verdict)
		for _, p := range rec.Problems {
			fmt.Fprintf(&b, "    %s: %s (%s) raw=%s\n",
				p.Field, p.Kind, p.Expectation, rawOrNull(p.Raw))
		}
	}
	return b.String()
}

func rawOrNull(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "null"
	}
	return string(raw)
}
