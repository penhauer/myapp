package transcoder

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
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log"
	"log/slog"
	"os"
	"unsafe"
)

type KeyFrameCallbackType func() int

var (
	FRAME_BUFFER = 30
	LOOKAHEAD    = 2
	logger       = slog.Default()
)

type Config struct {
	Codec            string
	TargetW          int
	TargetH          int
	InputFile        string
	InitialBitrate   int
	KeyFrameCallback KeyFrameCallbackType
	GoPSize          int
	LoopVideo        bool
	EncoderFrameRate int
	LogLevel         slog.Level // Log level for transcoder (e.g., slog.LevelDebug, slog.LevelInfo)
	MP4OutputFile    string
	HEVCOutputFile   string

	saveHEVC bool
	saveMP4  bool
}

type EncoderFrame struct {
	KeyFrame bool
	Data     []byte
	Bitrate  int
}

type transcodingCtx struct {
	// Conig
	config *Config

	//params
	srcW int
	srcH int

	hevcFile *os.File

	// decoding
	decFmt    *FormatContext
	decStream *Stream
	decCodec  *CodecContext
	decPkt    *Packet
	decFrame  *Frame
	srcFilter *FilterContext

	decFrameCnt     int
	decTrueFrameCnt int

	// encoding
	encFmt     *FormatContext
	encStream  *Stream
	encCodec   *CodecContext
	encIO      *IOContext
	encPkt     *Packet
	encFrame   *Frame
	sinkFilter *FilterContext

	// frames
	decFrameChan chan *Frame
	encFrameChan chan *EncoderFrame
	frameCnt     int
	nextKeyFrame int
	frameBitrate int
	pktFrameCnt  int

	// for debugging (todo: remove later)
	realFrameCnt  int
	droppedFrames int

	//sws
	swsCtx *C.struct_SwsContext
	swNV12 *Frame

	// hw
	hwframeCtx *C.AVBufferRef
}

