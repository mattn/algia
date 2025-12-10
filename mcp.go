package main

import (
	"context"

	"github.com/urfave/cli/v2"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/nbd-wtf/go-nostr"
)

func required[T comparable](r mcp.CallToolRequest, p string) T {
	var zero T
	if _, ok := r.Params.Arguments.(map[string]any); !ok {
		return zero
	}
	if _, ok := r.Params.Arguments.(map[string]any)[p]; !ok {
		return zero
	}
	if _, ok := r.Params.Arguments.(map[string]any)[p].(T); !ok {
		return zero
	}
	if r.Params.Arguments.(map[string]any)[p].(T) == zero {
		return zero
	}
	return r.Params.Arguments.(map[string]any)[p].(T)
}

func optional[T any](r mcp.CallToolRequest, p string) (T, bool) {
	var zero T
	if _, ok := r.Params.Arguments.(map[string]any); !ok {
		return zero, false
	}
	if _, ok := r.Params.Arguments.(map[string]any)[p]; !ok {
		return zero, false
	}
	if _, ok := r.Params.Arguments.(map[string]any)[p].(T); !ok {
		return zero, false
	}
	return r.Params.Arguments.(map[string]any)[p].(T), true
}

func doMcp(cCtx *cli.Context) error {
	s := server.NewMCPServer(
		"algia",
		version,
	)

	s.AddTool(mcp.NewTool("favorite_nostr_event",
		mcp.WithDescription("Like (favorite) a specific Nostr note by its event ID. Use the 'id' from get_nostr_timeline output to like recent notes. Example: Like the first note in the timeline."),
		mcp.WithString("id", mcp.Description("The event ID (hex string) of the note to like"), mcp.Required()),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		err := callLike(&likeArg{
			cfg: cCtx.App.Metadata["config"].(*Config),
			id:  required[string](r, "id"),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("OK"), nil
	})

	s.AddTool(mcp.NewTool("zap_nostr_note",
		mcp.WithDescription("Send a Lightning zap (sats) to a note"),
		mcp.WithString("id", mcp.Description("The event ID (hex string) of the note to zap"), mcp.Required()),
		mcp.WithNumber("amount_sats", mcp.Description("Amount in satoshis"), mcp.Required()),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		err := callZap(&zapArg{
			cfg:    cCtx.App.Metadata["config"].(*Config),
			id:     required[string](r, "id"),
			amount: required[uint64](r, "amount_sats"),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("OK"), nil
	})

	s.AddTool(mcp.NewTool("post_nostr_note",
		mcp.WithString("content", mcp.Description("Content of the note"), mcp.Required()),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		err := callPost(&postArg{
			cfg:     cCtx.App.Metadata["config"].(*Config),
			content: required[string](r, "content"),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("OK"), nil
	})

	s.AddTool(mcp.NewTool("get_nostr_timeline",
		mcp.WithDescription("Fetch the latest Nostr timeline events (notes). Returns a list of events with IDs, content, and authors. Use this to get note IDs for liking or zapping. Example: Get 10 recent events from a user's timeline."),
		mcp.WithNumber("number", mcp.Description("Number of events to fetch (default 10)"), mcp.DefaultNumber(10)),
		mcp.WithString("user", mcp.Description("Optional: Pubkey or npub of the user whose timeline to fetch"), mcp.DefaultString("")),
		mcp.WithOutputSchema[[]*nostr.Event](),
	), mcp.NewStructuredToolHandler(func(ctx context.Context, r mcp.CallToolRequest, arg any) ([]*nostr.Event, error) {
		events, err := callTimeline(&timelineArg{
			cfg: cCtx.App.Metadata["config"].(*Config),
			n:   r.GetInt("number", 10),
			u:   r.GetString("user", ""),
		})
		if err != nil {
			return nil, err
		}
		return events, nil
	}))

	s.AddTool(mcp.NewTool("search_nostr_notes",
		mcp.WithDescription("search nostr notes"),
		mcp.WithString("search", mcp.Description("words for search"), mcp.Required()),
		mcp.WithOutputSchema[[]*nostr.Event](),
	), mcp.NewStructuredToolHandler(func(ctx context.Context, r mcp.CallToolRequest, arg any) ([]*nostr.Event, error) {
		events, err := callSearch(&searchArg{
			cfg:    cCtx.App.Metadata["config"].(*Config),
			search: required[string](r, "search"),
		})
		if err != nil {
			return nil, err
		}
		return events, nil
	}))

	return server.ServeStdio(s)
}
