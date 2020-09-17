// Package x264 provides H.264/MPEG-4 AVC codec encoder based on [x264](https://www.videolan.org/developers/x264.html) library.
package x264

import "C"

import (
	"fmt"
	"image"
	"io"

	"github.com/piepacker/x264-go/x264c"
)

// Logging constants.
const (
	LogNone int32 = iota - 1
	LogError
	LogWarning
	LogInfo
	LogDebug
)

// Options represent encoding options.
type Options struct {
	// Frame width.
	Width int
	// Frame height.
	Height int
	// Frame rate.
	FrameRate int
	// Tunings: film, animation, grain, stillimage, psnr, ssim, fastdecode, zerolatency.
	Tune string
	// Presets: ultrafast, superfast, veryfast, faster, fast, medium, slow, slower, veryslow, placebo.
	Preset string
	// Profiles: baseline, main, high, high10, high422, high444.
	Profile string
	// Log level.
	LogLevel int32
	// CSP
	Csp int32
	// Pts
	Pts int64
	// Nals
	Nals []*x264c.Nal
	Param *x264c.Param
}

// Encoder type.
type Encoder struct {
	e *x264c.T
	w io.Writer

	img  *YCbCr
	opts *Options

	csp int32
	pts int64

	nnals int32
	nals  []*x264c.Nal

	picIn x264c.Picture
}

func DefaultOptions(width, height, fps int) (*Options, error) {
	opts := &Options{
		Width:     width,
		Height:    height,
		FrameRate: fps,
		Tune:      "zerolatency",
		Preset:    "veryfast",
		Profile:   "baseline",
		LogLevel:  LogInfo,
		Csp: x264c.CspI420,
		Pts: 0,
		Nals: make([]*x264c.Nal, 3),
	}
	if err := DefaultParams(opts); err != nil {
		return nil, err
	}
	return opts, nil
}

// DefaultParams sets up the Param field of an Option struct with the default values expected by the x264/gen2brain.
// DefaultParams is a good starting point, knowing that Params can always be updated later on.
// NOTE: DefaultParams expected that the top level fields of the Options struct (except Param) to be already filled in.
func DefaultParams(opts *Options) error {
	param := x264c.Param{}
	if opts.Preset != "" && opts.Profile != "" {
		ret := x264c.ParamDefaultPreset(&param, opts.Preset, opts.Tune)
		if ret < 0 {
			return fmt.Errorf("x264: invalid preset/tune name")
		}
	} else {
		x264c.ParamDefault(&param)
	}

	param.IWidth = int32(opts.Width)
	param.IHeight = int32(opts.Height)

	param.ICsp = x264c.CspI420
	param.BVfrInput = 0
	param.BRepeatHeaders = 1
	param.BAnnexb = 1

	param.ILogLevel = opts.LogLevel

	if opts.FrameRate > 0 {
		param.IFpsNum = uint32(opts.FrameRate)
		param.IFpsDen = 1

		param.IKeyintMax = int32(opts.FrameRate)
		param.BIntraRefresh = 1
	}

	if opts.Profile != "" {
		ret := x264c.ParamApplyProfile(&param, opts.Profile)
		if ret < 0 {
			return fmt.Errorf("x264: invalid profile name")
		}
	}
	opts.Param = &param
	return nil
}

// NewEncoder returns new x264 encoder.
func NewEncoder(w io.Writer, opts *Options) (e *Encoder, err error) {
	e = &Encoder{}

	e.w = w
	e.pts = opts.Pts
	e.opts = opts

	e.csp = opts.Csp

	e.nals = opts.Nals
	e.img = NewYCbCr(image.Rect(0, 0, e.opts.Width, e.opts.Height))

	if opts.Param != nil {
		// if param is specified (not nil) then param is used.
	} else {
		err = DefaultParams(opts)
		if err != nil {
			return
		}
	}

	// Allocate on create instead while encoding
	var picIn x264c.Picture
	ret := x264c.PictureAlloc(&picIn, e.csp, int32(e.opts.Width), int32(e.opts.Height))
	if ret < 0 {
		err = fmt.Errorf("x264: cannot allocate picture")
		return
	}
	e.picIn = picIn
	defer func() {
		// Cleanup if intialization fail
		if err != nil {
			x264c.PictureClean(&picIn)
		}
	}()

	e.e = x264c.EncoderOpen(opts.Param)
	if e.e == nil {
		err = fmt.Errorf("x264: cannot open the encoder")
		return
	}

	ret = x264c.EncoderHeaders(e.e, e.nals, &e.nnals)
	if ret < 0 {
		err = fmt.Errorf("x264: cannot encode headers")
		return
	}

	if ret > 0 {
		b := C.GoBytes(e.nals[0].PPayload, C.int(ret))
		n, er := e.w.Write(b)
		if er != nil {
			err = er
			return
		}

		if int(ret) != n {
			err = fmt.Errorf("x264: error writing headers, size=%d, n=%d", ret, n)
		}
	}

	return
}

// Encode encodes image.
func (e *Encoder) Encode(im image.Image) (err error) {
	var picOut x264c.Picture

	e.img.ToYCbCr(im)

	picIn := e.picIn
	e.img.CopyToCPointer(picIn.Img.Plane[0], picIn.Img.Plane[1], picIn.Img.Plane[2])
	picIn.IPts = e.pts
	e.pts++

	ret := x264c.EncoderEncode(e.e, e.nals, &e.nnals, &picIn, &picOut)
	if ret < 0 {
		err = fmt.Errorf("x264: cannot encode picture")
		return
	}

	if ret > 0 {
		b := C.GoBytes(e.nals[0].PPayload, C.int(ret))

		n, er := e.w.Write(b)
		if er != nil {
			err = er
			return
		}

		if int(ret) != n {
			err = fmt.Errorf("x264: error writing payload, size=%d, n=%d", ret, n)
		}
	}

	return
}

// Flush flushes encoder.
func (e *Encoder) Flush() (err error) {
	var picOut x264c.Picture

	for x264c.EncoderDelayedFrames(e.e) > 0 {
		ret := x264c.EncoderEncode(e.e, e.nals, &e.nnals, nil, &picOut)
		if ret < 0 {
			err = fmt.Errorf("x264: cannot encode picture")
			return
		}

		if ret > 0 {
			b := C.GoBytes(e.nals[0].PPayload, C.int(ret))

			n, er := e.w.Write(b)
			if er != nil {
				err = er
				return
			}

			if int(ret) != n {
				err = fmt.Errorf("x264: error writing payload, size=%d, n=%d", ret, n)
			}
		}
	}

	return
}

// Close closes encoder.
func (e *Encoder) Close() error {
	picIn := e.picIn
	x264c.PictureClean(&picIn)
	x264c.EncoderClose(e.e)
	return nil
}