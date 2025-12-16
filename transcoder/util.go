package transcoder

/*
#include <libavutil/avutil.h>
#include <libavcodec/avcodec.h>
#include <libavutil/avutil.h>
#include <libavcodec/avcodec.h>
#include <libavutil/avstring.h>
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/avutil.h>
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


static const AVStream *go_av_streams_get(const AVStream **streams, unsigned int n)
{
  return streams[n];
}

#cgo pkg-config: libavcodec libavutil libavformat libavcodec libavfilter
*/
import (
	"C"
)

import (
	"errors"
	"unsafe"
)

type Dictionary struct {
	CAVDictionary  **C.AVDictionary
	pCAVDictionary *C.AVDictionary
}

type ErrorCode int

func NewDictionary() *Dictionary {
	return NewDictionaryFromC(nil)
}

func NewDictionaryFromC(cDictionary unsafe.Pointer) *Dictionary {
	return &Dictionary{CAVDictionary: (**C.AVDictionary)(cDictionary)}
}

func (dict *Dictionary) set(key, value string, flags C.int) error {
	cKey := C.CString(key)
	defer C.free(unsafe.Pointer(cKey))
	cValue := C.CString(value)
	defer C.free(unsafe.Pointer(cValue))
	code := C.av_dict_set(dict.pointer(), cKey, cValue, flags)
	if code < 0 {
		return NewErrorFromCode(ErrorCode(code))
	}
	return nil
}

func (dict *Dictionary) Set(key, value string) error {
	return dict.set(key, value, C.AV_DICT_MATCH_CASE)
}

func (dict *Dictionary) Free() {
	C.av_dict_free(dict.pointer())
}

func (dict *Dictionary) Pointer() unsafe.Pointer {
	return unsafe.Pointer(dict.pointer())
}

func (dict *Dictionary) pointer() **C.AVDictionary {
	if dict.CAVDictionary != nil {
		return dict.CAVDictionary
	}
	return &dict.pCAVDictionary
}

type FormatContext struct {
	CAVFormatContext *C.AVFormatContext
}

func (ctx *FormatContext) NewStreamWithCodec(codec *Codec) (*Stream, error) {
	var cCodec *C.AVCodec
	if codec != nil {
		cCodec = (*C.AVCodec)(unsafe.Pointer(codec.CAVCodec))
	}
	cStream := C.avformat_new_stream(ctx.CAVFormatContext, cCodec)
	if cStream == nil {
		return nil, ErrAllocationError
	}
	return NewStreamFromC(unsafe.Pointer(cStream)), nil
}

func (ctx *FormatContext) OpenInput(fileName string, input *Input, options *Dictionary) error {
	cFileName := C.CString(fileName)
	defer C.free(unsafe.Pointer(cFileName))
	var cInput *C.AVInputFormat
	if input != nil {
		cInput = input.CAVInputFormat
	}
	var cOptions **C.AVDictionary
	if options != nil {
		cOptions = (**C.AVDictionary)(options.Pointer())
	}
	code := C.avformat_open_input(&ctx.CAVFormatContext, cFileName, cInput, cOptions)
	if code < 0 {
		return NewErrorFromCode(ErrorCode(code))
	}
	return nil
}

func (ctx *FormatContext) NumberOfStreams() uint {
	return uint(ctx.CAVFormatContext.nb_streams)
}

func (ctx *FormatContext) Streams() []*Stream {
	count := ctx.NumberOfStreams()
	if count <= 0 {
		return nil
	}
	streams := make([]*Stream, 0, count)
	for i := uint(0); i < count; i++ {
		cStream := C.go_av_streams_get(ctx.CAVFormatContext.streams, C.uint(i))
		stream := NewStreamFromC(unsafe.Pointer(cStream))
		streams = append(streams, stream)
	}
	return streams
}

func (ctx *FormatContext) FindStreamInfo(options []*Dictionary) error {
	code := C.avformat_find_stream_info(ctx.CAVFormatContext, nil)
	if code < 0 {
		return NewErrorFromCode(ErrorCode(code))
	}
	return nil
}

func (ctx *FormatContext) Dump(streamIndex int, url string, isOutput bool) {
	var cIsOutput C.int
	if isOutput {
		cIsOutput = C.int(1)
	}
	cURL := C.CString(url)
	defer C.free(unsafe.Pointer(cURL))
	C.av_dump_format(ctx.CAVFormatContext, C.int(streamIndex), cURL, cIsOutput)
}

