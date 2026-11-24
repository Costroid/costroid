package recipelint

import (
	"cmp"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// workflowRunEvent is one workflow_run event GitHub sends the gateway recipe
// about a run of the agent's workflow: its action and the attempt it names.
type workflowRunEvent struct {
	action  string
	attempt int
}

// gatewayContext is the expression context of the gateway recipe's run that
// the event starts, for a pull request's run on a branch of the repository.
func gatewayContext(event workflowRunEvent) map[string]any {
	return map[string]any{"github": map[string]any{
		"repository": "acme/widgets",
		"event": map[string]any{
			"action": event.action,
			"workflow_run": map[string]any{
				"id": int64(45377260976), "run_attempt": int64(event.attempt), "event": "pull_request",
				"head_repository": map[string]any{"full_name": "acme/widgets"},
			},
		},
	}}
}

// TestTheGatewayRecipeMintsOncePerAttempt evaluates the gateway recipe's mint
// and close conditions on the events GitHub sends for a run's attempts.
// Recorded GitHub Actions runs read them: a first attempt is followed by
// requested, in_progress and completed, and a re-run by in_progress and
// completed, with no requested. Each attempt gets one mint and one close,
// and a requested event for a later attempt, which GitHub does not send,
// would mint nothing more. The recipe listens for every action the
// conditions read, and gives each run and attempt its own concurrency group.
func TestTheGatewayRecipeMintsOncePerAttempt(t *testing.T) {
	documents, err := parse([]byte(read(t, filepath.Join(recipesDir, gatewayJobsRecipe))))
	if err != nil {
		t.Fatal(err)
	}
	root := documents[0]
	jobs := field(root, "jobs")
	mint, closing := scalarField(field(jobs, "mint"), "if"), scalarField(field(jobs, "close"), "if")
	types := sequence(field(field(field(root, "on"), "workflow_run"), "types"))
	var listened []string
	for _, node := range types {
		listened = append(listened, node.Value)
	}
	for _, action := range []string{"requested", "in_progress", "completed"} {
		if !slices.Contains(listened, action) {
			t.Errorf("the recipe does not listen for %s: %v", action, listened)
		}
	}
	group := scalarField(field(root, "concurrency"), "group")
	if !strings.Contains(group, "github.event.workflow_run.id") || !strings.Contains(group, "github.event.workflow_run.run_attempt") {
		t.Errorf("the concurrency group %q does not name the run and its attempt", group)
	}

	for _, run := range []struct {
		name   string
		events []workflowRunEvent
	}{
		{"a first attempt", []workflowRunEvent{{"requested", 1}, {"in_progress", 1}, {"completed", 1}}},
		{"a second attempt", []workflowRunEvent{{"in_progress", 2}, {"completed", 2}}},
		{"a third attempt", []workflowRunEvent{{"in_progress", 3}, {"completed", 3}}},
		{"a later attempt GitHub also sent requested for", []workflowRunEvent{{"requested", 2}, {"in_progress", 2}, {"completed", 2}}},
	} {
		t.Run(run.name, func(t *testing.T) {
			var minted, closed []string
			for _, event := range run.events {
				context := gatewayContext(event)
				if truthy(evaluate(t, mint, context)) {
					minted = append(minted, event.action)
				}
				if truthy(evaluate(t, closing, context)) {
					closed = append(closed, event.action)
				}
			}
			if len(minted) != 1 || len(closed) != 1 || closed[0] != "completed" {
				t.Errorf("mint ran on %v and close on %v, want one mint and one close on completed", minted, closed)
			}
		})
	}

	fork := gatewayContext(workflowRunEvent{"in_progress", 2})
	fork["github"].(map[string]any)["event"].(map[string]any)["workflow_run"].(map[string]any)["head_repository"] = map[string]any{"full_name": "fork/widgets"}
	if truthy(evaluate(t, mint, fork)) {
		t.Error("a fork's re-run gets a key")
	}
}

// evaluate reads a GitHub Actions expression and evaluates it in the context
// given. It takes what the gateway recipe's conditions hold: property paths,
// string and whole-number literals, parentheses, !, the comparisons, && and
// ||, with && binding closer than ||. Two strings compare ignoring case, and
// any other pair as whole numbers.
func evaluate(t *testing.T, expression string, context map[string]any) any {
	t.Helper()
	tokens, err := expressionTokens(expression)
	if err != nil {
		t.Fatalf("%v in %q", err, expression)
	}
	p := &expressionParser{tokens: tokens, context: context}
	value := p.or()
	if p.err == nil && p.at < len(p.tokens) {
		p.err = fmt.Errorf("%q after the expression", p.tokens[p.at])
	}
	if p.err != nil {
		t.Fatalf("%v in %q", p.err, expression)
	}
	return value
}

var expressionOperators = []string{"&&", "||", "==", "!=", ">=", "<=", ">", "<", "!", "(", ")"}

func expressionTokens(expression string) ([]string, error) {
	var tokens []string
	for rest := strings.TrimSpace(expression); rest != ""; rest = strings.TrimLeftFunc(rest, unicode.IsSpace) {
		if operator := operatorAt(rest); operator != "" {
			tokens, rest = append(tokens, operator), rest[len(operator):]
			continue
		}
		if rest[0] == '\'' {
			end := closingQuote(rest)
			if end < 0 {
				return nil, errors.New("an unclosed string")
			}
			tokens, rest = append(tokens, rest[:end+1]), rest[end+1:]
			continue
		}
		end := strings.IndexFunc(rest, func(r rune) bool {
			return unicode.IsSpace(r) || operatorAt(string(r)) != "" || r == '=' || r == '&' || r == '|'
		})
		if end == 0 {
			return nil, fmt.Errorf("an unknown character %q", rest[0])
		}
		if end < 0 {
			end = len(rest)
		}
		tokens, rest = append(tokens, rest[:end]), rest[end:]
	}
	return tokens, nil
}

// closingQuote is the index of the quote that closes the string literal text
// opens, where two quotes stand for one, or -1.
func closingQuote(text string) int {
	for i := 1; i < len(text); i++ {
		switch {
		case text[i] != '\'':
		case strings.HasPrefix(text[i:], "''"):
			i++
		default:
			return i
		}
	}
	return -1
}

func operatorAt(text string) string {
	for _, operator := range expressionOperators {
		if strings.HasPrefix(text, operator) {
			return operator
		}
	}
	return ""
}

type expressionParser struct {
	tokens  []string
	at      int
	context map[string]any
	err     error
}

func (p *expressionParser) next() string {
	if p.at < len(p.tokens) {
		return p.tokens[p.at]
	}
	return ""
}

func (p *expressionParser) or() any {
	value := p.and()
	for p.next() == "||" {
		p.at++
		right := p.and()
		if !truthy(value) {
			value = right
		}
	}
	return value
}

func (p *expressionParser) and() any {
	value := p.comparison()
	for p.next() == "&&" {
		p.at++
		right := p.comparison()
		if truthy(value) {
			value = right
		}
	}
	return value
}

func (p *expressionParser) comparison() any {
	left := p.unary()
	operator := p.next()
	if !slices.Contains([]string{"==", "!=", ">", ">=", "<", "<="}, operator) {
		return left
	}
	p.at++
	return compare(left, operator, p.unary())
}

func (p *expressionParser) unary() any {
	switch token := p.next(); {
	case token == "!":
		p.at++
		return !truthy(p.unary())
	case token == "(":
		p.at++
		value := p.or()
		if p.next() != ")" {
			p.err = errors.New("an unclosed parenthesis")
		}
		p.at++
		return value
	case token == "":
		p.err = errors.New("an expression that ends early")
		return nil
	default:
		p.at++
		return p.literal(token)
	}
}

func (p *expressionParser) literal(token string) any {
	switch {
	case strings.HasPrefix(token, "'"):
		return strings.ReplaceAll(token[1:len(token)-1], "''", "'")
	case token == "true", token == "false":
		return token == "true"
	case token == "null":
		return nil
	}
	if number, err := strconv.ParseInt(token, 10, 64); err == nil {
		return number
	}
	var value any = p.context
	for _, name := range strings.Split(token, ".") {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[name]
	}
	return value
}

func truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case int64:
		return v != 0
	case string:
		return v != ""
	}
	return true
}

func compare(left any, operator string, right any) bool {
	a, aString := left.(string)
	b, bString := right.(string)
	var order int
	if aString && bString {
		order = strings.Compare(strings.ToLower(a), strings.ToLower(b))
	} else {
		x, xNumber := number(left)
		y, yNumber := number(right)
		if !xNumber || !yNumber {
			return operator == "!="
		}
		order = cmp.Compare(x, y)
	}
	switch operator {
	case "==":
		return order == 0
	case "!=":
		return order != 0
	case ">":
		return order > 0
	case ">=":
		return order >= 0
	case "<":
		return order < 0
	}
	return order <= 0
}

// number is a value as GitHub's expressions coerce it to a number, and false
// when it is not one. The recipe's conditions compare whole numbers alone.
func number(value any) (int64, bool) {
	switch v := value.(type) {
	case nil:
		return 0, true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	case int64:
		return v, true
	case string:
		if strings.TrimSpace(v) == "" {
			return 0, true
		}
		parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return parsed, err == nil
	}
	return 0, false
}
