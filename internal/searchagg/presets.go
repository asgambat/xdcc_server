package searchagg

import (
	"context"

	"xdcc_server/internal/store"
)

// ---------------------------------------------------------------------------
// Preset operations
// ---------------------------------------------------------------------------

// CreatePreset creates a new search preset.
func (a *Aggregator) CreatePreset(ctx context.Context, name, query, filtersJSON string, isDefault bool) (int64, error) {
	return a.store.AddSearchPreset(ctx, store.SearchPreset{
		Name:        name,
		Query:       query,
		FiltersJSON: filtersJSON,
		IsDefault:   isDefault,
	})
}

// GetPreset returns a preset by ID.
func (a *Aggregator) GetPreset(ctx context.Context, id int64) (*store.SearchPreset, error) {
	return a.store.GetSearchPreset(ctx, id)
}

// ListPresets returns all search presets.
func (a *Aggregator) ListPresets(ctx context.Context) ([]store.SearchPreset, error) {
	return a.store.ListSearchPresets(ctx)
}

// UpdatePreset updates an existing preset.
func (a *Aggregator) UpdatePreset(ctx context.Context, p store.SearchPreset) error {
	return a.store.UpdateSearchPreset(ctx, p)
}

// DeletePreset deletes a preset.
func (a *Aggregator) DeletePreset(ctx context.Context, id int64) error {
	return a.store.DeleteSearchPreset(ctx, id)
}

// SetDefaultPreset marks a preset as the default.
func (a *Aggregator) SetDefaultPreset(ctx context.Context, id int64) error {
	return a.store.SetDefaultSearchPreset(ctx, id)
}

// SearchPreset executes a saved preset's search.
func (a *Aggregator) SearchPreset(ctx context.Context, presetID int64, opts SearchOptions) (*SearchResult, error) {
	preset, err := a.store.GetSearchPreset(ctx, presetID)
	if err != nil || preset == nil {
		return nil, err
	}

	// Override query from preset if not specified
	if opts.Query == "" {
		opts.Query = preset.Query
	}

	// Apply preset filters (providers, min_size, max_size) from saved filters_json
	// Only apply if opts doesn't already have them set (caller values take precedence)
	f := ParseFilters(preset.FiltersJSON)
	if len(opts.Providers) == 0 && len(f.Providers) > 0 {
		opts.Providers = f.Providers
	}
	if opts.MinSize == "" && f.MinSize != "" {
		opts.MinSize = f.MinSize
	}
	if opts.MaxSize == "" && f.MaxSize != "" {
		opts.MaxSize = f.MaxSize
	}

	return a.Search(ctx, opts)
}
