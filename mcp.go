package main

import (
	"context"

	"github.com/urfave/cli/v2"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
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
	s.AddTool(mcp.NewTool("send_satoshi",
		mcp.WithDescription("send zap to note with specified amount"),
		mcp.WithString("note", mcp.Description("Note ID"), mcp.Required()),
		mcp.WithNumber("amount", mcp.Description("Zap amount satoshi to the note"), mcp.Required()),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		err := callZap(&zapArg{
			cfg:    cCtx.App.Metadata["config"].(*Config),
			amount: required[uint64](r, "amount"),
			id:     required[string](r, "note"),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("OK"), nil
	})

	s.AddTool(mcp.NewTool("favorite_nostr_event",
		mcp.WithDescription("favorite note"),
		mcp.WithString("id", mcp.Description("ID"), mcp.Required()),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		err := callLike(&likeArg{
			cfg: cCtx.App.Metadata["config"].(*Config),
			id:  required[string](r, "note"),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("OK"), nil
	})

	s.AddTool(mcp.NewTool("publish_nostr_event",
		mcp.WithDescription("publish note"),
		mcp.WithString("content", mcp.Description("Content"), mcp.Required()),
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
	return server.ServeStdio(s)
}
