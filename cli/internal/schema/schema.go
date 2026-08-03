// Package schema validates ctx v1 JSON documents. The embedded schema copies
// make installed ctx binaries independent of their source checkout.
package schema

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/robinjoon/ai-kit/cli/internal/canonical"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	CaptureInput    = "capture_input"
	Checkpoint      = "checkpoint"
	RuntimeSnapshot = "runtime_snapshot"
	Handoff         = "handoff"
)

//go:embed assets/common.schema.json
var commonSchema []byte

//go:embed assets/capture-input.schema.json
var captureInputSchema []byte

//go:embed assets/checkpoint.schema.json
var checkpointSchema []byte

//go:embed assets/runtime-snapshot.schema.json
var runtimeSnapshotSchema []byte

//go:embed assets/handoff.schema.json
var handoffSchema []byte

var schemaSources = map[string][]byte{
	"urn:ctx:schema:v1:common":           commonSchema,
	"urn:ctx:schema:capture-input:v1":    captureInputSchema,
	"urn:ctx:schema:v1:checkpoint":       checkpointSchema,
	"urn:ctx:schema:v1:runtime-snapshot": runtimeSnapshotSchema,
	"urn:ctx:schema:v1:handoff":          handoffSchema,
}

var schemaIDs = map[string]string{
	CaptureInput:    "urn:ctx:schema:capture-input:v1",
	Checkpoint:      "urn:ctx:schema:v1:checkpoint",
	RuntimeSnapshot: "urn:ctx:schema:v1:runtime-snapshot",
	Handoff:         "urn:ctx:schema:v1:handoff",
}

// Validator is safe for concurrent validation after construction.
type Validator struct {
	schemas     map[string]*jsonschema.Schema
	agentTarget *jsonschema.Schema
}

// New compiles the v1 schemas with format assertions enabled.
func New() (*Validator, error) {
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	compiler.UseRegexpEngine(compileECMAScriptRegexp)
	for id, source := range schemaSources {
		var document any
		if err := json.Unmarshal(source, &document); err != nil {
			return nil, fmt.Errorf("decode embedded schema %s: %w", id, err)
		}
		if err := compiler.AddResource(id, document); err != nil {
			return nil, fmt.Errorf("add schema %s: %w", id, err)
		}
	}

	validator := &Validator{schemas: make(map[string]*jsonschema.Schema, len(schemaIDs))}
	for kind, id := range schemaIDs {
		compiled, err := compiler.Compile(id)
		if err != nil {
			return nil, fmt.Errorf("compile %s schema: %w", kind, err)
		}
		validator.schemas[kind] = compiled
	}
	agentTarget, err := compiler.Compile("urn:ctx:schema:v1:common#/$defs/agentTarget")
	if err != nil {
		return nil, fmt.Errorf("compile agentTarget schema: %w", err)
	}
	validator.agentTarget = agentTarget
	return validator, nil
}

type ecmaRegexp regexp2.Regexp

func (r *ecmaRegexp) MatchString(value string) bool {
	matched, err := (*regexp2.Regexp)(r).MatchString(value)
	return err == nil && matched
}

func (r *ecmaRegexp) String() string { return (*regexp2.Regexp)(r).String() }

func compileECMAScriptRegexp(pattern string) (jsonschema.Regexp, error) {
	compiled, err := regexp2.Compile(pattern, regexp2.ECMAScript)
	if err != nil {
		return nil, err
	}
	return (*ecmaRegexp)(compiled), nil
}

// Validate checks the JSON Schema and v1 invariants that require comparing
// fields in the same document. Parent and cross-record invariants belong to
// the application layer, where the referenced records are available.
func (v *Validator) Validate(kind string, record map[string]any) error {
	compiled, ok := v.schemas[kind]
	if !ok {
		return fmt.Errorf("unknown schema kind %q", kind)
	}
	if err := compiled.Validate(record); err != nil {
		return fmt.Errorf("%s schema validation: %w", kind, err)
	}
	switch kind {
	case Checkpoint:
		if err := canonical.VerifyContentDigest(record); err != nil {
			return err
		}
		context, _ := object(record["context"])
		if err := verifyDigest("context_digest", record["context_digest"], context); err != nil {
			return err
		}
		if err := validateSemanticState(record, true); err != nil {
			return err
		}
		return v.validateHandoffTargetExtension(record)
	case RuntimeSnapshot:
		if err := canonical.VerifyContentDigest(record); err != nil {
			return err
		}
		return validateRuntimeState(record)
	case CaptureInput:
		return validateSemanticState(record, false)
	case Handoff:
		checkpointID, _ := record["checkpoint_id"].(string)
		taskID, _ := record["task_id"].(string)
		if got, want := canonical.BytesDigest(canonical.HandoffBody(checkpointID, taskID)), record["rendered_body_digest"]; got != want {
			return fmt.Errorf("rendered_body_digest does not match fixed handoff body")
		}
		return nil
	default:
		return nil
	}
}

