// Package connectorbridge exposes CortexDB's live-database connector as AgentGo
// tools: introspect a running DB's schema, classify PII into a signed masking
// plan, run a desensitized import into RAG + knowledge-graph, and reverse
// pseudonymized tokens via a tenant vault.
//
// It is split out from the parent cortexbridge package because CortexDB's
// connector pulls heavy SQL-driver and CDC dependencies (Postgres pgx/pglogrepl,
// MySQL go-mysql binlog). Import this package ONLY when you need live-DB
// connectors; for CSV / SQL-dump imports use cortexbridge.RegisterImportFlow,
// which is driver-free.
//
// Registered tools (subject to allow/deny/prefix options):
//
//	connector_introspect — read a live DB's schema (no data imported)
//	connector_plan       — introspect + classify PII → UNSIGNED MaskingPlan for review
//	connector_run        — desensitized import into RAG+KG using a signed plan
//	connector_unmask     — reverse pseudonyms via the tenant vault (audited)
//
// Usage:
//
//	cortex, _ := cortexdb.Open(cortexdb.DefaultConfig("cortex.db"))
//	connectorbridge.Register(svc, cortex, connector.ToolboxOptions{Tenant: "acme"})
package connectorbridge

import (
	"fmt"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/cortexbridge"
	"github.com/liliang-cn/cortexdb/v2/pkg/connector"
	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// Register adapts the CortexDB connector toolbox onto the AgentGo service. opts
// configure the desensitization vault / classifier / tenant (zero value is fine:
// the classifier defaults to a rule-based PII classifier; without a Vault the
// connector_unmask tool simply has nothing to reverse). bridgeOpts forward to
// cortexbridge (WithAllow / WithDeny / WithNamePrefix).
func Register(svc *agent.Service, db *cortexdb.DB, opts connector.ToolboxOptions, bridgeOpts ...cortexbridge.Option) ([]string, error) {
	if db == nil {
		return nil, fmt.Errorf("connectorbridge: nil cortexdb handle")
	}
	return cortexbridge.RegisterToolbox(svc, connector.NewToolbox(db, opts), bridgeOpts...)
}