// func (ctx *FormatContext) FindBestStream(codec *Codec) int {
// 	return int(C.av_find_best_stream(ctx.CAVFormatContext, int32(MediaTypeVideo), -1, -1, &(codec.CAVCodec), 0))
// }

func (ctx *FormatContext) ReadFrame(pkt *Packet) (bool, error) {
	cPkt := (*C.AVPacket)(unsafe.Pointer(pkt.CAVPacket))
	code := C.av_read_frame(ctx.CAVFormatContext, cPkt)
	if code < 0 {
		if ErrorCode(code) == ErrorCodeEOF {
			return false, nil
		}
		return false, NewErrorFromCode(ErrorCode(code))
	}
	return true, nil
}

func NewContextForInput() (*FormatContext, error) {
	cCtx := C.avformat_alloc_context()
	if cCtx == nil {
		return nil, ErrAllocationError
	}
	return NewFormatContextFromC(unsafe.Pointer(cCtx)), nil
}

func NewFormatContextFromC(cCtx unsafe.Pointer) *FormatContext {
	return &FormatContext{CAVFormatContext: (*C.AVFormatContext)(cCtx)}
}

func NewContextForOutput(output *Output) (*FormatContext, error) {
	var cCtx *C.AVFormatContext
	code := C.avformat_alloc_output_context2(&cCtx, output.CAVOutputFormat, nil, nil)
	if code < 0 {
		return nil, NewErrorFromCode(ErrorCode(code))
	}
	return NewContextFromC(unsafe.Pointer(cCtx)), nil
}

func NewContextFromC(cCtx unsafe.Pointer) *FormatContext {
	return &FormatContext{CAVFormatContext: (*C.AVFormatContext)(cCtx)}
}

func (ctx *FormatContext) Free() {
	if ctx.CAVFormatContext != nil {
		defer C.avformat_free_context(ctx.CAVFormatContext)
		ctx.CAVFormatContext = nil
	}
}

type Stream struct {
	CAVStream *C.AVStream
}

func (s *Stream) SetAverageFrameRate(frameRate *Rational) {
	s.CAVStream.avg_frame_rate.num = (C.int)(frameRate.Numerator())
	s.CAVStream.avg_frame_rate.den = (C.int)(frameRate.Denominator())
}

func (s *Stream) TimeBase() *Rational {
	tb := &s.CAVStream.time_base
	return NewRational(int(tb.num), int(tb.den))
}

func (s *Stream) Index() int {
	return int(s.CAVStream.index)
}

// func (s *Stream) CodecContext() *CodecContext {
// 	if s.CAVStream.codec == nil {
// 		return nil
// 	}
// 	return NewCodecContextFromC(unsafe.Pointer(s.CAVStream.codec))
// }

type CodecContext struct {
	CAVCodecContext *C.AVCodecContext
	*OptionAccessor
}

func (ctx *CodecContext) GetPacket(pkt *Packet) int {
	cPkt := (*C.AVPacket)(unsafe.Pointer(pkt.CAVPacket))
	code := int(C.avcodec_receive_packet(ctx.CAVCodecContext, cPkt))
	if code < 0 {
		// e := NewErrorFromCode(ErrorCode(code))
		// println("the code is ", code, e.code, e.Error())
		// println("Get packet error", code)
	}
	return code
}

func (ctx *CodecContext) FeedFrame(frame *Frame) error {
	var cFrame *C.AVFrame
	if frame != nil {
		cFrame = (*C.AVFrame)(unsafe.Pointer(frame.CAVFrame))
	}
	code := int(C.avcodec_send_frame(ctx.CAVCodecContext, cFrame))
	if code != 0 {
		e := NewErrorFromCode(ErrorCode(code))
		return e
	}
	return nil
}

func (ctx *CodecContext) OpenWithCodec(codec *Codec, options *Dictionary) error {
	var cCodec *C.AVCodec
	if codec != nil {
		cCodec = codec.CAVCodec
	}
	var cOptions **C.AVDictionary
	if options != nil {
		cOptions = (**C.AVDictionary)(options.Pointer())
	}
	code := C.avcodec_open2(ctx.CAVCodecContext, cCodec, cOptions)
	if code < 0 {
		println("Code is ", code)
		return NewErrorFromCode(ErrorCode(code))
	}
	return nil
}

func (ctx *CodecContext) TimeBase() *Rational {
	return NewRationalFromC(unsafe.Pointer(&ctx.CAVCodecContext.time_base))
}

