// Package mix implements the "Personal Mix": a habit-based, weighted-random selection of tracks
// built from the logged-in user's listening habits (play counts, recency, loved and rated tracks),
// optionally blended with similar tracks from the configured metadata agents.
//
// It is deliberately different from:
//   - getRandomSongs: uniform random, ignores the user's annotations;
//   - getSimilarSongs / Jellyfin InstantMix: seed-based similarity for a single track/album/artist.
//
// The Personal Mix combines "exploitation" (tracks the user already likes/plays) with "exploration"
// (similar or new tracks), using a weighted random chooser and per-artist diversity constraints.
package mix

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/core/external"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/utils/random"
	"golang.org/x/sync/errgroup"
)

const (
	defaultSize    = 50
	maxSize        = 500
	maxSeeds       = 5                // number of seed tracks used to fetch similar songs
	exploreTimeout = 10 * time.Second // bounds the (external) similar-songs fetch
)

// Options controls a single Personal Mix request. The user is taken from the request context, so
// annotations (play count, rating, ...) are automatically scoped to the logged-in user.
type Options struct {
	Size       int
	SeedID     string // optional explicit seed (track/album/artist) instead of habit-derived seeds
	Genre      string
	FromYear   int
	ToYear     int
	LibraryIDs []int // restrict to these libraries (from the client's musicFolderId filter)
}

// Mixer generates a Personal Mix for the user in the request context.
type Mixer interface {
	PersonalMix(ctx context.Context, opts Options) (model.MediaFiles, error)
}

func New(ds model.DataStore, provider external.Provider) Mixer {
	return &mixer{ds: ds, provider: provider}
}

type mixer struct {
	ds       model.DataStore
	provider external.Provider
}

func (m *mixer) PersonalMix(ctx context.Context, opts Options) (model.MediaFiles, error) {
	cfg := conf.Server.PersonalMix

	size := opts.Size
	if size <= 0 {
		size = cfg.Size
	}
	if size <= 0 {
		size = defaultSize
	}
	size = min(size, maxSize)

	base := m.baseFilters(opts)

	// Exploitation pool: the user's own listening habits.
	exploit, err := m.habitCandidates(ctx, base, size)
	if err != nil {
		return nil, err
	}

	// Cold start: no habits and no explicit seed -> behave like getRandomSongs.
	if len(exploit) == 0 && opts.SeedID == "" {
		return m.randomFallback(ctx, base, size)
	}

	// Exploration pool: similar tracks derived from the seed(s).
	explore := m.exploreCandidates(ctx, opts, exploit, size)

	discoveryRatio := math.Max(0, math.Min(1, cfg.DiscoveryRatio))
	nDiscovery := int(math.Round(float64(size) * discoveryRatio))
	nExploit := size - nDiscovery

	now := time.Now()
	chosen := make(map[string]bool, size)
	artistCount := make(map[string]int, size)
	habitWeight := habitWeightFn(now, cfg.RecencyHalfLife)
	discoveryWeight := func(model.MediaFile) int { return 10 }

	var result model.MediaFiles
	result = append(result, pickWeighted(exploit, habitWeight, nExploit, cfg.MaxPerArtist, chosen, artistCount)...)
	result = append(result, pickWeighted(explore, discoveryWeight, nDiscovery, cfg.MaxPerArtist, chosen, artistCount)...)

	// Fill any shortfall from whichever pool still has candidates.
	if len(result) < size {
		result = append(result, pickWeighted(exploit, habitWeight, size-len(result), cfg.MaxPerArtist, chosen, artistCount)...)
	}
	if len(result) < size {
		result = append(result, pickWeighted(explore, discoveryWeight, size-len(result), cfg.MaxPerArtist, chosen, artistCount)...)
	}

	// Last resort: top up with random tracks so the mix reaches the requested size.
	if len(result) < size {
		result = m.topUpRandom(ctx, base, size, result, chosen, artistCount, cfg.MaxPerArtist)
	}

	shuffle(result)
	return result, nil
}

