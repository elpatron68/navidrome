package subsonic

import (
	"net/http"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/core/mix"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/server/subsonic/responses"
	"github.com/navidrome/navidrome/utils/req"
	"github.com/navidrome/navidrome/utils/slice"
)

// GetPersonalMix returns a habit-based, weighted-random list of tracks for the logged-in user
// (see core/mix). This is a Navidrome extension, not part of the Subsonic standard.
func (api *Router) GetPersonalMix(r *http.Request) (*responses.Subsonic, error) {
	if !conf.Server.PersonalMix.Enabled {
		return nil, newError(responses.ErrorGeneric, "Personal Mix is disabled")
	}

	p := req.Params(r)
	size := min(p.IntOr("size", conf.Server.PersonalMix.Size), 500)

	musicFolderIds, err := selectedMusicFolderIds(r, false)
	if err != nil {
		return nil, err
	}

	songs, err := api.mixer.PersonalMix(r.Context(), mix.Options{
		Size:       size,
		SeedID:     p.StringOr("seedId", ""),
		Genre:      p.StringOr("genre", ""),
		FromYear:   p.IntOr("fromYear", 0),
		ToYear:     p.IntOr("toYear", 0),
		LibraryIDs: musicFolderIds,
	})
	if err != nil {
		log.Error(r, "Error building personal mix", err)
		return nil, err
	}

	response := newResponse()
	response.PersonalMix = &responses.Songs{
		Songs: slice.MapWithArg(songs, r.Context(), childFromMediaFile),
	}
	return response, nil
}
