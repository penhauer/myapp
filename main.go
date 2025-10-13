package main

/*
#include <libavutil/avutil.h>
#include <libavutil/error.h>
#include <libavcodec/avcodec.h>
#include <libavutil/avstring.h>
#include <libavformat/avformat.h>
#include <libavutil/channel_layout.h>
#include <libavutil/dict.h>
#include <libavutil/pixdesc.h>
#include <libavutil/opt.h>
#include <libavutil/frame.h>
#include <libavutil/parseutils.h>
#include <libavutil/common.h>
#include <libavutil/eval.h>
#include <libavfilter/avfilter.h>
#include <libavfilter/buffersrc.h>
#include <libavfilter/buffersink.h>

#cgo pkg-config: libavcodec libavutil libavformat libavcodec libavfilter
*/
import (
	"C"
)
import (
	"log"
	"strconv"
	"unsafe"
)

type context struct {
	// decoding
	decFmt    *FormatContext
	decStream *Stream
	decCodec  *CodecContext
	decPkt    *Packet
	decFrame  *Frame
	srcFilter *FilterContext

	// encoding
	encFmt     *FormatContext
	encStream  *Stream
	encCodec   *CodecContext
	encIO      *IOContext
	encPkt     *Packet
	encFrame   *Frame
	sinkFilter *FilterContext

	// transcoding
	filterGraph *Graph
}

func openDecoder(ctx *context, inputFileName string) error {
	var err error
	ctx.decFmt, err = NewContextForInput()
	if err != nil {
		log.Fatalf("Failed to open input context: %v\n", err)
		return err
	}

	options := NewDictionary()
	defer options.Free()

	if err := ctx.decFmt.OpenInput(inputFileName, nil, options); err != nil {
		log.Fatalf("Failed to open input file: %v\n", err)
		return err
	}

	if err := ctx.decFmt.FindStreamInfo(nil); err != nil {
		return err
	}

	ctx.decFmt.Dump(0, inputFileName, false)

	for i, stream := range ctx.decFmt.Streams() {
		println(i, " th stream is ", stream)
		println(stream.CAVStream.codecpar.codec_id)
		println(stream.CAVStream.codecpar.codec_type)
	}

	ctx.decStream = ctx.decFmt.Streams()[0]

	codecID := int(ctx.decStream.CAVStream.codecpar.codec_id)
	codec := NewCodecFromC(unsafe.Pointer(C.avcodec_find_decoder((C.enum_AVCodecID)(codecID))))

	if ctx.decCodec, err = NewContextWithCodec(codec); err != nil {
		log.Fatalf("Failed to create codec context: %v\n", err)
		return err
	}

	C.avcodec_parameters_to_context(ctx.decCodec.CAVCodecContext, ctx.decStream.CAVStream.codecpar)

	if err = ctx.decCodec.OpenWithCodec(codec, options); err != nil {
		log.Fatalf("Failed to open decoder: %v\n", err)
		return err
	}

	if ctx.decPkt, err = NewPacket(); err != nil {
		log.Fatalf("Failed to create a packet", err)
		return err
	}

	if ctx.decFrame, err = NewFrame(); err != nil {
		log.Fatalf("Failed to create a frame", err)
		return err
	}

	for decodeStream(ctx) {
	}
	return nil
}