// baseFilters builds the shared WHERE clause: present files only, plus optional library and year
// filters. Genre is intentionally not filtered here (it lives in the tags relation); it can be
// added later without changing the public API.
func (m *mixer) baseFilters(opts Options) squirrel.Sqlizer {
	and := squirrel.And{squirrel.Eq{"missing": false}}
	if len(opts.LibraryIDs) > 0 {
		and = append(and, squirrel.Eq{"library_id": opts.LibraryIDs})
	}
	if opts.FromYear > 0 {
		and = append(and, squirrel.GtOrEq{"year": opts.FromYear})
	}
	if opts.ToYear > 0 {
		and = append(and, squirrel.LtOrEq{"year": opts.ToYear})
	}
	return and
}

// habitCandidates gathers the user's most relevant tracks across four signals (frequent, recent,
// loved, highly rated) and returns them de-duplicated. Each track carries the user's annotations,
// because the queries run with the request context (per-user annotation JOIN).
func (m *mixer) habitCandidates(ctx context.Context, base squirrel.Sqlizer, size int) (model.MediaFiles, error) {
	perSignal := max(size, defaultSize)
	repo := m.ds.MediaFile(ctx)

	type query struct {
		sort   string
		filter squirrel.Sqlizer
	}
	queries := []query{
		{sort: "play_count", filter: squirrel.Gt{"play_count": 0}},         // frequency
		{sort: "play_date", filter: squirrel.Gt{"play_date": time.Time{}}}, // recency
		{sort: "starred_at", filter: squirrel.Eq{"starred": true}},         // loved
		{sort: "rating", filter: squirrel.GtOrEq{"rating": 4}},             // highly rated
	}

	seen := make(map[string]bool)
	var out model.MediaFiles
	for _, q := range queries {
		songs, err := repo.GetAll(model.QueryOptions{
			Sort:    q.sort,
			Order:   "desc",
			Max:     perSignal,
			Filters: squirrel.And{base, q.filter},
		})
		if err != nil {
			return nil, err
		}
		for _, mf := range songs {
			if !seen[mf.ID] {
				seen[mf.ID] = true
				out = append(out, mf)
			}
		}
	}
	return out, nil
}

// exploreCandidates fetches similar tracks for a small set of seeds. Seeds are either the explicit
// SeedID or a sample of the user's habit tracks. Provider errors (e.g. no external agents
// configured) degrade to an empty exploration pool rather than failing the whole mix.
func (m *mixer) exploreCandidates(ctx context.Context, opts Options, exploit model.MediaFiles, size int) model.MediaFiles {
	if conf.Server.PersonalMix.DiscoveryRatio <= 0 && opts.SeedID == "" {
		return nil
	}

	var seeds []string
	if opts.SeedID != "" {
		seeds = []string{opts.SeedID}
	} else {
		for _, i := range sampleIndexes(len(exploit), maxSeeds) {
			seeds = append(seeds, exploit[i].ID)
		}
	}
	if len(seeds) == 0 {
		return nil
	}

	perSeed := max(size/len(seeds)+1, 10)
	ctx, cancel := context.WithTimeout(ctx, exploreTimeout)
	defer cancel()

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	var mu sync.Mutex
	seen := make(map[string]bool)
	var out model.MediaFiles
	for _, seed := range seeds {
		g.Go(func() error {
			songs, err := m.provider.SimilarSongs(gctx, seed, perSeed)
			if err != nil {
				log.Debug(gctx, "PersonalMix: could not fetch similar songs", "seed", seed, err)
				return nil
			}
			mu.Lock()
			defer mu.Unlock()
			for _, mf := range songs {
				if !seen[mf.ID] {
					seen[mf.ID] = true
					out = append(out, mf)
				}
			}
			return nil
		})
	}
	_ = g.Wait()
	return out
}