func setupDecoder(ctx *transcodingCtx) error {
	var err error
	ctx.decFmt, err = NewContextForInput()
	if err != nil {
		return fmt.Errorf("failed to open input context: %v", err)
	}

	options := NewDictionary()
	defer options.Free()

	// only for yuv
	options.Set("pixel_format", "yuv420p10le")
	options.Set("video_size", "3840x2160")

	if err := ctx.decFmt.OpenInput(ctx.config.InputFile, nil, options); err != nil {
		return fmt.Errorf("failed to open input file: %v", err)
	}

	if err := ctx.decFmt.FindStreamInfo(nil); err != nil {
		return fmt.Errorf("could not find stream info: %v", err)
	}

	ctx.decFmt.Dump(0, ctx.config.InputFile, false)
	ctx.decStream = ctx.decFmt.Streams()[0]

	codecID := int(ctx.decStream.CAVStream.codecpar.codec_id)
	codec := NewCodecFromC(unsafe.Pointer(C.avcodec_find_decoder((C.enum_AVCodecID)(codecID))))

	if ctx.decCodec, err = NewContextWithCodec(codec); err != nil {
		return fmt.Errorf("failed to create codec context: %v", err)
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

func setupEncoder(ctx *transcodingCtx) error {
	var err error

	codec := FindEncoderByName(ctx.config.Codec)
	if codec == nil {
		log.Fatalf("Could not find encoder %s", ctx.config.Codec)
		return fmt.Errorf("encoder not found")
	}

	// --- 2) Build options (NVENC-friendly; drop libvpx ones)
	params := map[string]string{
		// NVENC options
		"preset":         "p5",  // p1..p7 (p5 good balance)
		"tune":           "ull", // ultra-low-latency
		"rc":             "cbr", // cbr|vbr|constqp
		"zerolatency":    "1",
		"repeat_headers": "1",
		"aud":            "1",

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
		return fmt.Errorf("could not initiate hwframe context")
	}
	ctx.hwframeCtx = hwframeCtx
	ctx.encCodec.CAVCodecContext.hw_frames_ctx = C.av_buffer_ref(hwframeCtx)
	ctx.swsCtx = C.sws_getContext(C.int(ctx.srcW), C.int(ctx.srcH), ctx.decCodec.CAVCodecContext.pix_fmt,
		C.int(ctx.srcW), C.int(ctx.srcH), framesCtx.sw_format,
		C.SWS_BILINEAR, nil, nil, nil)

	// --- 5) Basic fields / time base / fps
	ctx.encCodec.CAVCodecContext.width = C.int(ctx.config.TargetW)
	ctx.encCodec.CAVCodecContext.height = C.int(ctx.config.TargetH)
	ctx.encCodec.SetTimeBase(NewRational(1, ctx.config.EncoderFrameRate))
	ctx.encCodec.SetFrameRate(NewRational(ctx.config.EncoderFrameRate, 1))

	// Low-latency: no B-frames
	ctx.encCodec.CAVCodecContext.max_b_frames = 0
	ctx.encCodec.CAVCodecContext.gop_size = C.int(ctx.config.GoPSize)
	ctx.encCodec.CAVCodecContext.bit_rate = C.long(ctx.config.InitialBitrate)
	ctx.frameBitrate = ctx.config.InitialBitrate
	ctx.encCodec.CAVCodecContext.pix_fmt = C.AV_PIX_FMT_CUDA

	// --- 6) Apply encoder options

	options := NewDictionary()
	defer options.Free()
	// Only pass actual encoder private opts through the OptionAccessor/Dictionary
	for k, v := range params {
		if err = options.Set(k, v); err != nil {
			fmt.Println(err.Error())
		}
	}

	if err := ctx.encCodec.OpenWithCodec(codec, options); err != nil {
		return fmt.Errorf("OpenWithCodec: %w", err)
	}

	ctx.encFmt = &FormatContext{}
	if C.avformat_alloc_output_context2(&ctx.encFmt.CAVFormatContext, nil, C.CString("mp4"), C.CString(ctx.config.MP4OutputFile)) < 0 {
		return fmt.Errorf("alloc_output_context2 failed")
	}

	if ctx.config.saveMP4 {
		if C.avio_open(&ctx.encFmt.CAVFormatContext.pb, C.CString(ctx.config.MP4OutputFile), C.AVIO_FLAG_WRITE) < 0 {
			return fmt.Errorf("avio_open failed")
		}
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

	if ctx.config.saveMP4 {
		C.av_dump_format(ctx.encFmt.CAVFormatContext, 0, C.CString(ctx.config.MP4OutputFile), 1)

		if C.avformat_write_header(ctx.encFmt.CAVFormatContext, nil) < 0 {
			return fmt.Errorf("avformat_write_header failed")
		}
	}

	return nil
}

// returns false if EOF reached otherwise true
func decodeStream(ctx *transcodingCtx) bool {
	var err error
	if ctx.decPkt, err = NewPacket(); err != nil {
		println("unable to allocate new packet for decoding")
	}

	read, err := ctx.decFmt.ReadFrame(ctx.decPkt)
	if err != nil {
		log.Fatalf("Failed to read packet: %v\n", err)
	}
	if !read {
		logger.Debug("Decoder format context read no frame",
			"lastFrameCount", ctx.decFrameCnt,
			"trueFrameCount", ctx.decTrueFrameCnt)
	}
	if read {
		ctx.decFrameCnt += 1
		ctx.decTrueFrameCnt += 1

		logger.Debug("Retrieved frame from decoder's format context",
			"frameCount", ctx.decFrameCnt,
			"trueFrameCount", ctx.decTrueFrameCnt)
	}

	defer ctx.decPkt.Unref()

	var code C.int
	if read {
		if ctx.decPkt.StreamIndex() != ctx.decStream.Index() {
			return true
		}
		ctx.decPkt.RescaleTime(ctx.decStream.TimeBase(), ctx.decCodec.TimeBase())
		cPkt := (*C.AVPacket)(unsafe.Pointer(ctx.decPkt.CAVPacket))
		code = C.avcodec_send_packet(ctx.decCodec.CAVCodecContext, cPkt)
	} else {
		code = C.avcodec_send_packet(ctx.decCodec.CAVCodecContext, nil)
	}

	if int(code) < 0 {
		log.Fatal("error when sending packet to codec")
		return false
	}

	for {
		frame := ctx.decFrame
		cFrame := (*C.AVFrame)(unsafe.Pointer(frame.CAVFrame))

		code = C.avcodec_receive_frame(ctx.decCodec.CAVCodecContext, cFrame)
		if code == C.AVERROR_EOF {
			logger.Debug("receive_frame returned AVERROR_EOF",
				"frameCount", ctx.decFrameCnt)
			return ctx.handleReadingStopped()
		}
		if code == AVERROR(C.EAGAIN) {
			logger.Debug("Decoder needs more input packets (EAGAIN)")
			break
		} else if int(code) < 0 {
			log.Fatal("error when receiving frame in the decoder")
			return false
		}

		if logger.Enabled(context.TODO(), slog.LevelDebug) {
			printRawFrameHash(ctx, frame)
		}

		C.sws_scale(ctx.swsCtx,
			(**C.uint8_t)(&frame.CAVFrame.data[0]), (*C.int)(&frame.CAVFrame.linesize[0]),
			0, C.int(ctx.srcH),
			(**C.uint8_t)(&ctx.swNV12.CAVFrame.data[0]), (*C.int)(&ctx.swNV12.CAVFrame.linesize[0]),
		)

		hwFrame, err := NewFrame()
		if err != nil {
			panic(err)
		}
		if int(C.av_hwframe_get_buffer(ctx.hwframeCtx, hwFrame.CAVFrame, 0)) < 0 {
			panic("sadfasdf")
		}
		if int(C.av_hwframe_transfer_data(hwFrame.CAVFrame, ctx.swNV12.CAVFrame, 0)) < 0 {
			panic("pashm")
		}

		ctx.decFrameChan <- hwFrame

		frame.Unref()
	}
	return true
}

func printRawFrameHash(ctx *transcodingCtx, frame *Frame) {
	var buf bytes.Buffer
	for i := 0; i < int(frame.CAVFrame.height); i++ {
		lineSize := int(frame.CAVFrame.linesize[0])
		dataPtr := unsafe.Pointer(uintptr(unsafe.Pointer(frame.CAVFrame.data[0])) + uintptr(i*lineSize))
		lineData := C.GoBytes(dataPtr, C.int(frame.CAVFrame.width*2)) // width*2 for 10-bit YUV420P10LE
		buf.Write(lineData)
	}

	hash := md5.Sum(buf.Bytes())
	md5String := hex.EncodeToString(hash[:])
	logger.Debug("Frame MD5",
		"frameCount", ctx.decFrameCnt,
		"trueFrameCount", ctx.decTrueFrameCnt,
		"md5", md5String)
}

func (ctx *transcodingCtx) handleReadingStopped() bool {
	if !ctx.config.LoopVideo {
		close(ctx.decFrameChan)
		return false
	}

	ctx.restartPlayback()
	return true
}

func (ctx *transcodingCtx) restartPlayback() {
	C.av_seek_frame(ctx.decFmt.CAVFormatContext, C.int(ctx.decStream.CAVStream.index), 0, C.AVSEEK_FLAG_BACKWARD)
	C.avcodec_flush_buffers(ctx.decCodec.CAVCodecContext)

	ctx.decTrueFrameCnt = 0
	// Send a nil packet so the encoder's buffer is drained
	ctx.decFrameChan <- nil
}

func getPacketBytes(p *Packet) []byte {
	frameBytes := C.GoBytes(unsafe.Pointer(p.CAVPacket.data), C.int(p.Size()))
	return frameBytes
}

func encodeFrame(ctx *transcodingCtx, frame *Frame, done bool) {
	var data []byte
	var err error

	if frame != nil {
		frame.CAVFrame.pts = C.int64_t(ctx.frameCnt)
		ctx.frameCnt += 1
	}

	if ctx.frameCnt+LOOKAHEAD == ctx.nextKeyFrame && ctx.config.KeyFrameCallback != nil {
		bitrate := ctx.config.KeyFrameCallback()
		ctx.encCodec.CAVCodecContext.bit_rate = C.long(bitrate)
	}

	err = ctx.encCodec.FeedFrame(frame)
	if err != nil {
		fmt.Printf("error in feedframe %v", err)
		return
	}

	if frame != nil {
		frame.Free()
	}

	if ctx.encPkt == nil {
		if ctx.encPkt, err = NewPacket(); err != nil {
			panic(err)
		}
	}
	keyFrame := false
	eofSeen := false
	// countGotten := 0
	lastPts := -1

	push := func(pts int) {
		if len(data) == 0 {
			logger.Debug("Skipped pushing empty access unit",
				"lastAccessUnit", ctx.pktFrameCnt,
				"encoderFrame", ctx.frameCnt)
			return
		}
		ctx.pktFrameCnt += 1

		logger.Debug("Pushing access unit",
			"accessUnit", ctx.pktFrameCnt,
			"pts", pts,
			"size", len(data),
			"encoderFrame", ctx.frameCnt)

		// An encoded frame (access unit) has been detected
		ef := &EncoderFrame{
			KeyFrame: keyFrame,
			Data:     data,
			Bitrate:  ctx.frameBitrate,
		}
		ctx.encFrameChan <- ef
		keyFrame = false
		data = nil
	}

	for {
		ret := ctx.encCodec.GetPacket(ctx.encPkt)
		if ret == int(AVERROR(C.EAGAIN)) {

			logger.Debug("Encoder needs more frames (EAGAIN)")
			break
		} else if ret == 0 {
			ctx.encPkt.SetStreamIndex(ctx.encStream.Index())
			ctx.encPkt.RescaleTime(ctx.encCodec.TimeBase(), ctx.encStream.TimeBase())

			pktPts := int(ctx.encPkt.CAVPacket.pts)
			if lastPts != -1 && pktPts != lastPts {
				push(pktPts)
			}
			lastPts = pktPts

			frameBytes := getPacketBytes(ctx.encPkt)
			isKeyFrame := ctx.encPkt.CAVPacket.flags & C.AV_PKT_FLAG_KEY
			if isKeyFrame != 0 {
				ctx.nextKeyFrame = ctx.frameCnt + ctx.config.GoPSize
				ctx.frameBitrate = int(ctx.encCodec.CAVCodecContext.bit_rate)
				keyFrame = true
			}

			// dropF := func() {
			// 	ctx.droppedFrames += 1
			// }
			saveF := func() {
				if ctx.config.saveHEVC {
					ctx.hevcFile.Write(frameBytes)
				}
			}

			saveF()

			// if len(frameBytes) > 0 {
			// 	if ctx.realFrameCnt >= 60 && keyFrame {
			// 		dropF()
			// 		if keyFrame {
			// 			fmt.Println("Dropping a Keyframe")
			// 		}
			// 	} else {
			// 		if keyFrame {
			// 			// panic("noooooo")
			// 			fmt.Println("Saving a Keyframe")
			// 		}
			// 		saveF()
			// 	}
			// 	ctx.realFrameCnt += 1
			// }

			// this invalidates the encPkt.pts so future references to encPkt.pts should be forbidden
			if ctx.config.saveMP4 {
				C.av_interleaved_write_frame(ctx.encFmt.CAVFormatContext, ctx.encPkt.CAVPacket)
			}

			// countGotten += 1
			// fmt.Printf("frame: %v countGotten: %d  pts: %d\n", ctx.frameCnt, countGotten, pktPts)

			// debug
			// fmt.Printf("frame: %v \n", ctx.frameCnt)
			// PrintHEVCNALs(frameBytes)

			data = append(data, frameBytes...)
			ctx.encPkt.Unref()
		} else if ret == int(C.AVERROR_EOF) {

			logger.Debug("Encoder reached EOF")
			eofSeen = true
			break
		} else {

			logger.Debug("Encoder error receiving packet", "returnCode", ret)
			break
		}
	}

	if done {
		if ctx.config.saveHEVC {
			ctx.hevcFile.Close()
		}

		if ctx.config.saveMP4 {
			C.av_write_trailer(ctx.encFmt.CAVFormatContext)

			if ctx.encFmt.CAVFormatContext.pb != nil {
				C.avio_closep(&ctx.encFmt.CAVFormatContext.pb)
			}

			C.avformat_free_context(ctx.encFmt.CAVFormatContext)
		}
	}

	// use lastPts instead of using
	push(lastPts)

	if eofSeen {
		if !ctx.config.LoopVideo {
			close(ctx.encFrameChan)
		} else {
			logger.Debug("Flushing encoder's buffer for video loop")
			C.avcodec_flush_buffers(ctx.encCodec.CAVCodecContext)
		}
	}
}

func AVERROR(e int) C.int {
	return -C.int(e)
}

// SetLogLevel configures the package logger with the specified level
func SetLogLevel(level slog.Level) {
	logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func setup(ctx *transcodingCtx) error {
	ctx.decFrameChan = make(chan *Frame, FRAME_BUFFER)
	ctx.encFrameChan = make(chan *EncoderFrame, FRAME_BUFFER)

	ctx.frameCnt = 0
	ctx.pktFrameCnt = 0
	ctx.decFrameCnt = 0
	ctx.decTrueFrameCnt = 0

	if ctx.config.saveHEVC {
		var err error
		ctx.hevcFile, err = os.Create(ctx.config.HEVCOutputFile)
		if err != nil {
			return err
		}
	}

	if err := setupDecoder(ctx); err != nil {
		return err
	}
	if err := setupEncoder(ctx); err != nil {
		return err
	}
	return nil
}

type FrameServingContext struct {
	tCtx *transcodingCtx
}

func (fsCtx *FrameServingContext) Init(cfg *Config) {
	// Configure logger if log level is set
	if cfg.LogLevel != 0 {
		SetLogLevel(cfg.LogLevel)
	}

	if cfg.HEVCOutputFile != "" {
		cfg.saveHEVC = true
	}
	if cfg.MP4OutputFile != "" {
		cfg.saveMP4 = true
	}

	tctx := &transcodingCtx{
		config:       cfg,
		nextKeyFrame: -1,
	}
	fsCtx.tCtx = tctx

	if err := setup(tctx); err != nil {
		panic(err)
	}

	go decodingRoutine(fsCtx.tCtx)
	go encodeRoutine(fsCtx.tCtx)
}

func (fsCtx *FrameServingContext) GetNextFrame() *EncoderFrame {
	for ef := range fsCtx.tCtx.encFrameChan {
		if len(ef.Data) == 0 {
			continue
		}
		return ef
	}

	logger.Debug("Encoding has ended")
	return &EncoderFrame{
		Data: nil,
	}
}

func decodingRoutine(ctx *transcodingCtx) {
	for {
		running := decodeStream(ctx)
		if !running {
			break
		}
	}
}

func encodeRoutine(ctx *transcodingCtx) {
	for frame := range ctx.decFrameChan {
		encodeFrame(ctx, frame, false)
	}
	encodeFrame(ctx, nil, true)
}
