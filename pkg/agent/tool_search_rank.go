package agent

import (
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// maxToolSearchMatches is how many tools one search_available_tools call
// returns and activates. Every match becomes part of the session's schema,
// so a long query that matched a word in most descriptions used to put
// hundreds of tools in front of the model.
const maxToolSearchMatches = 8

// rankToolMatches orders what a keyword search matched by how telling the
// matched words are, and keeps the best few. A word that appears in more than
// half of the catalog says nothing about which tool is meant, so it counts
// for nothing while the query has a rarer word; a match in the tool's name
// counts twice. corpus is how many tools and skills were searched.
func rankToolMatches(matches []domain.ToolDefinition, keywords []string, corpus, limit int) []domain.ToolDefinition {
	if len(matches) == 0 {
		return matches
	}
	if corpus < len(matches) {
		corpus = len(matches)
	}
	type doc struct{ name, text string }
	docs := make([]doc, len(matches))
	for i, m := range matches {
		n := strings.ToLower(m.Function.Name)
		docs[i] = doc{n, n + " " + strings.ToLower(m.Function.Description)}
	}

	seen := map[string]bool{}
	var terms []string
	for _, kw := range keywords {
		if utf8.RuneCountInString(kw) < 2 || seen[kw] {
			continue
		}
		seen[kw] = true
		terms = append(terms, kw)
	}
	// Every item a keyword occurs in is among the matches, so the count over
	// the matches is the count over the catalog.
	df := make([]int, len(terms))
	anyRare := false
	for j, t := range terms {
		for _, d := range docs {
			if strings.Contains(d.text, t) {
				df[j]++
			}
		}
		if df[j] > 0 && df[j]*2 <= corpus {
			anyRare = true
		}
	}
	weight := make([]float64, len(terms))
	for j := range terms {
		if df[j] == 0 || (anyRare && df[j]*2 > corpus) {
			continue
		}
		weight[j] = math.Log(1 + (float64(corpus)-float64(df[j])+0.5)/(float64(df[j])+0.5))
	}

	type scored struct {
		i     int
		score float64
	}
	var ranked []scored
	for i, d := range docs {
		var s float64
		for j, t := range terms {
			if weight[j] == 0 || !strings.Contains(d.text, t) {
				continue
			}
			s += weight[j]
			if strings.Contains(d.name, t) {
				s += weight[j]
			}
		}
		if s > 0 {
			ranked = append(ranked, scored{i, s})
		}
	}
	sort.SliceStable(ranked, func(a, b int) bool {
		if ranked[a].score != ranked[b].score {
			return ranked[a].score > ranked[b].score
		}
		return docs[ranked[a].i].name < docs[ranked[b].i].name
	})
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]domain.ToolDefinition, len(ranked))
	for k, r := range ranked {
		out[k] = matches[r.i]
	}
	return out
}
