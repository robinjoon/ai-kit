package render

import (
	"fmt"
	"strings"
)

// Resume renders the self-contained semantic portion of a checkpoint. It does
// not depend on a handoff document, runtime snapshot, or parent record.
func Resume(checkpoint map[string]any, gitSummary []string, maxBytes int) ([]byte, error) {
	context, ok := checkpoint["context"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("checkpoint context must be an object")
	}
	taskID, err := requiredString(checkpoint, "task_id")
	if err != nil {
		return nil, err
	}
	checkpointID, err := requiredString(checkpoint, "checkpoint_id")
	if err != nil {
		return nil, err
	}
	title, err := requiredString(context, "title")
	if err != nil {
		return nil, fmt.Errorf("context.%w", err)
	}
	summary, err := requiredString(context, "summary")
	if err != nil {
		return nil, fmt.Errorf("context.%w", err)
	}

	var header strings.Builder
	fmt.Fprintf(&header, "# ctx resume\n\nTask `%s` · checkpoint `%s`\n\n", taskID, checkpointID)
	fmt.Fprintf(&header, "## %s\n", title)
	summarySection := fmt.Sprintf("\n## Summary\n\n%s\n", summary)

	objective, successCriteria := objectiveSections(context["objective"])
	var actions, git strings.Builder
	writeActions(&actions, context["next_actions"])
	git.WriteString("\n## Git comparison\n\n")
	if len(gitSummary) == 0 {
		git.WriteString("- Current Git observation matches the checkpoint baseline.\n")
	} else {
		for _, line := range gitSummary {
			fmt.Fprintf(&git, "- %s\n", line)
		}
	}

	blockers := blockersSection(context["blockers"])
	workStatus, _ := checkpoint["work_status"].(string)
	sections := []resumeSection{
		{body: header.String(), critical: true},
		{body: captureWarning(checkpoint), critical: true},
		{body: objective, critical: true},
		{body: summarySection},
		{body: successCriteria},
		{body: textSection("Constraints", context["constraints"], "text")},
		{body: textSection("Assumptions", context["assumptions"], "text")},
		{body: textSection("Findings", context["findings"], "text")},
		{body: decisionsSection(context["decisions"])},
		{body: progressSection(context["progress"])},
		{body: actions.String(), critical: true},
		{body: blockers, critical: workStatus == "blocked"},
		{body: textSection("Open questions", context["open_questions"], "question")},
		{body: validationsSection(context["validations"])},
		{body: resourcesSection(context["relevant_resources"])},
		{body: git.String(), critical: true},
	}
	return prioritize(sections, maxBytes), nil
}

func objectiveSections(value any) (string, string) {
	objective, ok := value.(map[string]any)
	if !ok {
		return "", ""
	}
	var core, criteria strings.Builder
	if goal, ok := objective["goal"].(string); ok {
		fmt.Fprintf(&core, "\n## Objective\n\n%s\n", goal)
	}
	if items := stringsFromAny(objective["success_criteria"]); len(items) > 0 {
		criteria.WriteString("\n## Success criteria\n")
		for _, criterion := range items {
			fmt.Fprintf(&criteria, "\n- %s\n", criterion)
		}
	}
	return core.String(), criteria.String()
}

