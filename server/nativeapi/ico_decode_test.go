package nativeapi

import (
	_ "embed"
	"image"
	_ "image/png"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

//go:embed testdata/ndr_favicon.ico
var ndrFaviconICO []byte

var _ = Describe("decodeICO", func() {
	It("decodes the NDR favicon.ico (8-bit BMP DIB)", func() {
		img, err := decodeICO(ndrFaviconICO)
		Expect(err).NotTo(HaveOccurred())
		Expect(img.Bounds().Dx()).To(Equal(48))
		Expect(img.Bounds().Dy()).To(Equal(48))
	})

	It("converts ICO to PNG bytes", func() {
		reader, err := decodeICOToPNG(ndrFaviconICO)
		Expect(err).NotTo(HaveOccurred())

		img, format, err := image.DecodeConfig(reader)
		Expect(err).NotTo(HaveOccurred())
		Expect(format).To(Equal("png"))
		Expect(img.Width).To(Equal(48))
		Expect(img.Height).To(Equal(48))
	})
})