func (ctx *CodecContext) SetTimeBase(timeBase *Rational) {
	ctx.CAVCodecContext.time_base.num = (C.int)(timeBase.Numerator())
	ctx.CAVCodecContext.time_base.den = (C.int)(timeBase.Denominator())
}

func (ctx *CodecContext) CodecType() MediaType {
	return (MediaType)(ctx.CAVCodecContext.codec_type)
}

func (ctx *CodecContext) CopyTo(dst *CodecContext) error {
	// added in lavc 57.33.100
	parameters, err := NewCodecParameters()
	if err != nil {
		return err
	}
	defer parameters.Free()
	cParams := (*C.AVCodecParameters)(unsafe.Pointer(parameters.CAVCodecParameters))
	code := C.avcodec_parameters_from_context(cParams, ctx.CAVCodecContext)
	if code < 0 {
		return NewErrorFromCode(ErrorCode(code))
	}
	code = C.avcodec_parameters_to_context(dst.CAVCodecContext, cParams)
	if code < 0 {
		return NewErrorFromCode(ErrorCode(code))
	}
	return nil
}

func (ctx *CodecContext) SetFrameRate(frameRate *Rational) {
	ctx.CAVCodecContext.framerate.num = (C.int)(frameRate.Numerator())
	ctx.CAVCodecContext.framerate.den = (C.int)(frameRate.Denominator())
}

func (ctx *CodecContext) FrameRate() *Rational {
	return NewRationalFromC(unsafe.Pointer(&ctx.CAVCodecContext.framerate))
}

type Codec struct {
	CAVCodec *C.AVCodec
}

func FindEncoderByName(name string) *Codec {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	cCodec := C.avcodec_find_encoder_by_name(cName)
	if cCodec == nil {
		return nil
	}
	return NewCodecFromC(unsafe.Pointer(cCodec))
}

func FindDecoderByName(name string) *Codec {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	cCodec := C.avcodec_find_decoder_by_name(cName)
	if cCodec == nil {
		return nil
	}
	return NewCodecFromC(unsafe.Pointer(cCodec))
}

func NewCodecFromC(cCodec unsafe.Pointer) *Codec {
	return &Codec{CAVCodec: (*C.AVCodec)(cCodec)}
}

type Rational struct {
	CAVRational C.AVRational
}

func (r *Rational) Numerator() int {
	return int(r.CAVRational.num)
}

func (r *Rational) SetNumerator(numerator int) {
	r.CAVRational.num = (C.int)(numerator)
}

func (r *Rational) Denominator() int {
	return int(r.CAVRational.den)
}

func (r *Rational) SetDenominator(denominator int) {
	r.CAVRational.den = (C.int)(denominator)
}

func NewRational(numerator, denominator int) *Rational {
	r := &Rational{}
	r.CAVRational.num = C.int(numerator)
	r.CAVRational.den = C.int(denominator)
	return r
}

func NewRationalFromC(cRational unsafe.Pointer) *Rational {
	rational := (*C.AVRational)(cRational)
	return NewRational(int(rational.num), int(rational.den))
}

type Packet struct {
	CAVPacket *C.AVPacket
}

func (pkt *Packet) Size() int {
	return int(pkt.CAVPacket.size)
}

func (pkt *Packet) ConsumeData(size int) {
	data := unsafe.Pointer(pkt.CAVPacket.data)
	if data != nil {
		pkt.CAVPacket.size -= C.int(size)
		pkt.CAVPacket.data = (*C.uint8_t)(unsafe.Pointer(uintptr(data) + uintptr(size)))
	}
}

func (pkt *Packet) RescaleTime(srcTimeBase, dstTimeBase *Rational) {
	src := (*C.AVRational)(unsafe.Pointer(&srcTimeBase.CAVRational))
	dst := (*C.AVRational)(unsafe.Pointer(&dstTimeBase.CAVRational))
	C.av_packet_rescale_ts(pkt.CAVPacket, *src, *dst)
}

func (pkt *Packet) Unref() {
	C.av_packet_unref(pkt.CAVPacket)
}

func (pkt *Packet) Free() {
	C.av_packet_free(&pkt.CAVPacket)
}

func (pkt *Packet) StreamIndex() int {
	return int(pkt.CAVPacket.stream_index)
}

func (pkt *Packet) SetStreamIndex(streamIndex int) {
	pkt.CAVPacket.stream_index = (C.int)(streamIndex)
}

