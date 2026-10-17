package diagnostics

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
)

type callKey struct{}
type transcriptKey struct{}

func WithCall(ctx context.Context, id string) context.Context {
	if _, err := uuid.Parse(id); err != nil {
		return ctx
	}
	return context.WithValue(ctx, callKey{}, id)
}

func CallID(ctx context.Context) string { id, _ := ctx.Value(callKey{}).(string); return id }

func WithTranscript(ctx context.Context, logger *slog.Logger) context.Context {
	if logger == nil {
		return ctx
	}
	return context.WithValue(ctx, transcriptKey{}, logger)
}

// ModelOutput is deliberately routed only to the private transcript sink.
func ModelOutput(ctx context.Context, provider, model, finish, content string) {
	if logger, _ := ctx.Value(transcriptKey{}).(*slog.Logger); logger != nil {
		logger.Info("nlu_output", "call_id", CallID(ctx), "provider", provider, "model", model, "finish_reason", finish, "content", content)
	}
}