func (v *Validator) validateHandoffTargetExtension(record map[string]any) error {
	extensions, _ := object(record["extensions"])
	namespace, exists := object(extensions["io.github.robinjoon.ctx"])
	if !exists {
		return nil
	}
	target, exists := namespace["handoff_target"]
	if !exists {
		return nil
	}
	if record["purpose"] != "handoff" {
		return fmt.Errorf("io.github.robinjoon.ctx.handoff_target is only valid for handoff checkpoints")
	}
	if err := v.agentTarget.Validate(target); err != nil {
		return fmt.Errorf("io.github.robinjoon.ctx.handoff_target validation: %w", err)
	}
	return nil
}

// Decode reads one JSON object and rejects trailing data. Numbers are retained
// as json.Number so the schema validator sees their original JSON form.
func Decode(data []byte) (map[string]any, error) {
	return canonical.DecodeObject(data)
}

func verifyDigest(name string, stored any, value any) error {
	want, ok := stored.(string)
	if !ok {
		return fmt.Errorf("%s is missing", name)
	}
	got, err := canonical.Digest(value)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%s does not match canonical value", name)
	}
	return nil
}

func validateRuntimeState(record map[string]any) error {
	deviceID, _ := record["device_id"].(string)
	producer, _ := object(record["producer"])
	if producerDevice, ok := producer["device_id"].(string); ok && producerDevice != deviceID {
		return fmt.Errorf("producer.device_id must match device_id")
	}
	repository, _ := object(record["repository"])
	git, _ := object(repository["git"])
	worktree, _ := object(git["worktree"])
	if err := validateChangeList(worktree); err != nil {
		return err
	}
	return validateReferenceNamespace(record, nil)
}

func validateSemanticState(record map[string]any, hasWorkspace bool) error {
	context, _ := object(record["context"])
	if err := validateUniqueIDs(context, "decisions", "decision_id"); err != nil {
		return err
	}
	if err := validateUniqueIDs(context, "next_actions", "action_id"); err != nil {
		return err
	}
	if err := validateUniqueIDs(context, "blockers", "blocker_id"); err != nil {
		return err
	}
	if err := validateUniqueIDs(context, "validations", "validation_id"); err != nil {
		return err
	}
	if err := validateActionGraph(context); err != nil {
		return err
	}
	if err := validateDecisionGraph(context); err != nil {
		return err
	}
	var repoIDs map[string]struct{}
	if hasWorkspace {
		workspace, _ := object(record["workspace"])
		repoIDs = make(map[string]struct{})
		for _, repository := range array(workspace["repositories"]) {
			item, _ := object(repository)
			id, _ := item["repo_id"].(string)
			if _, duplicate := repoIDs[id]; duplicate {
				return fmt.Errorf("workspace.repositories contains duplicate repo_id %q", id)
			}
			repoIDs[id] = struct{}{}
		}
		primary, _ := workspace["primary_repo_id"].(string)
		if _, exists := repoIDs[primary]; !exists {
			return fmt.Errorf("workspace.primary_repo_id does not appear in repositories")
		}
	}
	return validateReferenceNamespace(record, repoIDs)
}

func validateChangeList(worktree map[string]any) error {
	changes, ok := object(worktree["changes"])
	if !ok {
		return nil
	}
	entries := array(changes["entries"])
	if total, ok := number(changes["total_entries"]); ok && total != len(entries) {
		return fmt.Errorf("changes.total_entries must equal entries length")
	}
	paths := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		change, _ := object(entry)
		path, _ := change["path"].(string)
		if _, duplicate := paths[path]; duplicate {
			return fmt.Errorf("changes.entries contains duplicate path %q", path)
		}
		paths[path] = struct{}{}
	}
	return nil
}