func NewPacket() (*Packet, error) {
	cPkt := (*C.AVPacket)(C.av_packet_alloc())
	if cPkt == nil {
		return nil, ErrAllocationError
	}
	return NewPacketFromC(unsafe.Pointer(cPkt)), nil
}

func NewPacketFromC(cPkt unsafe.Pointer) *Packet {
	return &Packet{CAVPacket: (*C.AVPacket)(cPkt)}
}

type Frame struct {
	CAVFrame *C.AVFrame
}

func NewFrame() (*Frame, error) {
	cFrame := C.av_frame_alloc()
	if cFrame == nil {
		return nil, ErrAllocationError
	}
	return NewFrameFromC(unsafe.Pointer(cFrame)), nil
}

func NewFrameFromC(cFrame unsafe.Pointer) *Frame {
	return &Frame{CAVFrame: (*C.AVFrame)(cFrame)}
}

func (f *Frame) Unref() {
	C.av_frame_unref(f.CAVFrame)
}

func (f *Frame) Free() {
	if f.CAVFrame != nil {
		C.av_frame_free(&f.CAVFrame)
	}
}

type FilterContext struct {
	CAVFilterContext *C.AVFilterContext
	*OptionAccessor
}

type IOContext struct {
	CAVIOContext *C.AVIOContext
}

type Graph struct {
	CAVFilterGraph *C.AVFilterGraph
}

var (
	ErrAllocationError     = errors.New("allocation error")
	ErrInvalidArgumentSize = errors.New("invalid argument size")
)

type Input struct {
	CAVInputFormat *C.AVInputFormat
}

func NewStreamFromC(cStream unsafe.Pointer) *Stream {
	return &Stream{CAVStream: (*C.AVStream)(cStream)}
}

func strError(code C.int) string {
	size := C.size_t(256)
	buf := (*C.char)(C.av_mallocz(size))
	defer C.av_free(unsafe.Pointer(buf))
	if C.av_strerror(code, buf, size-1) == 0 {
		return C.GoString(buf)
	}
	return "Unknown error"
}

type Error struct {
	code ErrorCode
	err  error
}

func (e *Error) Code() ErrorCode {
	return e.code
}

func (e *Error) Error() string {
	return e.err.Error()
}

func NewErrorFromCode(code ErrorCode) *Error {
	return &Error{
		code: code,
		err:  errors.New(strError(C.int(code))),
	}
}

type MediaType C.enum_AVMediaType

const (
	MediaTypeUnknown    MediaType = C.AVMEDIA_TYPE_UNKNOWN
	MediaTypeVideo      MediaType = C.AVMEDIA_TYPE_VIDEO
	MediaTypeAudio      MediaType = C.AVMEDIA_TYPE_AUDIO
	MediaTypeData       MediaType = C.AVMEDIA_TYPE_DATA
	MediaTypeSubtitle   MediaType = C.AVMEDIA_TYPE_SUBTITLE
	MediaTypeAttachment MediaType = C.AVMEDIA_TYPE_ATTACHMENT
)

func NewContextWithCodec(codec *Codec) (*CodecContext, error) {
	var cCodec *C.AVCodec
	if codec != nil {
		cCodec = codec.CAVCodec
	}
	cCtx := C.avcodec_alloc_context3(cCodec)
	if cCtx == nil {
		return nil, ErrAllocationError
	}
	return NewCodecContextFromC(unsafe.Pointer(cCtx)), nil
}

func NewCodecContextFromC(cCtx unsafe.Pointer) *CodecContext {
	return &CodecContext{
		CAVCodecContext: (*C.AVCodecContext)(cCtx),
		OptionAccessor:  NewOptionAccessor(cCtx, false),
	}
}