func captureWarning(checkpoint map[string]any) string {
	stability, _ := checkpoint["stability"].(string)
	capture, _ := checkpoint["capture"].(map[string]any)
	completeness, _ := capture["completeness"].(string)
	if (stability == "" || stability == "stable") && (completeness == "" || completeness == "complete") {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n> [!WARNING]\n")
	fmt.Fprintf(&b, "> This is a %s checkpoint with %s capture data. Treat missing context as unknown.\n", valueOr(stability, "draft"), valueOr(completeness, "partial"))
	if warnings := stringsFromAny(capture["warnings"]); len(warnings) > 0 {
		b.WriteString("\nCapture warnings:\n")
		for _, warning := range warnings {
			fmt.Fprintf(&b, "- %s\n", warning)
		}
	}
	if omitted := stringsFromAny(capture["omitted_sections"]); len(omitted) > 0 {
		b.WriteString("\nOmitted sections:\n")
		for _, section := range omitted {
			fmt.Fprintf(&b, "- `%s`\n", section)
		}
	}
	return b.String()
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func writeTextItems(b *strings.Builder, heading string, value any, key string) {
	items := objects(value)
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "\n## %s\n", heading)
	for _, item := range items {
		if text, ok := item[key].(string); ok {
			fmt.Fprintf(b, "\n- %s\n", text)
		}
	}
}

func textSection(heading string, value any, key string) string {
	var b strings.Builder
	writeTextItems(&b, heading, value, key)
	return b.String()
}

func decisionsSection(value any) string {
	items := objects(value)
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Decisions\n")
	for _, item := range items {
		statement, _ := item["statement"].(string)
		if statement == "" {
			continue
		}
		status, _ := item["status"].(string)
		fmt.Fprintf(&b, "\n- [%s] %s\n", valueOr(status, "unknown"), statement)
		if rationale, ok := item["rationale"].(string); ok && rationale != "" {
			fmt.Fprintf(&b, "  - Rationale: %s\n", rationale)
		}
	}
	return b.String()
}

func blockersSection(value any) string {
	items := objects(value)
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Blockers\n")
	for _, item := range items {
		description, _ := item["description"].(string)
		impact, _ := item["impact"].(string)
		unblockCondition, _ := item["unblock_condition"].(string)
		if description == "" {
			continue
		}
		fmt.Fprintf(&b, "\n- %s\n", description)
		if impact != "" {
			fmt.Fprintf(&b, "  - Impact: %s\n", impact)
		}
		if unblockCondition != "" {
			fmt.Fprintf(&b, "  - Unblock when: %s\n", unblockCondition)
		}
	}
	return b.String()
}

func writeProgress(b *strings.Builder, value any) {
	progress, ok := value.(map[string]any)
	if !ok {
		return
	}
	items := append(objects(progress["current"]), objects(progress["completed"])...)
	if len(items) == 0 {
		return
	}
	b.WriteString("\n## Progress\n")
	for _, item := range items {
		if text, ok := item["summary"].(string); ok {
			fmt.Fprintf(b, "\n- %s\n", text)
		}
	}
}

func progressSection(value any) string {
	var b strings.Builder
	writeProgress(&b, value)
	return b.String()
}

func writeActions(b *strings.Builder, value any) {
	items := objects(value)
	if len(items) == 0 {
		return
	}
	b.WriteString("\n## Next actions\n")
	for _, item := range items {
		description, ok := item["description"].(string)
		if !ok {
			continue
		}
		if doneWhen, ok := item["done_when"].(string); ok {
			fmt.Fprintf(b, "\n- %s — done when %s\n", description, doneWhen)
		} else {
			fmt.Fprintf(b, "\n- %s\n", description)
		}
	}
}

func resourcesSection(value any) string {
	items := objects(value)
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Relevant resources\n")
	for _, item := range items {
		note, _ := item["note"].(string)
		locator, _ := item["locator"].(map[string]any)
		path, _ := locator["path"].(string)
		if path != "" && note != "" {
			fmt.Fprintf(&b, "\n- `%s`: %s\n", path, note)
		} else if note != "" {
			fmt.Fprintf(&b, "\n- %s\n", note)
		}
	}
	return b.String()
}

func validationsSection(value any) string {
	items := objects(value)
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Validations\n")
	for _, item := range items {
		summary, _ := item["summary"].(string)
		if summary == "" {
			continue
		}
		outcome, _ := item["outcome"].(string)
		fmt.Fprintf(&b, "\n- [%s] %s\n", valueOr(outcome, "unknown"), summary)
		if kind, ok := item["kind"].(string); ok && kind != "" {
			fmt.Fprintf(&b, "  - Kind: %s\n", kind)
		}
		if command, ok := item["command"].(string); ok && command != "" {
			fmt.Fprintf(&b, "  - Command: %s\n", command)
		}
	}
	return b.String()
}

func objects(value any) []map[string]any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			result = append(result, object)
		}
	}
	return result
}

func stringsFromAny(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

type resumeSection struct {
	body     string
	critical bool
}

func prioritize(sections []resumeSection, maxBytes int) []byte {
	if maxBytes <= 0 {
		var result strings.Builder
		for _, section := range sections {
			result.WriteString(section.body)
		}
		return []byte(result.String())
	}
	criticalCount := 0
	for _, section := range sections {
		if section.critical && section.body != "" {
			criticalCount++
		}
	}
	fairShare := maxBytes
	if criticalCount > 0 {
		fairShare = maxBytes / criticalCount
	}
	remaining := maxBytes
	var result strings.Builder
	truncated := false
	for index, section := range sections {
		if section.body == "" {
			continue
		}
		reserved := 0
		for _, future := range sections[index+1:] {
			if future.critical && future.body != "" {
				reserved += min(len(future.body), fairShare)
			}
		}
		allowed := remaining - reserved
		if allowed < 0 {
			allowed = 0
		}
		if !section.critical && len(section.body) > allowed {
			truncated = true
			continue
		}
		piece := truncateUTF8(section.body, allowed)
		if len(piece) < len(section.body) {
			truncated = true
		}
		result.Write(piece)
		remaining -= len(piece)
	}
	if truncated {
		const suffix = "\n[ctx resume output truncated]\n"
		if result.Len()+len(suffix) <= maxBytes {
			result.WriteString(suffix)
		}
	}
	return []byte(result.String())
}

func truncateUTF8(value string, maxBytes int) []byte {
	if len(value) <= maxBytes {
		return []byte(value)
	}
	end := 0
	for i := range value {
		if i > maxBytes {
			break
		}
		end = i
	}
	return []byte(value[:end])
}
