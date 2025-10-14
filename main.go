package main

/*
#include <libavutil/avutil.h>
#include <libavutil/hwcontext.h>
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
#include <libswscale/swscale.h>


#cgo pkg-config: libavcodec libavutil libavformat libavcodec libavfilter libswscale
*/
import (
	"C"
)
import (
	"fmt"
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

	//sws
	swsCtx *C.struct_SwsContext
	swNV12 *Frame

	// hw
	hwframeCtx *C.AVBufferRef

	//params
	srcW int
	srcH int
	dstW int
	dstH int
}

func setupDecoder(ctx *context, inputFileName string) error {
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

	if ctx.decFrame, err = NewFrame(); err != nil {
		log.Fatalf("", err)
		return fmt.Errorf("failed to create a frame: %w", err)
	}

	ctx.srcW = int(ctx.decCodec.CAVCodecContext.width)
	ctx.srcH = int(ctx.decCodec.CAVCodecContext.height)

	ctx.swNV12, _ = NewFrame()
	ctx.swNV12.CAVFrame.format = C.AV_PIX_FMT_NV12
	ctx.swNV12.CAVFrame.width = C.int(ctx.srcW)
	ctx.swNV12.CAVFrame.height = C.int(ctx.srcH)

	if int(C.av_frame_get_buffer(ctx.swNV12.CAVFrame, 0)) < 0 {
		return fmt.Errorf("failed to allocate buffer for FMT_NV12 frame")
	}

	return nil
}