const (
	ErrorCodeBSFNotFound      ErrorCode = -1179861752
	ErrorCodeBug              ErrorCode = -558323010
	ErrorCodeBufferTooSmall   ErrorCode = -1397118274
	ErrorCodeDecoderNotFound  ErrorCode = -1128613112
	ErrorCodeDemuxerNotFound  ErrorCode = -1296385272
	ErrorCodeEncoderNotFound  ErrorCode = -1129203192
	ErrorCodeEOF              ErrorCode = -541478725
	ErrorCodeExit             ErrorCode = -1414092869
	ErrorCodeExternal         ErrorCode = -542398533
	ErrorCodeFilterNotFound   ErrorCode = -1279870712
	ErrorCodeInvalidData      ErrorCode = -1094995529
	ErrorCodeMuxerNotFound    ErrorCode = -1481985528
	ErrorCodeOptionNotFound   ErrorCode = -1414549496
	ErrorCodePatchWelcome     ErrorCode = -1163346256
	ErrorCodeProtocolNotFound ErrorCode = -1330794744
	ErrorCodeStreamNotFound   ErrorCode = -1381258232
	ErrorCodeBug2             ErrorCode = -541545794
	ErrorCodeUnknown          ErrorCode = -1313558101
	ErrorCodeExperimental     ErrorCode = -733130664
	ErrorCodeInputChanged     ErrorCode = -1668179713
	ErrorCodeOutputChanged    ErrorCode = -1668179714
	ErrorCodeHttpBadRequest   ErrorCode = -808465656
	ErrorCodeHttpUnauthorized ErrorCode = -825242872
	ErrorCodeHttpForbidden    ErrorCode = -858797304
	ErrorCodeHttpNotFound     ErrorCode = -875574520
	ErrorCodeHttpOther4xx     ErrorCode = -1482175736
	ErrorCodeHttpServerError  ErrorCode = -1482175992
	NoPTSValue                int64     = -9223372036854775808
)

type CodecParameters struct {
	CAVCodecParameters *C.AVCodecParameters
}

func NewCodecParameters() (*CodecParameters, error) {
	cPkt := (*C.AVCodecParameters)(C.avcodec_parameters_alloc())
	if cPkt == nil {
		return nil, ErrAllocationError
	}
	return NewCodecParametersFromC(unsafe.Pointer(cPkt)), nil
}

func NewCodecParametersFromC(cPSD unsafe.Pointer) *CodecParameters {
	return &CodecParameters{CAVCodecParameters: (*C.AVCodecParameters)(cPSD)}
}

func (cParams *CodecParameters) Free() {
	C.avcodec_parameters_free(&cParams.CAVCodecParameters)
}

type PixelFormat C.enum_AVPixelFormat

const (
	PixelFormatNone PixelFormat = C.AV_PIX_FMT_NONE
)

func FindPixelFormatByName(name string) (PixelFormat, bool) {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	cPixelFormat := C.av_get_pix_fmt(cName)
	return PixelFormat(cPixelFormat), (cPixelFormat != C.AV_PIX_FMT_NONE)
}

func (pfmt PixelFormat) Name() string {
	str, _ := pfmt.NameOk()
	return str
}

func (pfmt PixelFormat) NameOk() (string, bool) {
	return cStringToStringOk(C.av_get_pix_fmt_name((C.enum_AVPixelFormat)(pfmt)))
}

func cStringToStringOk(cStr *C.char) (string, bool) {
	if cStr == nil {
		return "", false
	}
	return C.GoString(cStr), true
}

type Output struct {
	CAVOutputFormat *C.AVOutputFormat
}

func NewOutputFromC(cOutput unsafe.Pointer) *Output {
	return &Output{CAVOutputFormat: (*C.AVOutputFormat)(cOutput)}
}

type OptionSearchFlags int

const (
	OptionSearchChildren OptionSearchFlags = C.AV_OPT_SEARCH_CHILDREN
	OptionSearchFakeObj  OptionSearchFlags = C.AV_OPT_SEARCH_FAKE_OBJ
)

type OptionAccessor struct {
	obj  unsafe.Pointer
	fake bool
}

func NewOptionAccessor(obj unsafe.Pointer, fake bool) *OptionAccessor {
	return &OptionAccessor{obj: obj, fake: fake}
}

func (oa *OptionAccessor) SetOption(name, value string) error {
	return oa.SetOptionWithFlags(name, value, OptionSearchChildren)
}

func (oa *OptionAccessor) SetOptionWithFlags(name, value string, flags OptionSearchFlags) error {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	cValue := C.CString(value)
	defer C.free(unsafe.Pointer(cValue))
	searchFlags := oa.searchFlags(flags)
	code := C.av_opt_set(oa.obj, cName, cValue, searchFlags)
	if code < 0 {
		return NewErrorFromCode(ErrorCode(code))
	}
	return nil
}

func (oa *OptionAccessor) searchFlags(flags OptionSearchFlags) C.int {
	flags &^= OptionSearchFakeObj
	if oa.fake {
		flags |= OptionSearchFakeObj
	}
	return C.int(flags)
}
