// Package decision is a client for decision engines: small models that answer
// typed questions about a text in one forward pass, instead of generating an
// answer token by token.
//
// It exists because several places in this framework already pay for a full
// model call to make a small closed decision — does this request forbid tools,
// is this worth remembering, is this tool call risky. Those calls are
// structured, temperature 0, and on the critical path. A decision engine
// answers the same shape of question in milliseconds.
//
// Nothing here generates text. An engine can answer "which of these" and "how
// much" and "yes or no"; it cannot quote the user, name a tool or write a
// description. A caller that needs any of those still needs a language model,
// and the honest use of this package is to decide whether that call is needed
// at all.
package decision

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Type is the kind of question being asked.
type Type string

const (
	// TypeChoice picks one of Criteria.
	TypeChoice Type = "choice"
	// TypeScore rates against ordered Criteria, lowest first.
	TypeScore Type = "score"
	// TypeNoul answers yes or no.
	TypeNoul Type = "noul"
)

// Question is one thing to decide about a text.
type Question struct {
	Type Type
	// Instructions is the question in plain words, addressed to the engine.
	Instructions string
	// Criteria are the options for choice, or the ordered levels for score.
	// Unused by noul.
	Criteria []string
	// Descriptions say what each choice option means, keyed by option, or
	// what "yes" and "no" mean for a noul. Optional, and often the difference
	// between a usable answer and a coin toss: a routing question whose two
	// outcomes were each described in one line moved its worst negative from
	// 0.54 to 0.20. Score takes none — its levels are descriptions already.
	// An engine that only takes labels folds them into the instructions.
	Descriptions map[string]string
}

// Describe returns q with descriptions attached. See Question.Descriptions.
func (q Question) Describe(descriptions map[string]string) Question {
	q.Descriptions = descriptions
	return q
}

// Choice asks which of options fits.
func Choice(instructions string, options ...string) Question {
	return Question{Type: TypeChoice, Instructions: instructions, Criteria: options}
}

// Score asks where the text sits on an ordered scale, lowest level first.
func Score(instructions string, levels ...string) Question {
	return Question{Type: TypeScore, Instructions: instructions, Criteria: levels}
}

// Noul asks a yes/no question.
func Noul(instructions string) Question {
	return Question{Type: TypeNoul, Instructions: instructions}
}

// Validate reports whether the question is answerable as written.
func (q Question) Validate() error {
	if strings.TrimSpace(q.Instructions) == "" {
		return fmt.Errorf("decision: question has no instructions")
	}
	switch q.Type {
	case TypeNoul:
		for key := range q.Descriptions {
			if key != "yes" && key != "no" {
				return fmt.Errorf("decision: a noul describes only yes and no, not %q", key)
			}
		}
		return nil
	case TypeChoice, TypeScore:
		if len(q.Criteria) < 2 {
			return fmt.Errorf("decision: %s needs at least two criteria", q.Type)
		}
		if q.Type == TypeScore && len(q.Descriptions) > 0 {
			return fmt.Errorf("decision: score levels are their own descriptions")
		}
		for key := range q.Descriptions {
			if !slices.Contains(q.Criteria, key) {
				return fmt.Errorf("decision: description for %q, which is not an option", key)
			}
		}
		return nil
	default:
		return fmt.Errorf("decision: unknown question type %q", q.Type)
	}
}

// describedInstructions is the instructions with the descriptions appended,
// in criteria order, for an engine that cannot take them separately.
func (q Question) describedInstructions() string {
	if len(q.Descriptions) == 0 {
		return q.Instructions
	}
	keys := q.Criteria
	if q.Type == TypeNoul {
		keys = []string{"yes", "no"}
	}
	var parts []string
	for _, k := range keys {
		if d := strings.TrimSpace(q.Descriptions[k]); d != "" {
			parts = append(parts, k+": "+d)
		}
	}
	if len(parts) == 0 {
		return q.Instructions
	}
	return q.Instructions + " (" + strings.Join(parts, "; ") + ")"
}

// Answer is one decision.
//
// Confidence is the engine's own calibrated number, not the winning
// probability — the two differ, and the engine's is the conservative one. A
// caller gating on it writes one comparison whatever the question type was.
type Answer struct {
	Type Type
	// Label is the chosen option for choice, the chosen level for score, and
	// "yes" or "no" for noul. It is the one field a caller can read without
	// knowing which kind of question it asked.
	Label string
	// Yes is the verdict of a noul question, false for every other type.
	Yes bool
	// Confidence is how sure the engine is of Label, in [0,1].
	Confidence float64
	// Score is the position on a score question's scale, between 0 and
	// len(Criteria)-1 and not rounded to a level.
	Score float64
	// Probabilities is the full distribution, keyed by option for choice and
	// by level index for score. Nil for noul.
	Probabilities map[string]float64
}

// Engine answers typed questions about a text.
//
// Decide answers every question in one round trip; engines are built for that
// and asking them one at a time gives up most of the speed. An implementation
// must be safe for concurrent use.
type Engine interface {
	// Name identifies the engine in logs and observer callbacks.
	Name() string
	// Decide returns one answer per question, keyed as the questions were. An
	// error means no answer at all: callers degrade, they do not guess.
	Decide(ctx context.Context, text string, questions map[string]Question) (map[string]Answer, error)
}

// ValidateQuestions checks a whole set before it is sent.
func ValidateQuestions(questions map[string]Question) error {
	if len(questions) == 0 {
		return fmt.Errorf("decision: no questions")
	}
	names := make([]string, 0, len(questions))
	for name := range questions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("decision: question with an empty name")
		}
		if err := questions[name].Validate(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}