func (m *mixer) randomFallback(ctx context.Context, base squirrel.Sqlizer, size int) (model.MediaFiles, error) {
	return m.ds.MediaFile(ctx).GetRandom(model.QueryOptions{Filters: base, Max: size})
}

func (m *mixer) topUpRandom(ctx context.Context, base squirrel.Sqlizer, size int, result model.MediaFiles,
	chosen map[string]bool, artistCount map[string]int, maxPerArtist int) model.MediaFiles {
	extra, err := m.ds.MediaFile(ctx).GetRandom(model.QueryOptions{Filters: base, Max: size * 2})
	if err != nil {
		log.Debug(ctx, "PersonalMix: random top-up failed", err)
		return result
	}
	for _, mf := range extra {
		if len(result) >= size {
			break
		}
		if chosen[mf.ID] {
			continue
		}
		ak := artistKey(mf)
		if maxPerArtist > 0 && ak != "" && artistCount[ak] >= maxPerArtist {
			continue
		}
		result = append(result, mf)
		chosen[mf.ID] = true
		artistCount[ak]++
	}
	return result
}

// habitWeightFn returns a scoring function that rewards frequently played, recently played, loved
// and highly rated tracks. Weights are positive integers, as required by the WeightedChooser.
func habitWeightFn(now time.Time, half time.Duration) func(model.MediaFile) int {
	return func(mf model.MediaFile) int {
		w := 10
		if mf.PlayCount > 0 {
			w += int(math.Log2(float64(mf.PlayCount)+1) * 10)
		}
		if mf.PlayDate != nil && half > 0 {
			age := now.Sub(*mf.PlayDate)
			if age < 0 {
				age = 0
			}
			decay := math.Exp(-math.Ln2 * float64(age) / float64(half))
			w += int(decay * 20)
		}
		if mf.Starred {
			w += 25
		}
		if mf.Rating > 0 {
			w += mf.Rating * 5
		}
		if w < 1 {
			w = 1
		}
		return w
	}
}

// pickWeighted selects up to n tracks from pool using weighted random selection without
// replacement, skipping already-chosen tracks and enforcing the per-artist cap.
func pickWeighted(pool model.MediaFiles, weightFn func(model.MediaFile) int, n, maxPerArtist int,
	chosen map[string]bool, artistCount map[string]int) model.MediaFiles {
	if n <= 0 || len(pool) == 0 {
		return nil
	}
	wc := random.NewWeightedChooser[model.MediaFile]()
	for _, mf := range pool {
		if chosen[mf.ID] {
			continue
		}
		w := weightFn(mf)
		if w < 1 {
			w = 1
		}
		wc.Add(mf, w)
	}

	var res model.MediaFiles
	for len(res) < n && wc.Size() > 0 {
		mf, err := wc.Pick()
		if err != nil {
			break
		}
		if chosen[mf.ID] {
			continue
		}
		ak := artistKey(mf)
		if maxPerArtist > 0 && ak != "" && artistCount[ak] >= maxPerArtist {
			continue
		}
		res = append(res, mf)
		chosen[mf.ID] = true
		artistCount[ak]++
	}
	return res
}

func artistKey(mf model.MediaFile) string {
	if mf.AlbumArtistID != "" {
		return mf.AlbumArtistID
	}
	if mf.ArtistID != "" {
		return mf.ArtistID
	}
	return mf.Artist
}

// sampleIndexes returns up to count distinct random indexes in [0, size).
func sampleIndexes(size, count int) []int {
	if size <= 0 {
		return nil
	}
	idx := make([]int, size)
	for i := range idx {
		idx[i] = i
	}
	shuffleInts(idx)
	return idx[:min(count, size)]
}

func shuffle[T any](s []T) {
	for i := len(s) - 1; i > 0; i-- {
		j := int(random.Int64N(i + 1))
		s[i], s[j] = s[j], s[i]
	}
}

func shuffleInts(s []int) { shuffle(s) }