func validateReferenceNamespace(record map[string]any, repoIDs map[string]struct{}) error {
	known := make(map[string]referenceSource)
	for _, sessionValue := range array(record["session_refs"]) {
		session, _ := object(sessionValue)
		if err := addReferenceSource(known, session["session_ref_id"], "session", false, nil); err != nil {
			return err
		}
		for _, logValue := range array(session["logs"]) {
			log, _ := object(logValue)
			if err := addReferenceSource(known, log["log_ref_id"], "log", true, log["selector"]); err != nil {
				return err
			}
			if err := validateLocatorRepo(log["locator"], repoIDs); err != nil {
				return err
			}
		}
	}
	context, _ := object(record["context"])
	resources := make(map[string]struct{})
	for _, resourceValue := range array(context["relevant_resources"]) {
		resource, _ := object(resourceValue)
		id, _ := resource["resource_id"].(string)
		if err := addReferenceSource(known, id, "resource", true, resource["selection"]); err != nil {
			return err
		}
		resources[id] = struct{}{}
		if err := validateLocatorRepo(resource["locator"], repoIDs); err != nil {
			return err
		}
	}
	if err := validateEvidenceRefs(record, known); err != nil {
		return err
	}
	for _, item := range append(array(progress(context, "completed")), array(progress(context, "current"))...) {
		if err := validateResourceRefs(item, resources); err != nil {
			return err
		}
	}
	for _, action := range array(context["next_actions"]) {
		if err := validateResourceRefs(action, resources); err != nil {
			return err
		}
	}
	for _, validation := range array(context["validations"]) {
		item, _ := object(validation)
		if reference, ok := item["output_resource_ref"].(string); ok {
			if _, exists := resources[reference]; !exists {
				return fmt.Errorf("validation output_resource_ref %q is unknown", reference)
			}
		}
		cwd, _ := object(item["cwd"])
		if repoID, ok := cwd["repo_id"].(string); ok && repoIDs != nil {
			if _, exists := repoIDs[repoID]; !exists {
				return fmt.Errorf("validation cwd repo_id %q is unknown", repoID)
			}
		}
	}
	return nil
}

func progress(context map[string]any, key string) any {
	progress, _ := object(context["progress"])
	return progress[key]
}

func validateLocatorRepo(value any, repoIDs map[string]struct{}) error {
	if repoIDs == nil {
		return nil
	}
	locator, _ := object(value)
	if repoID, ok := locator["repo_id"].(string); ok {
		if _, exists := repoIDs[repoID]; !exists {
			return fmt.Errorf("locator repo_id %q is unknown", repoID)
		}
	}
	return nil
}

type referenceSource struct {
	kind            string
	selectorCapable bool
	selector        map[string]any
}

func addReferenceSource(known map[string]referenceSource, value any, kind string, selectorCapable bool, selectorValue any) error {
	id, _ := value.(string)
	if _, exists := known[id]; exists {
		return fmt.Errorf("duplicate %s reference ID %q", kind, id)
	}
	selector, _ := object(selectorValue)
	if selector != nil {
		if err := validateSelector(selector); err != nil {
			return fmt.Errorf("%s reference %q selector: %w", kind, id, err)
		}
	}
	known[id] = referenceSource{kind: kind, selectorCapable: selectorCapable, selector: selector}
	return nil
}

