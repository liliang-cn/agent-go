package agent

import (
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

// priceModel gives a model rates for the length of one test. Nothing is priced
// until someone registers it, so a test that needs a cost has to say what it is.
func priceModel(t *testing.T, model string, in, out float64) {
	t.Helper()
	pool.RegisterModelPricing(model, pool.ModelPricing{InputPer1K: in, OutputPer1K: out})
	t.Cleanup(func() { pool.UnregisterModelPricing(model) })
}
