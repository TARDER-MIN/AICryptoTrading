// Package ai wraps the Claude API for trade-signal generation on BingX
// USDS-M perpetual futures. When ANTHROPIC_API_KEY is unset, Client.Enabled()
// is false and callers should surface a "not configured" status rather than
// fail hard - critically, the autotrader treats this as a hard stop: it
// never trades off the bare deterministic rule signal alone, so if AI isn't
// configured, auto-trading is effectively a no-op even though it's "live"
// from launch (see internal/autotrader).
package ai

import (
	"errors"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

var ErrNotConfigured = errors.New("ai: ANTHROPIC_API_KEY is not set")

type Client struct {
	anthropic anthropic.Client
	model     string
	enabled   bool

	// signalSystemPrompt/watchlistSystemPrompt are the system-level
	// instructions for GenerateSignal/SelectDailyWatchlist respectively -
	// sourced from config (AI_SIGNAL_SYSTEM_PROMPT/AI_WATCHLIST_SYSTEM_PROMPT
	// in .env) so they're editable without a rebuild. See internal/config
	// for the built-in defaults these fall back to when unset.
	signalSystemPrompt    string
	watchlistSystemPrompt string
}

func New(apiKey, model, signalSystemPrompt, watchlistSystemPrompt string) *Client {
	if model == "" {
		model = "claude-sonnet-5"
	}
	if apiKey == "" {
		return &Client{model: model, enabled: false, signalSystemPrompt: signalSystemPrompt, watchlistSystemPrompt: watchlistSystemPrompt}
	}
	return &Client{
		anthropic:             anthropic.NewClient(option.WithAPIKey(apiKey)),
		model:                 model,
		enabled:               true,
		signalSystemPrompt:    signalSystemPrompt,
		watchlistSystemPrompt: watchlistSystemPrompt,
	}
}

func (c *Client) Enabled() bool { return c.enabled }
