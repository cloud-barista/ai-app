package catalog

import (
	"context"
	"sort"
	"strings"
	"unicode"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// Match is one candidate application for a natural language query.
type Match struct {
	AppID string `json:"appId"`
	Name  string `json:"name"`
	// Score is comparable only within one Resolve call. It ranks candidates; it
	// is not a probability and must not be compared across queries.
	Score int `json:"score"`
	// Reasons names the signals that matched, so an operator can see why a
	// candidate came up rather than being handed an unexplained ranking.
	Reasons []string `json:"reasons"`
	Runtime string   `json:"runtime"`
	ModelID string   `json:"modelId"`
}

// Signal weights. They are ordered by how specific the signal is: an exact
// identifier can only mean one application, a description word can mean any.
const (
	scoreExactID     = 100
	scoreIDToken     = 40
	scoreAlias       = 45
	scoreModelToken  = 25
	scoreNameToken   = 12
	scoreRuntime     = 18
	scoreSizeToken   = 20
	scoreFormatToken = 15
	scoreDescToken   = 4
)

// stopTokens are words that appear in almost every deployment instruction and
// therefore separate nothing. Scoring them rewards the longest description
// instead of the right application.
var stopTokens = map[string]bool{
	"배포": true, "배포해줘": true, "올려": true, "올려줘": true, "띄워": true, "띄워줘": true,
	"해줘": true, "하나": true, "노드": true, "모델": true, "서버": true, "추론": true,
	"deploy": true, "please": true, "the": true, "for": true, "with": true, "on": true,
	"a": true, "an": true, "to": true, "me": true, "run": true, "start": true,
}

// Resolve ranks registered applications against a natural language query.
//
// It exists so that picking an application is not left entirely to the planning
// model reading a catalog dump. A deterministic ranking can be measured and
// regression tested, and the model gets a short candidate list instead of every
// field of every application. The model still decides; this narrows the field.
func (s *Store) Resolve(ctx context.Context, query string, limit int) []Match {
	if limit <= 0 {
		limit = 5
	}
	queryTokens := tokenSet(query)
	if len(queryTokens) == 0 {
		return []Match{}
	}
	lowered := strings.ToLower(query)

	matches := make([]Match, 0, 4)
	for _, spec := range s.List(ctx) {
		if m, structured := scoreSpec(&spec, lowered, queryTokens); structured {
			matches = append(matches, m)
		}
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].AppID < matches[j].AppID
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

// scoreSpec adds up every signal one application matches and reports whether any
// of them came from a structured field.
//
// Only a structured field makes an application a candidate. Description text is
// scored because it breaks ties between two candidates, but a description word on
// its own means the operator used a common word, not that they asked for this
// application, and answering "no candidate" is more useful than a confident wrong
// one.
func scoreSpec(spec *model.AppSpec, lowered string, queryTokens map[string]bool) (Match, bool) {
	m := Match{
		AppID:   spec.ID,
		Name:    spec.Name,
		Runtime: string(spec.Runtime),
		ModelID: spec.Model.ID,
		Reasons: []string{},
	}
	structured := false
	add := func(points int, reason string) {
		m.Score += points
		m.Reasons = append(m.Reasons, reason)
		structured = true
	}

	if strings.Contains(lowered, strings.ToLower(spec.ID)) {
		add(scoreExactID, "id")
		return m, true
	}

	for _, token := range tokenList(spec.ID) {
		if queryTokens[token] && !stopTokens[token] {
			add(scoreIDToken, "id:"+token)
		}
	}
	for _, alias := range spec.Aliases {
		if matchesAlias(alias, lowered, queryTokens) {
			add(scoreAlias, "alias:"+alias)
		}
	}
	for _, token := range tokenList(spec.Model.ID) {
		if !queryTokens[token] || stopTokens[token] {
			continue
		}
		if isSizeToken(token) {
			add(scoreSizeToken, "size:"+token)
			continue
		}
		if isVersionFragment(token) {
			// "3.1" splits into "3" and "1". Those match almost anything and
			// would outweigh the family name they came from.
			continue
		}
		add(scoreModelToken, "model:"+token)
	}
	if queryTokens[strings.ToLower(string(spec.Runtime))] {
		add(scoreRuntime, "runtime:"+string(spec.Runtime))
	}
	if spec.Model.Format != "" && queryTokens[strings.ToLower(spec.Model.Format)] {
		add(scoreFormatToken, "format:"+spec.Model.Format)
	}
	for _, token := range tokenList(spec.Name) {
		if queryTokens[token] && !stopTokens[token] {
			add(scoreNameToken, "name:"+token)
		}
	}
	for _, token := range tokenList(spec.Description) {
		if queryTokens[token] && !stopTokens[token] && len(token) > 2 {
			// Deliberately not through add: a description word never makes a
			// candidate on its own.
			m.Score += scoreDescToken
			m.Reasons = append(m.Reasons, "desc:"+token)
		}
	}
	return m, structured
}

// matchesAlias accepts an alias as a whole token, or as a substring that starts
// on a word boundary.
//
// Substring matching is what carries Korean phrasing: an operator writes
// "라마모델" as one word, so a token comparison alone would miss it. Requiring the
// start of a word is what keeps it from matching the middle of another name:
// "ollama" contains "llama", and without the boundary every Ollama instruction
// also proposed the Llama application.
func matchesAlias(alias, lowered string, queryTokens map[string]bool) bool {
	a := strings.ToLower(strings.TrimSpace(alias))
	if a == "" {
		return false
	}
	if queryTokens[a] {
		return true
	}
	for offset := 0; ; {
		i := strings.Index(lowered[offset:], a)
		if i < 0 {
			return false
		}
		at := offset + i
		if at == 0 || !isWordRune(rune(lowered[at-1])) {
			return true
		}
		offset = at + 1
	}
}

// isWordRune reports a byte that continues a word rather than ending one.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// isSizeToken reports a parameter count such as "8b" or "70b", which separates
// two builds of the same model family better than the family name does.
func isSizeToken(token string) bool {
	if len(token) < 2 || !strings.HasSuffix(token, "b") {
		return false
	}
	for _, r := range token[:len(token)-1] {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// isVersionFragment reports a short all-digit token, which is what a version
// number leaves behind when it is split on the dot.
func isVersionFragment(token string) bool {
	if len(token) > 2 {
		return false
	}
	for _, r := range token {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// tokenList splits text on anything that is not a letter or a digit.
//
// Hangul syllables are letters, so a Korean word survives as one token while
// "meta-llama/Llama-3.1-8B" breaks into the pieces an operator actually types.
func tokenList(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func tokenSet(text string) map[string]bool {
	set := make(map[string]bool)
	for _, token := range tokenList(text) {
		set[token] = true
	}
	return set
}