func validateEvidenceRefs(record map[string]any, known map[string]referenceSource) error {
	var walk func(any) error
	walk = func(value any) error {
		switch item := value.(type) {
		case map[string]any:
			if refs, exists := item["evidence_refs"]; exists {
				for _, refValue := range array(refs) {
					ref, _ := object(refValue)
					id, _ := ref["ref_id"].(string)
					source, exists := known[id]
					if !exists {
						return fmt.Errorf("evidence reference %q is unknown", id)
					}
					selector, _ := object(ref["selector"])
					if selector == nil {
						continue
					}
					if !source.selectorCapable {
						return fmt.Errorf("evidence reference %q cannot select part of a %s reference", id, source.kind)
					}
					if err := validateSelector(selector); err != nil {
						return fmt.Errorf("evidence reference %q selector: %w", id, err)
					}
					if source.selector != nil {
						if err := validateSelectorSubset(source.selector, selector); err != nil {
							return fmt.Errorf("evidence reference %q selector: %w", id, err)
						}
					}
				}
			}
			for key, child := range item {
				if key == "extensions" {
					continue
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range item {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(record["context"])
}

func validateSelector(selector map[string]any) error {
	kind, _ := selector["kind"].(string)
	switch kind {
	case "line_range":
		return validateIntegerRange(selector, "start_line", "end_line")
	case "byte_range":
		return validateIntegerRange(selector, "start_byte", "end_byte")
	case "time_range":
		start, err := parseExactTimestamp(selector["start_at"].(string))
		if err != nil {
			return err
		}
		end, err := parseExactTimestamp(selector["end_at"].(string))
		if err != nil {
			return err
		}
		if end.compare(start) < 0 {
			return fmt.Errorf("end_at must not be before start_at")
		}
	}
	return nil
}

func validateIntegerRange(selector map[string]any, startKey, endKey string) error {
	start, _ := number(selector[startKey])
	end, _ := number(selector[endKey])
	if end < start {
		return fmt.Errorf("%s must not be less than %s", endKey, startKey)
	}
	return nil
}

func validateSelectorSubset(source, evidence map[string]any) error {
	sourceKind, _ := source["kind"].(string)
	evidenceKind, _ := evidence["kind"].(string)
	if evidenceKind != sourceKind {
		return fmt.Errorf("kind %q does not match source kind %q", evidenceKind, sourceKind)
	}
	switch sourceKind {
	case "line_range":
		return validateIntegerSubset(source, evidence, "start_line", "end_line")
	case "byte_range":
		return validateIntegerSubset(source, evidence, "start_byte", "end_byte")
	case "time_range":
		sourceStart, _ := parseExactTimestamp(source["start_at"].(string))
		sourceEnd, _ := parseExactTimestamp(source["end_at"].(string))
		evidenceStart, _ := parseExactTimestamp(evidence["start_at"].(string))
		evidenceEnd, _ := parseExactTimestamp(evidence["end_at"].(string))
		if evidenceStart.compare(sourceStart) < 0 || evidenceEnd.compare(sourceEnd) > 0 {
			return fmt.Errorf("time range is outside source range")
		}
	case "message_ids":
		available := make(map[string]struct{})
		for _, value := range array(source["message_ids"]) {
			available[value.(string)] = struct{}{}
		}
		for _, value := range array(evidence["message_ids"]) {
			id := value.(string)
			if _, exists := available[id]; !exists {
				return fmt.Errorf("message ID %q is outside source selection", id)
			}
		}
	case "opaque":
		if evidence["value"] != source["value"] {
			return fmt.Errorf("opaque selector does not match source selector")
		}
	}
	return nil
}

type exactTimestamp struct {
	seconds  int64
	fraction string
}

func parseExactTimestamp(value string) (exactTimestamp, error) {
	zoneStart := len(value) - 1
	if value[zoneStart] != 'Z' {
		zoneStart = len(value) - len("+00:00")
	}
	base := value[:len("2006-01-02T15:04:05")] + value[zoneStart:]
	parsed, err := time.Parse(time.RFC3339, base)
	if err != nil {
		return exactTimestamp{}, err
	}
	fraction := ""
	if zoneStart > len("2006-01-02T15:04:05") {
		fraction = strings.TrimRight(value[len("2006-01-02T15:04:05."):zoneStart], "0")
	}
	return exactTimestamp{seconds: parsed.Unix(), fraction: fraction}, nil
}

func (timestamp exactTimestamp) compare(other exactTimestamp) int {
	if timestamp.seconds < other.seconds {
		return -1
	}
	if timestamp.seconds > other.seconds {
		return 1
	}
	length := max(len(timestamp.fraction), len(other.fraction))
	for index := 0; index < length; index++ {
		left, right := byte('0'), byte('0')
		if index < len(timestamp.fraction) {
			left = timestamp.fraction[index]
		}
		if index < len(other.fraction) {
			right = other.fraction[index]
		}
		if left < right {
			return -1
		}
		if left > right {
			return 1
		}
	}
	return 0
}

func validateIntegerSubset(source, evidence map[string]any, startKey, endKey string) error {
	sourceStart, _ := number(source[startKey])
	sourceEnd, _ := number(source[endKey])
	evidenceStart, _ := number(evidence[startKey])
	evidenceEnd, _ := number(evidence[endKey])
	if evidenceStart < sourceStart || evidenceEnd > sourceEnd {
		return fmt.Errorf("range is outside source range")
	}
	return nil
}

func validateResourceRefs(value any, resources map[string]struct{}) error {
	item, _ := object(value)
	for _, refValue := range array(item["resource_refs"]) {
		ref, _ := refValue.(string)
		if _, exists := resources[ref]; !exists {
			return fmt.Errorf("resource reference %q is unknown", ref)
		}
	}
	return nil
}

func validateUniqueIDs(context map[string]any, collection, key string) error {
	seen := make(map[string]struct{})
	for _, value := range array(context[collection]) {
		item, _ := object(value)
		if err := addID(seen, item[key], collection); err != nil {
			return err
		}
	}
	return nil
}

func addID(seen map[string]struct{}, value any, label string) error {
	id, _ := value.(string)
	if _, exists := seen[id]; exists {
		return fmt.Errorf("duplicate %s ID %q", label, id)
	}
	seen[id] = struct{}{}
	return nil
}

func validateActionGraph(context map[string]any) error {
	dependencies := make(map[string][]string)
	for _, value := range array(context["next_actions"]) {
		item, _ := object(value)
		id, _ := item["action_id"].(string)
		dependencies[id] = nil
		for _, dependency := range array(item["dependencies"]) {
			dependencies[id] = append(dependencies[id], dependency.(string))
		}
	}
	for id, refs := range dependencies {
		for _, ref := range refs {
			if _, exists := dependencies[ref]; !exists {
				return fmt.Errorf("next action dependency %q is unknown", ref)
			}
		}
		if err := visitGraph(id, dependencies, map[string]bool{}, map[string]bool{}); err != nil {
			return fmt.Errorf("next action dependencies: %w", err)
		}
	}
	return nil
}

func validateDecisionGraph(context map[string]any) error {
	supersedes := make(map[string][]string)
	for _, value := range array(context["decisions"]) {
		item, _ := object(value)
		id, _ := item["decision_id"].(string)
		supersedes[id] = nil
		for _, prior := range array(item["supersedes"]) {
			supersedes[id] = append(supersedes[id], prior.(string))
		}
	}
	for id, refs := range supersedes {
		for _, ref := range refs {
			if _, exists := supersedes[ref]; !exists {
				return fmt.Errorf("decision supersedes %q is unknown", ref)
			}
		}
		if err := visitGraph(id, supersedes, map[string]bool{}, map[string]bool{}); err != nil {
			return fmt.Errorf("decision supersedes: %w", err)
		}
	}
	return nil
}

func visitGraph(id string, graph map[string][]string, visiting, visited map[string]bool) error {
	if visiting[id] {
		return fmt.Errorf("cycle at %q", id)
	}
	if visited[id] {
		return nil
	}
	visiting[id] = true
	for _, next := range graph[id] {
		if err := visitGraph(next, graph, visiting, visited); err != nil {
			return err
		}
	}
	delete(visiting, id)
	visited[id] = true
	return nil
}

func object(value any) (map[string]any, bool) {
	item, ok := value.(map[string]any)
	return item, ok
}

func array(value any) []any {
	items, _ := value.([]any)
	return items
}

func number(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case int64:
		return int(number), true
	case json.Number:
		integer, err := number.Int64()
		return int(integer), err == nil
	case float64:
		return int(number), number == float64(int(number))
	default:
		return 0, false
	}
}

var (
	defaultOnce      sync.Once
	defaultValidator *Validator
	defaultErr       error
)

// Validate uses the process-wide compiled v1 schemas.
func Validate(kind string, record map[string]any) error {
	defaultOnce.Do(func() { defaultValidator, defaultErr = New() })
	if defaultErr != nil {
		return defaultErr
	}
	return defaultValidator.Validate(kind, record)
}

// Asset returns a defensive copy of an embedded schema. It is primarily useful
// to test that generated assets still match the repository specification.
func Asset(name string) ([]byte, bool) {
	for id, source := range schemaSources {
		if strings.HasSuffix(id, name) {
			return append([]byte(nil), source...), true
		}
	}
	return nil, false
}
