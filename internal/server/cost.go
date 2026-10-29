package server

import (
	"fmt"
	"math"
)

const DefaultMaxCostUSD = 0.50

// SessionCost uses USD. Estimated means a request's usage was unavailable;
// its maximum reserved cost is retained and further requests are blocked.
type SessionCost struct {
	SpentUSD  float64 `yaml:"spent_usd"`
	LimitUSD  float64 `yaml:"limit_usd"`
	Estimated bool    `yaml:"estimated,omitempty"`
}

type costTracker struct{ cost SessionCost }

func newCostTracker(config ProviderConfig) (costTracker, error) {
	c := costTracker{}
	limit := DefaultMaxCostUSD
	if config.MaxCostUSD != nil {
		limit = *config.MaxCostUSD
	}
	err := c.SetCostLimitUSD(limit)
	return c, err
}

func (c *costTracker) Cost() SessionCost { return c.cost }

// SetCostLimitUSD changes the session's total allowance without resetting spend.
// Zero prevents further requests. This is a host API, never an agent builtin.
func (c *costTracker) SetCostLimitUSD(limit float64) error {
	if limit < 0 || math.IsNaN(limit) || math.IsInf(limit, 0) {
		return fmt.Errorf("max_cost_usd must be finite and nonnegative")
	}
	c.cost.LimitUSD = limit
	return nil
}

// GPT-6.1 Sol Standard, 30m cache writes, USD per token. Verified 2026-10-29:
// https://developers.openai.com/api/docs/models/gpt-6.1-sol
// https://developers.openai.com/api/docs/guides/prompt-caching
// Cache writes replace ordinary input charges; reasoning is already in output.
func solRates(inputTokens int) (input, cached, write, output float64) {
	input, cached, write, output = 2e-6, 0.1e-6, 2.5e-6, 10e-6
	if inputTokens > 272000 {
		input, cached, write, output = input*2, cached*2, write*2, output*1.5
	}
	return
}

func usageCost(usage TokenUsage) (float64, error) {
	details := usage.InputTokensDetails
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || details == nil || details.CachedTokens == nil || details.CacheWriteTokens == nil {
		return 0, fmt.Errorf("missing or invalid token usage; cannot account for cost")
	}
	cached, written := *details.CachedTokens, *details.CacheWriteTokens
	if cached < 0 || written < 0 || cached > usage.InputTokens || written > usage.InputTokens-cached {
		return 0, fmt.Errorf("invalid cache token usage")
	}
	i, c, w, o := solRates(usage.InputTokens)
	return float64(usage.InputTokens-cached-written)*i + float64(cached)*c + float64(written)*w + float64(usage.OutputTokens)*o, nil
}

func (c *costTracker) outputAllowance(inputTokens int) (int, float64, error) {
	if inputTokens < 0 {
		return 0, 0, fmt.Errorf("invalid input token count")
	}
	if c.cost.Estimated {
		return 0, 0, fmt.Errorf("session cost is uncertain after a request failure; refusing further requests")
	}
	_, _, write, output := solRates(inputTokens)
	// Reserve every input token at the most expensive possible rate. A cache hit
	// is only known after completion. Limit generated tokens before spending.
	inputCost := float64(inputTokens) * write
	remaining := c.cost.LimitUSD - c.cost.SpentUSD - inputCost
	if remaining < 16*output {
		return 0, 0, fmt.Errorf("session cost limit: $%.6f spent of $%.6f; insufficient budget for the next request", c.cost.SpentUSD, c.cost.LimitUSD)
	}
	tokens := int(math.Min(128000, math.Floor(math.Nextafter(remaining/output, 0))))
	if tokens < 16 {
		return 0, 0, fmt.Errorf("session cost limit: insufficient budget for minimum output")
	}
	return tokens, inputCost + float64(tokens)*output, nil
}