func openEncoder(ctx *context) error {
	var codec *Codec
	var err error

	// codecName := "libx264"
	// codec = FindEncoderByName(codecName)

	codec = NewCodecFromC(unsafe.Pointer(C.avcodec_find_encoder(C.AV_CODEC_ID_VP9)))

	if codec == nil {
		log.Fatal("Could not find codec ")
		return err
	}

	options := NewDictionary()
	defer options.Free()

	params := map[string]string{
		"bitrate": "1500000",
		// "width":     "1920",
		"width": "2560",
		// "height":    "1080",
		"height":    "1440",
		"framerate": "20",
		"gop_size":  "120",
		// "pixel_format": fmt.Sprintf("%d", C.AV_PIX_FMT_YUV420P),
		// "pixel_format": fmt.Sprintf("%d", video_source.getContext().pix_fmt), // nvenc only
		// "preset":      "veryfast", // software
		// "tune":        "zerolatency", // software
		// "preset":      "p4",  // nvenc
		// "tune":        "ull", // nvenc
		// "rc":          "cbr", // nvenc
		// "zerolatency": "1",   // nvenc

		"quality": "realtime",
		// "deadline": "rt",
		"cpu-used":      "6",
		"row-mt":        "1",
		"tile-columns":  "3",
		"tile-rows":     "1",
		"threads":       "15",
		"lag-in-frames": "0",
		// "auto-alt-ref":  "0",
		// "rc_end_usage":  "cbr",
		// "b":             "8M",
		// "maxrate":       "8M",
		// "minrate":       "8M",
		// "bufsize": "8M",
		// "g":       "120",
	}

	var pix_fmt PixelFormat = (PixelFormat)(ctx.decCodec.CAVCodecContext.pix_fmt)
	params["pixel_format"] = pix_fmt.Name()

	println(pix_fmt.Name())

	for key, val := range params {
		if err := options.Set(key, val); err != nil {
			log.Fatalf("Failed to set encoder option %s: %v\n", key, err)
			return err
		}
	}

	if ctx.encCodec, err = NewContextWithCodec(codec); err != nil {
		log.Fatal("Could not initialize encoder context")
		return err
	}
	o := NewOptionAccessor(unsafe.Pointer(ctx.encCodec.CAVCodecContext.priv_data), false)

	for key, val := range params {
		switch key {
		case "bitrate":
			v, _ := strconv.Atoi(val)
			ctx.encCodec.CAVCodecContext.bit_rate = (C.long)(v)
		case "width":
			v, _ := strconv.Atoi(val)
			ctx.encCodec.CAVCodecContext.width = (C.int)(v)
		case "height":
			v, _ := strconv.Atoi(val)
			ctx.encCodec.CAVCodecContext.height = (C.int)(v)
		case "pixel_format":
			ctx.encCodec.CAVCodecContext.pix_fmt = ctx.decCodec.CAVCodecContext.pix_fmt
		case "framerate":
			v, _ := strconv.Atoi(val)
			ctx.encCodec.SetTimeBase(NewRational(1, v))
			ctx.encCodec.SetFrameRate(NewRational(v, 1))
		case "gop_size":
			v, _ := strconv.Atoi(val)
			ctx.encCodec.CAVCodecContext.gop_size = (C.int)(v)
		default:
			if err = o.SetOption(key, val); err != nil {
				println("cannot set option ", key, "= ", val, " for encoder ")
			}
		}
	}

	ctx.encFmt = &FormatContext{}
	C.avformat_alloc_output_context2(&ctx.encFmt.CAVFormatContext, nil, C.CString("ivf"), C.CString("output.ivf"))

	C.avio_open(&ctx.encFmt.CAVFormatContext.pb, C.CString("output.ivf"), C.AVIO_FLAG_WRITE)

	ctx.encCodec.OpenWithCodec(codec, options)

	if ctx.encStream, err = ctx.encFmt.NewStreamWithCodec(codec); err != nil {
		log.Fatalln("cannot create a new stream")
		return err
	}

	C.avcodec_parameters_from_context(ctx.encStream.CAVStream.codecpar, ctx.encCodec.CAVCodecContext)
	ctx.encStream.TimeBase().SetDenominator(ctx.encCodec.TimeBase().Denominator())
	ctx.encStream.TimeBase().SetNumerator(ctx.encCodec.TimeBase().Numerator())
	ctx.encStream.SetAverageFrameRate(ctx.encCodec.FrameRate())

	C.av_dump_format(ctx.encFmt.CAVFormatContext, 0, C.CString("output.ivf"), 1)

	C.avformat_write_header(ctx.encFmt.CAVFormatContext, nil)

	for {
		encodeStream(ctx)
	}

}

func TranscodeY4MToH264(ctx *context, inputFileName string) error {
	openDecoder(ctx, inputFileName)
	openEncoder(ctx)
	return nil
}

var frameChan chan *Frame = make(chan *Frame, 1000)

func decodeStream(ctx *context) bool {
	var err error
	if ctx.decPkt, err = NewPacket(); err != nil {
		println("unable to allocate new packet for decoding")
	}

	reading, err := ctx.decFmt.ReadFrame(ctx.decPkt)
	if err != nil {
		log.Fatalf("Failed to read packet: %v\n", err)
	}
	if !reading {
		return false
	}
	defer ctx.decPkt.Unref()

	if ctx.decPkt.StreamIndex() != ctx.decStream.Index() {
		return true
	}
	ctx.decPkt.RescaleTime(ctx.decStream.TimeBase(), ctx.decCodec.TimeBase())

	pkt := ctx.decPkt

	cPkt := (*C.AVPacket)(unsafe.Pointer(pkt.CAVPacket))
	code := C.avcodec_send_packet(ctx.decCodec.CAVCodecContext, cPkt)
	if int(code) < 0 {
		log.Fatal("error when sending packet to codec")
		return false
	}

	for int(code) >= 0 {
		if ctx.decFrame, err = NewFrame(); err != nil {
			println("Unable to alloc a new frame for decoding")
		}
		frame := ctx.decFrame
		cFrame := (*C.AVFrame)(unsafe.Pointer(frame.CAVFrame))

		code = C.avcodec_receive_frame(ctx.decCodec.CAVCodecContext, cFrame)
		if code == AVERROR(C.EAGAIN) || code == C.AVERROR_EOF {
			break
		} else if int(code) < 0 {
			log.Fatal("error when receiving frame in the decoder")
			return false
		}

		frameChan <- frame
	}
	return true
}

var ecnt = 0

func encodeStream(ctx *context) error {
	var err error
	frame := <-frameChan
	frame.CAVFrame.pts = C.long(ecnt)
	ecnt += 1

	println("Feeding frame", ecnt, frame.CAVFrame, frame.CAVFrame.height, " x ", frame.CAVFrame.width)
	err = ctx.encCodec.FeedFrame(frame)
	if err != nil {
		println(err.Error())
		return err
	}

	frame.Free()

	if ctx.encPkt == nil {
		ctx.encPkt, _ = NewPacket()
	}
	for {
		if err != nil {
			println("unable to allocate new packet for encoding")
			return err
		}
		ret := ctx.encCodec.GetPacket(ctx.encPkt)
		if ret == -11 {
			break
		} else if ret == 0 {
			println("Successfully got a packet", ecnt, ctx.encPkt.Size())
			ctx.encPkt.RescaleTime(ctx.encCodec.TimeBase(), ctx.encStream.TimeBase())
			C.av_interleaved_write_frame(ctx.encFmt.CAVFormatContext, ctx.encPkt.CAVPacket)
			ctx.encPkt.Unref()
		}
	}

	return nil
}

func AVERROR(e int) C.int {
	return -C.int(e)
}

func main() {
	ctx := &context{}
	TranscodeY4MToH264(ctx, "input.y4m")

}
