package subsonic

import (
	"context"
	"errors"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/core/mix"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/server/subsonic/responses"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type mockMixer struct {
	songs model.MediaFiles
	err   error
	opts  mix.Options
}

func (m *mockMixer) PersonalMix(_ context.Context, opts mix.Options) (model.MediaFiles, error) {
	m.opts = opts
	return m.songs, m.err
}

var _ = Describe("GetPersonalMix", func() {
	var router *Router
	var mocked *mockMixer

	BeforeEach(func() {
		conf.Server.PersonalMix.Enabled = true
		conf.Server.PersonalMix.Size = 50
		mocked = &mockMixer{}
		router = &Router{mixer: mocked}
	})

	It("returns the tracks from the mixer", func() {
		mocked.songs = model.MediaFiles{{ID: "1"}, {ID: "2"}}
		r := newGetRequest("size=2")

		resp, err := router.GetPersonalMix(r)

		Expect(err).ToNot(HaveOccurred())
		Expect(resp.PersonalMix).ToNot(BeNil())
		Expect(resp.PersonalMix.Songs).To(HaveLen(2))
		Expect(resp.PersonalMix.Songs[0].Id).To(Equal("1"))
	})

	It("passes request parameters to the mixer", func() {
		r := newGetRequest("size=17", "seedId=abc", "fromYear=2000", "toYear=2010")

		_, err := router.GetPersonalMix(r)

		Expect(err).ToNot(HaveOccurred())
		Expect(mocked.opts.Size).To(Equal(17))
		Expect(mocked.opts.SeedID).To(Equal("abc"))
		Expect(mocked.opts.FromYear).To(Equal(2000))
		Expect(mocked.opts.ToYear).To(Equal(2010))
	})

	It("uses the configured default size when size is not provided", func() {
		r := newGetRequest()

		_, err := router.GetPersonalMix(r)

		Expect(err).ToNot(HaveOccurred())
		Expect(mocked.opts.Size).To(Equal(50))
	})

	It("returns an error when the feature is disabled", func() {
		conf.Server.PersonalMix.Enabled = false
		r := newGetRequest()

		_, err := router.GetPersonalMix(r)

		Expect(err).To(MatchError(errSubsonic))
		var subErr subError
		errors.As(err, &subErr)
		Expect(subErr.code).To(Equal(responses.ErrorGeneric))
	})

	It("propagates errors from the mixer", func() {
		mocked.err = errors.New("boom")
		r := newGetRequest()

		_, err := router.GetPersonalMix(r)

		Expect(err).To(HaveOccurred())
	})
})
