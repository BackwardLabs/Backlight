package api

import (
	"context"
	"encoding/json"
)

func (s *Server) enrichIncidentMetadata(ctx context.Context, chain, txHash string, metadata json.RawMessage) json.RawMessage {
	if s == nil || s.Resolver == nil {
		return metadata
	}
	enriched, err := s.Resolver.EnrichIncidentMetadata(ctx, chain, txHash, metadata)
	if err != nil || len(enriched) == 0 {
		return metadata
	}
	return enriched
}