func setupEncoder(ctx *context) error {
	var err error

	// codecName := "av1_nvenc"
	codecName := "hevc_nvenc"
	codec := FindEncoderByName(codecName)
	if codec == nil {
		log.Fatalf("Could not find encoder %s", codecName)
		return fmt.Errorf("encoder not found")
	}

	// --- 2) Build options (NVENC-friendly; drop libvpx ones)
	params := map[string]string{
		"bitrate":   "1500000", // ~1.5 Mbps
		"width":     "2560",
		"height":    "1440",
		"framerate": "20",
		"gop_size":  "120",

		// NVENC options
		"preset":      "p5",  // p1..p7 (p5 good balance)
		"tune":        "ull", // ultra-low-latency
		"rc":          "cbr", // cbr|vbr|constqp
		"zerolatency": "1",
		// For 10-bit: use P010 and consider profile=1
		// "profile":   "1",
	}

	// --- 3) Create encoder context
	if ctx.encCodec, err = NewContextWithCodec(codec); err != nil {
		return fmt.Errorf("NewContextWithCodec: %w", err)
	}

	// --- 4) Create & attach CUDA hw device (auto-upload from SW frames)
	var hwDev *C.AVBufferRef
	if C.av_hwdevice_ctx_create(&hwDev, C.AV_HWDEVICE_TYPE_CUDA, nil, nil, 0) < 0 || hwDev == nil {
		return fmt.Errorf("failed to create CUDA hw device")
	}
	println("hwDev: ", hwDev)
	ctx.encCodec.CAVCodecContext.hw_device_ctx = C.av_buffer_ref(hwDev)

	println("ctx.encCodec.CAVCodecContext.hw_device_ctx: ", ctx.encCodec.CAVCodecContext.hw_device_ctx)

	// Create hw frames ctx (GPU surface pool)
	hwframeCtx := C.av_hwframe_ctx_alloc(hwDev)
	if unsafe.Pointer(hwframeCtx) == nil {
		return fmt.Errorf("av_hwframe_ctx_alloc failed")
	}
	println("hwFrameCtx: ", hwframeCtx)

	framesCtx := (*C.AVHWFramesContext)(unsafe.Pointer(hwframeCtx.data))
	framesCtx.format = C.AV_PIX_FMT_CUDA
	framesCtx.sw_format = C.AV_PIX_FMT_NV12
	framesCtx.width = C.int(ctx.srcW)
	framesCtx.height = C.int(ctx.srcH)
	if int(C.av_hwframe_ctx_init(hwframeCtx)) < 0 {
		return fmt.Errorf("Could not initiate hwframe context")
	}
	ctx.hwframeCtx = hwframeCtx
	ctx.encCodec.CAVCodecContext.hw_frames_ctx = C.av_buffer_ref(hwframeCtx)
	ctx.swsCtx = C.sws_getContext(C.int(ctx.srcW), C.int(ctx.srcH), ctx.decCodec.CAVCodecContext.pix_fmt,
		C.int(ctx.srcW), C.int(ctx.srcH), framesCtx.sw_format,
		C.SWS_BILINEAR, nil, nil, nil)

	// --- 5) Basic fields / time base / fps
	// NOTE: feed NV12 frames (or convert prior to SendFrame)
	w, _ := strconv.Atoi(params["width"])
	h, _ := strconv.Atoi(params["height"])
	fps, _ := strconv.Atoi(params["framerate"])

	ctx.encCodec.CAVCodecContext.width = C.int(w)
	ctx.encCodec.CAVCodecContext.height = C.int(h)

	ctx.encCodec.SetTimeBase(NewRational(1, fps))
	ctx.encCodec.SetFrameRate(NewRational(fps, 1))

	// Low-latency: no B-frames
	ctx.encCodec.CAVCodecContext.max_b_frames = 0

	// GOP
	g, _ := strconv.Atoi(params["gop_size"])
	ctx.encCodec.CAVCodecContext.gop_size = C.int(g)

	// Bitrate
	br, _ := strconv.Atoi(params["bitrate"])
	ctx.encCodec.CAVCodecContext.bit_rate = C.long(br)

	ctx.encCodec.CAVCodecContext.pix_fmt = C.AV_PIX_FMT_CUDA

	// --- 6) Apply encoder options

	options := NewDictionary()
	defer options.Free()
	// Only pass actual encoder private opts through the OptionAccessor/Dictionary
	for _, k := range []string{"preset", "tune", "rc", "zerolatency"} {
		_ = options.Set(k, params[k])
	}

	if err := ctx.encCodec.OpenWithCodec(codec, options); err != nil {
		return fmt.Errorf("OpenWithCodec: %w", err)
	}

	// --- 7) Muxer: IVF with AV1 (works fine)
	ctx.encFmt = &FormatContext{}
	if C.avformat_alloc_output_context2(&ctx.encFmt.CAVFormatContext, nil, C.CString("mp4"), C.CString("output.mp4")) < 0 {
		return fmt.Errorf("alloc_output_context2 failed")
	}
	if C.avio_open(&ctx.encFmt.CAVFormatContext.pb, C.CString("output.mp4"), C.AVIO_FLAG_WRITE) < 0 {
		return fmt.Errorf("avio_open failed")
	}

	// New stream
	if ctx.encStream, err = ctx.encFmt.NewStreamWithCodec(codec); err != nil {
		return fmt.Errorf("NewStreamWithCodec: %w", err)
	}

	// Copy parameters and align time bases
	C.avcodec_parameters_from_context(ctx.encStream.CAVStream.codecpar, ctx.encCodec.CAVCodecContext)
	ctx.encStream.TimeBase().SetDenominator(ctx.encCodec.TimeBase().Denominator())
	ctx.encStream.TimeBase().SetNumerator(ctx.encCodec.TimeBase().Numerator())
	ctx.encStream.SetAverageFrameRate(ctx.encCodec.FrameRate())

	C.av_dump_format(ctx.encFmt.CAVFormatContext, 0, C.CString("output.ivf"), 1)

	if C.avformat_write_header(ctx.encFmt.CAVFormatContext, nil) < 0 {
		return fmt.Errorf("avformat_write_header failed")
	}

	println("done")
	return nil
}

