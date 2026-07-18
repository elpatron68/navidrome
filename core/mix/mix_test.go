package mix_test

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/core/mix"
	"github.com/navidrome/navidrome/model"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMix(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Mix Suite")
}

// fakeMFRepo honors QueryOptions.Sort to emulate the per-signal filters that the real repository
// applies via the annotation JOIN. Only GetAll/GetRandom are used by the mixer.
type fakeMFRepo struct {
	model.MediaFileRepository
	all model.MediaFiles
}

func (f *fakeMFRepo) GetAll(qo ...model.QueryOptions) (model.MediaFiles, error) {
	o := model.QueryOptions{}
	if len(qo) > 0 {
		o = qo[0]
	}
	var res model.MediaFiles
	for _, mf := range f.all {
		switch o.Sort {
		case "play_count":
			if mf.PlayCount > 0 {
				res = append(res, mf)
			}
		case "play_date":
			if mf.PlayDate != nil {
				res = append(res, mf)
			}
		case "starred_at":
			if mf.Starred {
				res = append(res, mf)
			}
		case "rating":
			if mf.Rating >= 4 {
				res = append(res, mf)
			}
		default:
			res = append(res, mf)
		}
	}
	if o.Max > 0 && len(res) > o.Max {
		res = res[:o.Max]
	}
	return res, nil
}

func (f *fakeMFRepo) GetRandom(qo ...model.QueryOptions) (model.MediaFiles, error) {
	o := model.QueryOptions{}
	if len(qo) > 0 {
		o = qo[0]
	}
	res := append(model.MediaFiles{}, f.all...)
	if o.Max > 0 && len(res) > o.Max {
		res = res[:o.Max]
	}
	return res, nil
}

type fakeDataStore struct {
	model.DataStore
	repo *fakeMFRepo
}

func (d *fakeDataStore) MediaFile(context.Context) model.MediaFileRepository { return d.repo }

type fakeProvider struct {
	similar model.MediaFiles
	err     error
}

func (f *fakeProvider) SimilarSongs(context.Context, string, int) (model.MediaFiles, error) {
	return f.similar, f.err
}
func (f *fakeProvider) TopSongs(context.Context, string, int) (model.MediaFiles, error) {
	return nil, nil
}
func (f *fakeProvider) UpdateAlbumInfo(context.Context, string) (*model.Album, error) {
	return nil, nil
}
func (f *fakeProvider) UpdateArtistInfo(context.Context, string, int, bool) (*model.Artist, error) {
	return nil, nil
}
func (f *fakeProvider) ArtistImage(context.Context, string) (*url.URL, error) { return nil, nil }
func (f *fakeProvider) AlbumImage(context.Context, string) (*url.URL, error)  { return nil, nil }

func track(id, artistID string) model.MediaFile {
	return model.MediaFile{ID: id, Title: id, ArtistID: artistID, AlbumArtistID: artistID, Artist: artistID}
}

func ids(mfs model.MediaFiles) map[string]bool {
	m := make(map[string]bool, len(mfs))
	for _, mf := range mfs {
		m[mf.ID] = true
	}
	return m
}

var _ = Describe("Mixer", func() {
	var (
		ctx      context.Context
		repo     *fakeMFRepo
		provider *fakeProvider
		mixer    mix.Mixer
	)

	BeforeEach(func() {
		ctx = GinkgoT().Context()
		repo = &fakeMFRepo{}
		provider = &fakeProvider{}
		conf.Server.PersonalMix.Enabled = true
		conf.Server.PersonalMix.Size = 50
		conf.Server.PersonalMix.DiscoveryRatio = 0
		conf.Server.PersonalMix.MaxPerArtist = 0
		conf.Server.PersonalMix.RecencyHalfLife = 720 * time.Hour
		mixer = mix.New(&fakeDataStore{repo: repo}, provider)
	})

	Describe("cold start (no listening history)", func() {
		It("falls back to random tracks", func() {
			repo.all = model.MediaFiles{track("a", "art1"), track("b", "art2"), track("c", "art3")}
			res, err := mixer.PersonalMix(ctx, mix.Options{Size: 10})
			Expect(err).ToNot(HaveOccurred())
			Expect(res).To(HaveLen(3))
			Expect(ids(res)).To(HaveKey("a"))
		})
	})

	Describe("habit-based selection", func() {
		BeforeEach(func() {
			now := time.Now()
			played := track("played", "art1")
			played.PlayCount = 20
			played.PlayDate = &now
			loved := track("loved", "art2")
			loved.Starred = true
			rated := track("rated", "art3")
			rated.Rating = 5
			cold := track("cold", "art4") // no annotations -> not a habit candidate
			repo.all = model.MediaFiles{played, loved, rated, cold}
		})

		It("returns only tracks derived from listening habits when size fits", func() {
			res, err := mixer.PersonalMix(ctx, mix.Options{Size: 3})
			Expect(err).ToNot(HaveOccurred())
			Expect(res).To(HaveLen(3))
			got := ids(res)
			Expect(got).To(SatisfyAll(HaveKey("played"), HaveKey("loved"), HaveKey("rated")))
			Expect(got).ToNot(HaveKey("cold"))
		})

		It("tops up with random tracks when habits are insufficient", func() {
			res, err := mixer.PersonalMix(ctx, mix.Options{Size: 4})
			Expect(err).ToNot(HaveOccurred())
			Expect(res).To(HaveLen(4))
			Expect(ids(res)).To(HaveKey("cold"))
		})
	})

	Describe("per-artist diversity", func() {
		It("never exceeds MaxPerArtist for the same artist", func() {
			conf.Server.PersonalMix.MaxPerArtist = 2
			now := time.Now()
			var data model.MediaFiles
			for i := 0; i < 10; i++ {
				mf := track("same"+string(rune('0'+i)), "sameArtist")
				mf.PlayCount = int64(i + 1)
				mf.PlayDate = &now
				data = append(data, mf)
			}
			// a couple of other artists so a full mix is possible
			for i := 0; i < 5; i++ {
				mf := track("other"+string(rune('0'+i)), "artist"+string(rune('0'+i)))
				mf.Starred = true
				data = append(data, mf)
			}
			repo.all = data
			res, err := mixer.PersonalMix(ctx, mix.Options{Size: 12})
			Expect(err).ToNot(HaveOccurred())
			perArtist := map[string]int{}
			for _, mf := range res {
				perArtist[mf.AlbumArtistID]++
			}
			Expect(perArtist["sameArtist"]).To(BeNumerically("<=", 2))
		})
	})

	Describe("exploration (discovery)", func() {
		It("includes similar tracks from the provider", func() {
			conf.Server.PersonalMix.DiscoveryRatio = 1
			now := time.Now()
			seed := track("seed", "art1")
			seed.PlayCount = 5
			seed.PlayDate = &now
			repo.all = model.MediaFiles{seed}
			provider.similar = model.MediaFiles{
				track("sim1", "artX"), track("sim2", "artY"), track("sim3", "artZ"),
			}
			res, err := mixer.PersonalMix(ctx, mix.Options{Size: 3})
			Expect(err).ToNot(HaveOccurred())
			got := ids(res)
			Expect(got).To(SatisfyAny(HaveKey("sim1"), HaveKey("sim2"), HaveKey("sim3")))
		})
	})

	Describe("size handling", func() {
		It("returns no more than the available tracks", func() {
			repo.all = model.MediaFiles{track("a", "art1"), track("b", "art2")}
			res, err := mixer.PersonalMix(ctx, mix.Options{Size: 100})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(res)).To(BeNumerically("<=", 2))
		})
	})
})
