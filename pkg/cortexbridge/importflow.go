package cortexbridge

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
	"github.com/liliang-cn/cortexdb/v2/pkg/graphflow"
	"github.com/liliang-cn/cortexdb/v2/pkg/importflow"
)

// importflow imports tabular data (CSV / SQL dump) into a CortexDB database's
// RAG + knowledge-graph stores, with an LLM inferring the schema→mapping plan.
// This file is driver-free (importflow's built-in sources are CSV and SQL-dump);
// live-DB connectors with their heavy SQL-driver deps live in the separate
// cortexbridge/connectorbridge sub-package so this package stays lightweight.

// RegisterImportFlow exposes an importflow.Importer's tools (importflow_plan,
// importflow_run) on the agent so the LLM can plan and run data imports into
// RAG+KG. Build the Importer with NewImporter (or importflow.New) first.
//
//	im := cortexbridge.NewImporter(cortex, svc.LLM)
//	cortexbridge.RegisterImportFlow(svc, im)
func RegisterImportFlow(svc *agent.Service, im *importflow.Importer, opts ...Option) ([]string, error) {
	if im == nil {
		return nil, fmt.Errorf("cortexbridge: nil importflow.Importer")
	}
	return RegisterToolbox(svc, importflow.NewToolbox(im), opts...)
}

// NewImporter builds an importflow.Importer over a CortexDB handle, wiring the
// agent's LLM as the schema-mapping inferer (and text refiner) so plan/auto-
// import work out of the box. Extra importflow options are appended.
func NewImporter(db *cortexdb.DB, llm domain.Generator, opts ...importflow.Option) *importflow.Importer {
	base := []importflow.Option{}
	if llm != nil {
		gen := JSONGeneratorFromLLM(llm)
		base = append(base,
			importflow.WithMappingInferer(&importflow.LLMInferer{Client: gen}),
			importflow.WithTextRefiner(&importflow.LLMRefiner{Client: gen}),
		)
	}
	return importflow.New(db, append(base, opts...)...)
}

// JSONGeneratorFromLLM adapts an AgentGo LLM (domain.Generator) to CortexDB's
// graphflow.JSONGenerator (GenerateJSON), the interface importflow's LLM-backed
// inferer/refiner expect.
func JSONGeneratorFromLLM(llm domain.Generator) graphflow.JSONGenerator {
	return llmJSONGenerator{llm: llm}
}

type llmJSONGenerator struct{ llm domain.Generator }

func (g llmJSONGenerator) GenerateJSON(ctx context.Context, systemPrompt, userPrompt string) ([]byte, error) {
	prompt := userPrompt
	if strings.TrimSpace(systemPrompt) != "" {
		prompt = systemPrompt + "\n\n" + userPrompt
	}
	out, err := g.llm.Generate(ctx, prompt, nil)
	if err != nil {
		return nil, err
	}
	return []byte(stripJSONFence(out)), nil
}

// stripJSONFence removes a leading/trailing ```json ... ``` markdown fence that
// chat models often wrap JSON in, so the inferer's json.Unmarshal succeeds.
func stripJSONFence(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return t
	}
	t = strings.TrimPrefix(t, "```")
	if i := strings.IndexByte(t, '\n'); i >= 0 { // drop the ```json language tag line
		t = t[i+1:]
	}
	if j := strings.LastIndex(t, "```"); j >= 0 {
		t = t[:j]
	}
	return strings.TrimSpace(t)
}