func decode(ctx *context) {
	for decodeStream(ctx) {
	}
	close(frameChan)
}

func encode(ctx *context) {
	for {
		done, _ := encodeStream(ctx)
		if done {
			break
		}
	}
}

func TranscodeY4MToH265(ctx *context, inputFileName string) error {
	if err := setupDecoder(ctx, inputFileName); err != nil {
		println(err.Error())
	}
	if err := setupEncoder(ctx); err != nil {
		println(err.Error())
	}

	decode(ctx)
	encode(ctx)

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
		frame := ctx.decFrame
		cFrame := (*C.AVFrame)(unsafe.Pointer(frame.CAVFrame))

		code = C.avcodec_receive_frame(ctx.decCodec.CAVCodecContext, cFrame)
		if code == AVERROR(C.EAGAIN) || code == C.AVERROR_EOF {
			break
		} else if int(code) < 0 {
			log.Fatal("error when receiving frame in the decoder")
			return false
		}

		if ret := C.av_frame_make_writable(ctx.swNV12.CAVFrame); ret < 0 {
			panic("make_writable swNV12")
		}

		C.sws_scale(ctx.swsCtx,
			(**C.uint8_t)(&frame.CAVFrame.data[0]), (*C.int)(&frame.CAVFrame.linesize[0]),
			0, C.int(ctx.srcH),
			(**C.uint8_t)(&ctx.swNV12.CAVFrame.data[0]), (*C.int)(&ctx.swNV12.CAVFrame.linesize[0]),
		)

		hwFrame, _ := NewFrame()
		if int(C.av_hwframe_get_buffer(ctx.hwframeCtx, hwFrame.CAVFrame, 0)) < 0 {
			panic("sadfasdf")
		}
		if int(C.av_hwframe_transfer_data(hwFrame.CAVFrame, ctx.swNV12.CAVFrame, 0)) < 0 {
			panic("pashm")
		}
		frameChan <- hwFrame

		frame.Unref()
		// ctx.swNV12.Unref()

	}
	return true
}

var ecnt = 0

func encodeStream(ctx *context) (bool, error) {
	var err error

	frame, ok := <-frameChan
	if !ok {
		frame = nil
	} else {
		frame.CAVFrame.pts = C.long(ecnt)
		ecnt += 1
	}
	println(frame, ok)

	err = ctx.encCodec.FeedFrame(frame)
	if err != nil {
		println(err.Error())
		return false, err
	}

	if ok {
		frame.Free()
	}

	if ctx.encPkt == nil {
		ctx.encPkt, _ = NewPacket()
	}
	for {
		if err != nil {
			println("unable to allocate new packet for encoding")
			return false, err
		}
		ret := ctx.encCodec.GetPacket(ctx.encPkt)
		if ret == int(AVERROR(C.EAGAIN)) {
			break
		} else if ret == 0 {
			println("Successfully got a packet", ecnt, ctx.encPkt.Size())
			ctx.encPkt.RescaleTime(ctx.encCodec.TimeBase(), ctx.encStream.TimeBase())
			C.av_interleaved_write_frame(ctx.encFmt.CAVFormatContext, ctx.encPkt.CAVPacket)
			ctx.encPkt.Unref()
		} else if ret == int(C.AVERROR_EOF) {
			break
		}
	}

	if !ok {
		C.av_write_trailer(ctx.encFmt.CAVFormatContext)

		// 3. Close the output file handle
		if ctx.encFmt.CAVFormatContext.pb != nil {
			C.avio_closep(&ctx.encFmt.CAVFormatContext.pb)
		}

		// 4. Free the format context
		C.avformat_free_context(ctx.encFmt.CAVFormatContext)
		return true, nil
	}

	return false, nil
}

func AVERROR(e int) C.int {
	return -C.int(e)
}

func main() {
	ctx := &context{dstW: 2560, dstH: 1440}
	TranscodeY4MToH265(ctx, "input.y4m")
}
