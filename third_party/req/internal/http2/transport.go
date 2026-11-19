// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Transport code.

package http2

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/bits"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/textproto"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	clienthpack "github.com/imroc/req/v3/internal/hpack"
	"golang.org/x/net/http/httpguts"
	"golang.org/x/net/http2/hpack"
	"golang.org/x/net/idna"

	"github.com/imroc/req/v3/http2"
	"github.com/imroc/req/v3/internal/ascii"
	"github.com/imroc/req/v3/internal/common"
	"github.com/imroc/req/v3/internal/compress"
	"github.com/imroc/req/v3/internal/dump"
	"github.com/imroc/req/v3/internal/header"
	"github.com/imroc/req/v3/internal/netutil"
	"github.com/imroc/req/v3/internal/transport"
	reqtls "github.com/imroc/req/v3/pkg/tls"
)

const (
	// transportDefaultConnFlow is how many connection-level flow control
	// tokens we give the server at start-up, past the default 64k.
	transportDefaultConnFlow = 1 << 30

	// transportDefaultStreamFlow is how many stream-level flow
	// control tokens we announce to the peer, and how many bytes
	// we buffer per stream.
	transportDefaultStreamFlow = 4 << 20

	// initialMaxConcurrentStreams is a connections maxConcurrentStreams until
	// it's received servers initial SETTINGS frame, which corresponds with the
	// spec's minimum recommended value.
	initialMaxConcurrentStreams = 100

	// defaultMaxConcurrentStreams is a connections default maxConcurrentStreams
	// if the server doesn't include one in its initial SETTINGS frame.
	defaultMaxConcurrentStreams = 1000
)

// Transport is an HTTP/2 Transport.
//
// A Transport internally caches connections to servers. It is safe
// for concurrent use by multiple goroutines.
type Transport struct {
	*transport.Options

	// DialTLS specifies an optional dial function for creating
	// TLS connections for requests.
	//
	// If DialTLS is nil, tls.Dial is used.
	//
	// If the returned net.Conn has a ConnectionState method like tls.Conn,
	// it will be used to set http.Response.TLS.
	DialTLS func(network, addr string, cfg *tls.Config) (net.Conn, error)

	// ConnPool optionally specifies an alternate connection pool to use.
	// If nil, the default is used.
	ConnPool ClientConnPool

	// AllowHTTP, if true, permits HTTP/2 requests using the insecure,
	// plain-text "http" scheme. Note that this does not enable h2c support.
	AllowHTTP bool

	// MaxHeaderListSize is the http2 SETTINGS_MAX_HEADER_LIST_SIZE to
	// send in the initial settings frame. It is how many bytes
	// of response headers are allowed. Unlike the http2 spec, zero here
	// means to use a default limit (currently 10MB). If you actually
	// want to advertise an unlimited value to the peer, Transport
	// interprets the highest possible value here (0xffffffff or 1<<32-1)
	// to mean no limit.
	MaxHeaderListSize uint32

	// StrictMaxConcurrentStreams controls whether the server's
	// SETTINGS_MAX_CONCURRENT_STREAMS should be respected
	// globally. If false, new TCP connections are created to the
	// server as needed to keep each under the per-connection
	// SETTINGS_MAX_CONCURRENT_STREAMS limit. If true, the
	// server's SETTINGS_MAX_CONCURRENT_STREAMS is interpreted as
	// a global limit and callers of RoundTrip block when needed,
	// waiting for their turn.
	StrictMaxConcurrentStreams bool

	// IdleConnTimeout is the maximum amount of time an idle
	// (keep-alive) connection will remain idle before closing
	// itself.
	// Zero means no limit.
	IdleConnTimeout time.Duration

	// ReadIdleTimeout is the timeout after which a health check using ping
	// frame will be carried out if no frame is received on the connection.
	// Note that a ping response will is considered a received frame, so if
	// there is no other traffic on the connection, the health check will
	// be performed every ReadIdleTimeout interval.
	// If zero, no health check is performed.
	ReadIdleTimeout time.Duration

	// PingTimeout is the timeout after which the connection will be closed
	// if a response to Ping is not received.
	// Defaults to 15s.
	PingTimeout time.Duration

	// WriteByteTimeout is the timeout after which the connection will be
	// closed no data can be written to it. The timeout begins when data is
	// available to write, and is extended whenever any bytes are written.
	WriteByteTimeout time.Duration

	// CountError, if non-nil, is called on HTTP/2 transport errors.
	// It's intended to increment a metric for monitoring, such
	// as an expvar or Prometheus metric.
	// The errType consists of only ASCII word characters.
	CountError func(errType string)

	Settings []http2.Setting

	ConnectionFlow uint32
	HeaderPriority http2.PriorityParam
	PriorityFrames []http2.PriorityFrame
	// PathWithoutIndex 仅控制 :path 的 HPACK 表示，不改变其他字段或动态表。
	PathWithoutIndex bool
	// RespectHeaderTableSize 开启后跟随服务端表大小，包括超过默认 4096 的表。
	RespectHeaderTableSize bool
	// IndexOversized 使用 Chromium 的超大字段入表／清表策略。
	IndexOversized bool
	// ChromiumFlowControl 按声明窗口与官方半窗／五秒消费间隔合并接收更新。
	ChromiumFlowControl bool
	// ChromiumRetry 将 REFUSED_STREAM／GOAWAY 的重发限制为两次，不做指数退避。
	ChromiumRetry bool
	// ChromiumUpload 使用连接队列预留窗口并按帧完成顺序安排下一帧。
	ChromiumUpload bool
	// MaxUploadFrameSize 非零时限制上传 DATA 负载，不改变 HEADERS 或服务端帧上限。
	MaxUploadFrameSize uint32

	connPoolOnce  sync.Once
	connPoolOrDef ClientConnPool // non-nil version of ConnPool
}

// newTimer creates a new time.Timer, or a synthetic timer in tests.
func (t *Transport) newTimer(d time.Duration) timer {
	return timeTimer{time.NewTimer(d)}
}

// afterFunc creates a new time.AfterFunc timer, or a synthetic timer in tests.
func (t *Transport) afterFunc(d time.Duration, f func()) timer {
	return timeTimer{time.AfterFunc(d, f)}
}

func (t *Transport) contextWithTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}

func maxHeaderListSize(size uint32) uint32 {
	if size == 0 {
		return 10 << 20
	}
	if size == 0xffffffff {
		return 0
	}
	return size
}

func (t *Transport) pingTimeout() time.Duration {
	if t.PingTimeout == 0 {
		return 15 * time.Second
	}
	return t.PingTimeout
}

func (t *Transport) connPool() ClientConnPool {
	t.connPoolOnce.Do(t.initConnPool)
	return t.connPoolOrDef
}

func (t *Transport) initConnPool() {
	if t.ConnPool != nil {
		t.connPoolOrDef = t.ConnPool
	} else {
		t.connPoolOrDef = &clientConnPool{t: t}
	}
}

// ClientConn is the state of a single HTTP/2 client connection to an
// HTTP/2 server.
type ClientConn struct {
	t             *Transport
	tconn         net.Conn             // usually TLSConn, except specialized impls
	tlsState      *tls.ConnectionState // nil only for specialized impls
	reused        uint32               // whether conn is being reused; atomic
	singleUse     bool                 // whether being used for a single http.Request
	getConnCalled bool                 // used by clientConnPool

	// readLoop goroutine fields:
	readerDone chan struct{} // closed on error
	readerErr  error         // set before readerDone is closed

	idleTimeout time.Duration // or 0 for never
	idleTimer   timer

	mu                 sync.Mutex // guards following
	cond               *sync.Cond // hold mu; broadcast on flow/closed changes
	flow               outflow    // our conn-level flow control quota (cs.outflow is per stream)
	uploadQueue        []uploadWrite
	uploadStalled      []*queuedUpload
	uploadWriting      bool
	uploadWriteOwner   bool
	uploadWriteYielded bool
	uploadTaskRunning  bool
	uploadApplying     bool
	uploadReading      bool
	uploadReader       *uploadBatchReader
	uploadEvents       []uploadEvent
	uploadControls     []func() error
	uploadControlCount int
	uploadOpening      int    // 已占并发槽位但还未分配编号的请求
	uploadDrainErr     error  // 非空时仅排空在途写和会话控制帧
	inflow             inflow // peer's conn-level flow control
	doNotReuse         bool   // whether conn is marked to not be reused for any future requests
	closing            bool
	closed             bool
	seenSettings       bool                     // true if we've seen a settings frame, false otherwise
	wantSettingsAck    bool                     // we sent a SETTINGS frame and haven't heard back
	goAway             *GoAwayFrame             // if non-nil, the GoAwayFrame we received
	goAwayDebug        string                   // goAway frame's debug data, retained as a string
	streams            map[uint32]*clientStream // client-initiated
	streamsReserved    int                      // incr by ReserveNewRequest; decr on RoundTrip
	nextStreamID       uint32
	pendingRequests    int                       // requests blocked and waiting to be sent because len(streams) == maxConcurrentStreams
	pings              map[[8]byte]chan struct{} // in flight ping data to notification channel
	br                 *bufio.Reader
	lastActive         time.Time
	lastIdle           time.Time // time last idle
	// Settings from peer: ChromiumUpload 只由 mu 保护；原路径同时由 wmu 保护。
	maxFrameSize          uint32
	maxConcurrentStreams  uint32
	peerMaxHeaderListSize uint64
	initialWindowSize     uint32

	// reqHeaderMu is a 1-element semaphore channel controlling access to sending new requests.
	// Write to reqHeaderMu to lock it, read from it to unlock.
	// Lock reqmu BEFORE mu or wmu.
	reqHeaderMu chan struct{}

	// wmu is held while writing.
	// Acquire BEFORE mu when holding both, to avoid blocking mu on network writes.
	// Only acquire both at the same time when changing peer settings.
	wmu  sync.Mutex
	bw   *bufio.Writer
	fr   *Framer
	werr error        // first write error that has occurred
	hbuf bytes.Buffer // HPACK encoder writes into this
	henc *clienthpack.Encoder
}

// clientStream is the state for a single HTTP/2 stream. One of these
// is created for each Transport.RoundTrip call.
type clientStream struct {
	currentRequest *http.Request
	cc             *ClientConn

	// Fields of Request that we may access even after the response body is closed.
	ctx       context.Context
	reqCancel <-chan struct{}

	trace               *httptrace.ClientTrace // or nil
	ID                  uint32
	bufPipe             pipe // buffered pipe with the flow-controlled response payload
	requestedGzip       bool
	isHead              bool
	reservationReleased bool // owned by writeRequest and cleanupWriteRequest

	abortOnce sync.Once
	abort     chan struct{} // closed to signal stream should end immediately
	abortErr  error         // set if abort is closed

	peerClosed chan struct{} // closed when the peer sends an END_STREAM flag
	donec      chan struct{} // closed after the stream is in the closed state
	on100      chan struct{} // buffered; written to if a 100 is received

	respHeaderRecv chan struct{}  // closed when headers are received
	res            *http.Response // set if respHeaderRecv is closed

	upload       *queuedUpload         // guarded by cc.mu
	uploadEnded  bool                  // guarded by cc.mu；连接写循环已写完本端 END_STREAM
	flow         outflow               // guarded by cc.mu
	inflow       inflow                // guarded by cc.mu
	bytesRemain  int64                 // -1 means unknown; owned by transportResponseBody.Read
	readErr      error                 // sticky read error; owned by transportResponseBody.Read
	responseRead *chromiumResponseRead // Chromium 挂起的读取；由 cc.mu 保护

	reqBody              io.ReadCloser
	reqBodyContentLength int64         // -1 means unknown
	reqBodyClosed        chan struct{} // guarded by cc.mu; non-nil on Close, closed when done

	// owned by writeRequest:
	sentEndStream bool // sent an END_STREAM flag to the peer
	sentHeaders   bool
	resetQueued   bool // mu 保护；读取错误与请求清理共用一次 RST

	// owned by clientConnReadLoop:
	firstByte    bool  // got the first response byte
	pastHeaders  bool  // got first MetaHeadersFrame (actual headers)
	pastTrailers bool  // got optional second MetaHeadersFrame (trailers)
	num1xx       uint8 // number of 1xx responses seen
	readClosed   bool  // peer sent an END_STREAM flag
	readAborted  bool  // read loop reset the stream

	trailer    http.Header  // accumulated trailers
	resTrailer *http.Header // client's Response.Trailer
}

var got1xxFuncForTests func(int, textproto.MIMEHeader) error

// get1xxTraceFunc returns the value of request's httptrace.ClientTrace.Got1xxResponse func,
// if any. It returns nil if not set or if the Go version is too old.
func (cs *clientStream) get1xxTraceFunc() func(int, textproto.MIMEHeader) error {
	if fn := got1xxFuncForTests; fn != nil {
		return fn
	}
	return traceGot1xxResponseFunc(cs.trace)
}

func (cs *clientStream) abortStream(err error) {
	cs.cc.mu.Lock()
	defer cs.cc.mu.Unlock()
	cs.abortStreamLocked(err)
}

func (cs *clientStream) abortStreamLocked(err error) {
	cs.abortOnce.Do(func() {
		cs.abortErr = err
		close(cs.abort)
	})
	if cs.responseRead != nil {
		cs.responseRead.finishLocked(0, err)
	}
	if cs.reqBody != nil {
		cs.closeReqBodyLocked()
	}
	// TODO(dneil): Clean up tests where cs.cc.cond is nil.
	if cs.cc.cond != nil {
		// Wake up writeRequestBody if it is waiting on flow control.
		cs.cc.cond.Broadcast()
	}
}

func (cs *clientStream) abortRequestBodyWrite() {
	cc := cs.cc
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cs.reqBody != nil && cs.reqBodyClosed == nil {
		cs.closeReqBodyLocked()
		cc.cond.Broadcast()
	}
}

func (cs *clientStream) closeReqBodyLocked() {
	if u := cs.upload; u != nil {
		err := cs.cc.uploadErrorLocked(u)
		if err == nil {
			err = errStopReqBodyWrite
		}
		cs.cc.finishUploadLocked(u, err)
	}
	if cs.reqBodyClosed != nil {
		return
	}
	cs.reqBodyClosed = make(chan struct{})
	reqBodyClosed := cs.reqBodyClosed
	go func() {
		cs.reqBody.Close()
		close(reqBodyClosed)
	}()
}

type stickyErrWriter struct {
	conn    net.Conn
	timeout time.Duration
	err     *error
}

func (sew stickyErrWriter) Write(p []byte) (n int, err error) {
	if *sew.err != nil {
		return 0, *sew.err
	}
	for {
		if sew.timeout != 0 {
			sew.conn.SetWriteDeadline(time.Now().Add(sew.timeout))
		}
		nn, err := sew.conn.Write(p[n:])
		n += nn
		if n < len(p) && nn > 0 && errors.Is(err, os.ErrDeadlineExceeded) {
			// Keep extending the deadline so long as we're making progress.
			continue
		}
		if sew.timeout != 0 {
			sew.conn.SetWriteDeadline(time.Time{})
		}
		*sew.err = err
		return n, err
	}
}

// noCachedConnError is the concrete type of ErrNoCachedConn, which
// needs to be detected by net/http regardless of whether it's its
// bundled version (in h2_bundle.go with a rewritten type name) or
// from a user's x/net/http2. As such, as it has a unique method name
// (IsHTTP2NoCachedConnError) that net/http sniffs for via func
// IsNoCachedConnError.
type noCachedConnError struct{}

func (noCachedConnError) IsHTTP2NoCachedConnError() {}

func (noCachedConnError) Error() string { return "http2: no cached connection was available" }

// IsNoCachedConnError reports whether err is of type noCachedConnError
// or its equivalent renamed type in net/http2's h2_bundle.go. Both types
// may coexist in the same running program.
func IsNoCachedConnError(err error) bool {
	_, ok := err.(interface{ IsHTTP2NoCachedConnError() })
	return ok
}

var ErrNoCachedConn error = noCachedConnError{}

// RoundTripOpt are options for the Transport.RoundTripOpt method.
type RoundTripOpt struct {
	// OnlyCachedConn controls whether RoundTripOpt may
	// create a new TCP connection. If set true and
	// no cached connection is available, RoundTripOpt
	// will return ErrNoCachedConn.
	OnlyCachedConn bool
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.RoundTripOpt(req, RoundTripOpt{})
}

func (t *Transport) RoundTripOnlyCachedConn(req *http.Request) (*http.Response, error) {
	return t.RoundTripOpt(req, RoundTripOpt{OnlyCachedConn: true})
}

// authorityAddr returns a given authority (a host/IP, or host:port / ip:port)
// and returns a host:port. The port 443 is added if needed.
func authorityAddr(scheme string, authority string) (addr string) {
	host, port, err := net.SplitHostPort(authority)
	if err != nil { // authority didn't have a port
		host = authority
		port = ""
	}
	if port == "" { // authority's port was empty
		port = "443"
		if scheme == "http" {
			port = "80"
		}
	}
	if a, err := idna.ToASCII(host); err == nil {
		host = a
	}
	// IPv6 address literal, without a port:
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return host + ":" + port
	}
	return net.JoinHostPort(host, port)
}

func (t *Transport) AddConn(conn net.Conn, addr string) (used bool, err error) {
	used, err = t.connPool().AddConnIfNeeded(addr, t, conn)
	return
}

// RoundTripOpt is like RoundTrip, but takes options.
func (t *Transport) RoundTripOpt(req *http.Request, opt RoundTripOpt) (*http.Response, error) {
	if t.ChromiumRetry && req.Context().Value(chromiumRetryKey{}) == nil {
		req = t.NewRequestRetryState(req)
	}
	if !(req.URL.Scheme == "https" || (req.URL.Scheme == "http" && t.AllowHTTP)) {
		return nil, errors.New("http2: unsupported scheme")
	}

	addr := netutil.AuthorityAddr(req.URL.Scheme, req.URL.Host)
	var cc *ClientConn
	var err error
	if opt.OnlyCachedConn {
		cc, err = t.connPool().GetClientConn(req, addr, false)
		if err != nil {
			return nil, err
		}
		traceGotConn(req, cc, true)
		return cc.RoundTrip(req)
	}
	for retry := 0; ; retry++ {
		cc, err = t.connPool().GetClientConn(req, addr, true)
		if err != nil {
			t.vlogf("http2: Transport failed to get client conn for %s: %v", addr, err)
			return nil, err
		}
		reused := !atomic.CompareAndSwapUint32(&cc.reused, 0, 1)
		traceGotConn(req, cc, reused)
		res, err := cc.RoundTrip(req)
		if err != nil && retry <= 6 {
			roundTripErr := err
			if t.ChromiumRetry && req.Context().Err() != nil {
				return nil, req.Context().Err()
			}
			if !t.AllowRetry(req, roundTripErr) {
				return nil, roundTripErr
			}
			if req, err = shouldRetryRequest(req, err); err == nil {
				// After the first retry, do exponential backoff with 10% jitter.
				if retry == 0 || (t.ChromiumRetry && chromiumRejection(roundTripErr)) {
					t.vlogf("RoundTrip retrying after failure: %v", roundTripErr)
					continue
				}
				backoff := float64(uint(1) << (uint(retry) - 1))
				backoff += backoff * (0.1 * mathrand.Float64())
				d := time.Second * time.Duration(backoff)
				tm := t.newTimer(d)
				select {
				case <-tm.C():
					t.vlogf("RoundTrip retrying after failure: %v", roundTripErr)
					continue
				case <-req.Context().Done():
					tm.Stop()
					err = req.Context().Err()
				}
			}
		}
		if err != nil {
			t.vlogf("RoundTrip failure: %v", err)
			return nil, err
		}
		return res, nil
	}
}

// CloseIdleConnections closes any connections which were previously
// connected from previous requests but are now sitting idle.
// It does not interrupt any connections currently in use.
func (t *Transport) CloseIdleConnections() {
	t.connPool().CloseIdleConnections()
}

var (
	errClientConnClosed    = errors.New("http2: client conn is closed")
	errClientConnUnusable  = errors.New("http2: client conn not usable")
	errClientConnGotGoAway = errors.New("http2: Transport received Server's graceful shutdown GOAWAY")
)

type chromiumRetryKey struct{}
type chromiumRetryState struct{ retries int }

func chromiumRejection(err error) bool {
	se, streamError := err.(StreamError)
	return err == errClientConnGotGoAway || (streamError && se.Code == ErrCodeRefusedStream)
}

// NewRequestRetryState 为一次外层请求创建预算，缓存连接和后续拨号共享它。
// 预算只随当前请求传递，不与连接池或其他请求共用。
func (t *Transport) NewRequestRetryState(req *http.Request) *http.Request {
	if !t.ChromiumRetry {
		return req
	}
	return req.WithContext(context.WithValue(req.Context(), chromiumRetryKey{}, &chromiumRetryState{}))
}

// AllowRetry 只限制官方样本中的明确拒绝，不把尚未写出请求的连接淘汰算成重发。
func (t *Transport) AllowRetry(req *http.Request, err error) bool {
	if !t.ChromiumRetry {
		return true
	}
	if !chromiumRejection(err) {
		return true
	}
	state := req.Context().Value(chromiumRetryKey{}).(*chromiumRetryState)
	if state.retries >= 2 {
		return false
	}
	state.retries++
	return true
}

// shouldRetryRequest is called by RoundTrip when a request fails to get
// response headers. It is always called with a non-nil error.
// It returns either a request to retry (either the same request, or a
// modified clone), or an error if the request can't be replayed.
func shouldRetryRequest(req *http.Request, err error) (*http.Request, error) {
	if !CanRetryError(err) {
		return nil, err
	}
	// If the Body is nil (or http.NoBody), it's safe to reuse
	// this request and its Body.
	if req.Body == nil || req.Body == http.NoBody {
		return req, nil
	}

	// If the request body can be reset back to its original
	// state via the optional req.GetBody, do that.
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		newReq := *req
		newReq.Body = body
		return &newReq, nil
	}

	// The Request.Body can't reset back to the beginning, but we
	// don't seem to have started to read from it yet, so reuse
	// the request directly.
	if err == errClientConnUnusable {
		return req, nil
	}

	return nil, fmt.Errorf("http2: Transport: cannot retry err [%v] after Request.Body was written; define Request.GetBody to avoid this error", err)
}

func CanRetryError(err error) bool {
	if err == errClientConnUnusable || err == errClientConnGotGoAway {
		return true
	}
	if se, ok := err.(StreamError); ok {
		if se.Code == ErrCodeProtocol && se.Cause == errFromPeer {
			// See golang/go#47635, golang/go#42777
			return true
		}
		return se.Code == ErrCodeRefusedStream
	}
	return false
}

func (t *Transport) dialClientConn(ctx context.Context, addr string, singleUse bool) (*ClientConn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	tconn, err := t.dialTLS(ctx)("tcp", addr, t.newTLSConfig(host))
	if err != nil {
		return nil, err
	}
	return t.newClientConn(tconn, singleUse)
}

func (t *Transport) newTLSConfig(host string) *tls.Config {
	cfg := new(tls.Config)
	if c := t.TLSClientConfig; c != nil {
		*cfg = *c.Clone()
	}
	if !strSliceContains(cfg.NextProtos, NextProtoTLS) {
		cfg.NextProtos = append([]string{NextProtoTLS}, cfg.NextProtos...)
	}
	if cfg.ServerName == "" {
		cfg.ServerName = host
	}
	return cfg
}

var zeroDialer net.Dialer

type tlsHandshakeTimeoutError struct{}

func (tlsHandshakeTimeoutError) Timeout() bool   { return true }
func (tlsHandshakeTimeoutError) Temporary() bool { return true }
func (tlsHandshakeTimeoutError) Error() string   { return "net/http: TLS handshake timeout" }

// dialTCP dials a TCP connection, preferring a custom DialContext when set.
func (t *Transport) dialTCP(ctx context.Context, network, addr string) (net.Conn, error) {
	if t.DialContext != nil {
		c, err := t.DialContext(ctx, network, addr)
		if c == nil && err == nil {
			err = errors.New("net/http: Transport.DialContext hook returned (nil, nil)")
		}
		return c, err
	}
	return zeroDialer.DialContext(ctx, network, addr)
}

// dialTLSWithContext uses tls.Dialer, added in Go 1.15, to open a TLS
// connection. When DialContext is set (e.g. via Client.SetDial / SetResolver /
// SetHosts), the custom dialer is used for the underlying TCP connection.
func (t *Transport) dialTLSWithContext(ctx context.Context, network, addr string, cfg *tls.Config) (reqtls.Conn, error) {
	if t.TLSHandshakeContext != nil {
		conn, err := t.dialTCP(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		var firstTLSHost string
		if firstTLSHost, _, err = net.SplitHostPort(addr); err != nil {
			conn.Close()
			return nil, err
		}
		trace := httptrace.ContextClientTrace(ctx)
		errc := make(chan error, 2)
		var timer *time.Timer // for canceling TLS handshake
		if d := t.TLSHandshakeTimeout; d != 0 {
			timer = time.AfterFunc(d, func() {
				errc <- tlsHandshakeTimeoutError{}
			})
		}
		go func() {
			if trace != nil && trace.TLSHandshakeStart != nil {
				trace.TLSHandshakeStart()
			}
			tlsCn, tlsState, err := t.TLSHandshakeContext(ctx, firstTLSHost, conn)
			if err != nil {
				if timer != nil {
					timer.Stop()
				}
				if trace != nil && trace.TLSHandshakeDone != nil {
					trace.TLSHandshakeDone(tls.ConnectionState{}, err)
				}
			} else {
				conn = tlsCn
				if trace != nil && trace.TLSHandshakeDone != nil {
					trace.TLSHandshakeDone(*tlsState, nil)
				}
			}
			errc <- err
		}()
		if err := <-errc; err != nil {
			conn.Close()
			return nil, err
		} else {
			tlsCn := conn.(reqtls.Conn)
			return tlsCn, nil
		}
	}

	// Custom DialContext: dial TCP first, then perform TLS handshake so
	// SetResolver / SetHosts / SetDial apply to HTTP/2 as well.
	// Handshake timeout / trace handling matches HTTP/1 persistConn.addTLS.
	if t.DialContext != nil {
		conn, err := t.dialTCP(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		tlsCn := tls.Client(conn, cfg)
		trace := httptrace.ContextClientTrace(ctx)
		errc := make(chan error, 2)
		var timer *time.Timer
		if d := t.TLSHandshakeTimeout; d != 0 {
			timer = time.AfterFunc(d, func() {
				errc <- tlsHandshakeTimeoutError{}
			})
		}
		go func() {
			if trace != nil && trace.TLSHandshakeStart != nil {
				trace.TLSHandshakeStart()
			}
			err := tlsCn.HandshakeContext(ctx)
			if timer != nil {
				timer.Stop()
			}
			errc <- err
		}()
		if err := <-errc; err != nil {
			conn.Close()
			if err == (tlsHandshakeTimeoutError{}) {
				// Wait for HandshakeContext to return after close.
				<-errc
			}
			if trace != nil && trace.TLSHandshakeDone != nil {
				trace.TLSHandshakeDone(tls.ConnectionState{}, err)
			}
			return nil, err
		}
		if trace != nil && trace.TLSHandshakeDone != nil {
			trace.TLSHandshakeDone(tlsCn.ConnectionState(), nil)
		}
		return tlsCn, nil
	}

	dialer := &tls.Dialer{
		Config: cfg,
	}
	conn, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	tlsCn := conn.(reqtls.Conn)
	return tlsCn, nil
}

func (t *Transport) dialTLS(ctx context.Context) func(string, string, *tls.Config) (net.Conn, error) {
	if t.DialTLS != nil {
		return t.DialTLS
	}
	if t.DialTLSContext != nil {
		return func(network string, addr string, cfg *tls.Config) (net.Conn, error) {
			return t.DialTLSContext(ctx, network, addr)
		}
	}
	return func(network, addr string, cfg *tls.Config) (net.Conn, error) {
		tlsCn, err := t.dialTLSWithContext(ctx, network, addr, cfg)
		if err != nil {
			return nil, err
		}
		state := tlsCn.ConnectionState()
		if p := state.NegotiatedProtocol; p != NextProtoTLS {
			return nil, fmt.Errorf("http2: unexpected ALPN protocol %q; want %q", p, NextProtoTLS)
		}
		if !state.NegotiatedProtocolIsMutual {
			return nil, errors.New("http2: could not negotiate protocol mutually")
		}
		return tlsCn, nil
	}
}

func (t *Transport) NewClientConn(c net.Conn) (*ClientConn, error) {
	return t.newClientConn(c, t.DisableKeepAlives)
}

func (t *Transport) newClientConn(c net.Conn, singleUse bool) (*ClientConn, error) {
	cc := &ClientConn{
		t:                     t,
		tconn:                 c,
		readerDone:            make(chan struct{}),
		nextStreamID:          1,
		maxFrameSize:          16 << 10,                    // spec default
		initialWindowSize:     65535,                       // spec default
		maxConcurrentStreams:  initialMaxConcurrentStreams, // "infinite", per spec. Use a smaller value until we have received server settings.
		peerMaxHeaderListSize: 0xffffffffffffffff,          // "infinite", per spec. Use 2^64-1 instead.
		streams:               make(map[uint32]*clientStream),
		singleUse:             singleUse,
		wantSettingsAck:       true,
		pings:                 make(map[[8]byte]chan struct{}),
		reqHeaderMu:           make(chan struct{}, 1),
	}
	if VerboseLogs {
		t.vlogf("http2: Transport creating client conn %p to %v", cc, c.RemoteAddr())
	}

	cc.cond = sync.NewCond(&cc.mu)
	if t.ChromiumUpload {
		if pending, ok := c.(interface{ SetWritePendingCallback(func(bool)) }); ok {
			pending.SetWritePendingCallback(cc.uploadWriteState)
		}
	}

	headerListSize := t.MaxHeaderListSize
	var headerTableSize uint32 = initialHeaderTableSize
	var readFrameSize uint32 = minMaxFrameSize
	for _, setting := range t.Settings {
		switch setting.ID {
		case http2.SettingMaxFrameSize:
			readFrameSize = setting.Val
			if !t.ChromiumUpload {
				cc.maxFrameSize = setting.Val
			}
		case http2.SettingMaxHeaderListSize:
			headerListSize = setting.Val
		case http2.SettingHeaderTableSize:
			headerTableSize = setting.Val
		}
	}

	cc.flow.add(initialWindowSize)

	// TODO: adjust this writer size to account for frame size +
	// MTU + crypto/tls record padding.
	cc.bw = bufio.NewWriter(stickyErrWriter{
		conn:    c,
		timeout: t.WriteByteTimeout,
		err:     &cc.werr,
	})
	cc.br = bufio.NewReader(c)
	if t.ChromiumUpload {
		if pending, ok := c.(interface{ SetReadStateCallback(func(bool)) bool }); ok {
			r := &uploadBatchReader{r: c, cc: cc}
			if pending.SetReadStateCallback(r.state) {
				cc.uploadReader = r
				cc.br = bufio.NewReaderSize(r, 8*1024)
			}
		}
	}
	cc.fr = NewFramer(cc.bw, cc.br)
	cc.fr.cc = cc
	if t.ChromiumUpload {
		cc.fr.SetMaxReadFrameSize(readFrameSize)
	}
	if t.CountError != nil {
		cc.fr.countError = t.CountError
	}
	cc.fr.ReadMetaHeaders = hpack.NewDecoder(headerTableSize, nil)
	if t.ChromiumUpload {
		cc.fr.chromiumReadMeta = clienthpack.NewChromiumDecoder(headerTableSize, nil)
	}
	cc.fr.MaxHeaderListSize = maxHeaderListSize(headerListSize)

	cc.henc = clienthpack.NewEncoder(&cc.hbuf)
	cc.henc.IndexOversized = t.IndexOversized

	if cs, ok := c.(connectionStater); ok {
		state := cs.ConnectionState()
		cc.tlsState = &state
	}

	var initialSettings []http2.Setting
	if len(t.Settings) > 0 {
		initialSettings = t.Settings
	} else {
		initialSettings = []http2.Setting{
			{ID: http2.SettingEnablePush, Val: 0},
			{ID: http2.SettingInitialWindowSize, Val: transportDefaultStreamFlow},
		}
		if max := cc.fr.MaxHeaderListSize; max != 0 {
			initialSettings = append(initialSettings, http2.Setting{ID: http2.SettingMaxHeaderListSize, Val: max})
		}
	}

	cc.bw.Write(clientPreface)
	cc.fr.WriteSettings(initialSettings...)
	connFlow := cc.t.ConnectionFlow
	if connFlow < 1 {
		connFlow = transportDefaultConnFlow
	}
	cc.fr.WriteWindowUpdate(0, connFlow)

	for _, p := range t.PriorityFrames {
		cc.fr.WritePriority(p.StreamID, p.PriorityParam)
		cc.nextStreamID = p.StreamID + 2
	}

	cc.inflow.init(int32(connFlow) + initialWindowSize)
	if t.ChromiumFlowControl {
		cc.inflow.initChromium(int32(connFlow) + initialWindowSize)
	}
	cc.bw.Flush()
	if cc.werr != nil {
		cc.Close()
		return nil, cc.werr
	}

	// Start the idle timer after the connection is fully initialized.
	if d := t.IdleConnTimeout; d != 0 {
		cc.idleTimeout = d
		cc.idleTimer = t.afterFunc(d, cc.onIdleTimeout)
	}

	go cc.readLoop()
	return cc, nil
}

func (cc *ClientConn) healthCheck() {
	pingTimeout := cc.t.pingTimeout()
	// We don't need to periodically ping in the health check, because the readLoop of ClientConn will
	// trigger the healthCheck again if there is no frame received.
	ctx, cancel := cc.t.contextWithTimeout(context.Background(), pingTimeout)
	defer cancel()
	cc.vlogf("http2: Transport sending health check")
	err := cc.Ping(ctx)
	if err != nil {
		cc.vlogf("http2: Transport health check failure: %v", err)
		cc.closeForLostPing()
		return
	} else {
		cc.vlogf("http2: Transport health check success")
	}
}

// SetDoNotReuse marks cc as not reusable for future HTTP requests.
func (cc *ClientConn) SetDoNotReuse() {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.doNotReuse = true
}

func (cc *ClientConn) setGoAway(f *GoAwayFrame) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	old := cc.goAway
	cc.goAway = f

	// Merge the previous and current GoAway error frames.
	if cc.goAwayDebug == "" {
		cc.goAwayDebug = string(f.DebugData())
	}
	if old != nil && old.ErrCode != ErrCodeNo {
		cc.goAway.ErrCode = old.ErrCode
	}
	if cc.t.ChromiumUpload {
		cc.discardQueuedUploadHeadersLocked(errClientConnGotGoAway, true)
		cc.cond.Broadcast()
	}
	last := f.LastStreamID
	for streamID, cs := range cc.streams {
		if streamID <= last {
			// The server's GOAWAY indicates that it received this stream.
			// It will either finish processing it, or close the connection
			// without doing so. Either way, leave the stream alone for now.
			continue
		}
		if streamID == 1 && cc.goAway.ErrCode != ErrCodeNo {
			// Don't retry the first stream on a connection if we get a non-NO error.
			// If the server is sending an error on a new connection,
			// retrying the request on a new one probably isn't going to work.
			cs.abortStreamLocked(fmt.Errorf("http2: Transport received GOAWAY from server ErrCode:%v", cc.goAway.ErrCode))
		} else {
			// Aborting the stream with errClentConnGotGoAway indicates that
			// the request should be retried on a new connection.
			cs.abortStreamLocked(errClientConnGotGoAway)
		}
	}
}

// CanTakeNewRequest reports whether the connection can take a new request,
// meaning it has not been closed or received or sent a GOAWAY.
//
// If the caller is going to immediately make a new request on this
// connection, use ReserveNewRequest instead.
func (cc *ClientConn) CanTakeNewRequest() bool {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return cc.canTakeNewRequestLocked()
}

// ReserveNewRequest is like CanTakeNewRequest but also reserves a
// concurrent stream in cc. The reservation is decremented on the
// next call to RoundTrip.
func (cc *ClientConn) ReserveNewRequest() bool {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if st := cc.idleStateLocked(); !st.canTakeNewRequest {
		return false
	}
	cc.streamsReserved++
	return true
}

// ClientConnState describes the state of a ClientConn.
type ClientConnState struct {
	// Closed is whether the connection is closed.
	Closed bool

	// Closing is whether the connection is in the process of
	// closing. It may be closing due to shutdown, being a
	// single-use connection, being marked as DoNotReuse, or
	// having received a GOAWAY frame.
	Closing bool

	// StreamsActive is how many streams are active.
	StreamsActive int

	// StreamsReserved is how many streams have been reserved via
	// ClientConn.ReserveNewRequest.
	StreamsReserved int

	// StreamsPending is how many requests have been sent in excess
	// of the peer's advertised MaxConcurrentStreams setting and
	// are waiting for other streams to complete.
	StreamsPending int

	// MaxConcurrentStreams is how many concurrent streams the
	// peer advertised as acceptable. Zero means no SETTINGS
	// frame has been received yet.
	MaxConcurrentStreams uint32

	// LastIdle, if non-zero, is when the connection last
	// transitioned to idle state.
	LastIdle time.Time
}

// clientConnIdleState describes the suitability of a client
// connection to initiate a new RoundTrip request.
type clientConnIdleState struct {
	canTakeNewRequest bool
}

func (cc *ClientConn) idleState() clientConnIdleState {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return cc.idleStateLocked()
}

func (cc *ClientConn) idleStateLocked() (st clientConnIdleState) {
	if cc.singleUse && (cc.nextStreamID > 1 || cc.uploadOpening != 0) {
		return
	}
	var maxConcurrentOkay bool
	if cc.t.StrictMaxConcurrentStreams {
		// We'll tell the caller we can take a new request to
		// prevent the caller from dialing a new TCP
		// connection, but then we'll block later before
		// writing it.
		maxConcurrentOkay = true
	} else {
		maxConcurrentOkay = int64(len(cc.streams)+cc.uploadOpening+cc.streamsReserved+1) <= int64(cc.maxConcurrentStreams)
	}

	st.canTakeNewRequest = cc.goAway == nil && !cc.closed && !cc.closing && maxConcurrentOkay &&
		!cc.doNotReuse &&
		int64(cc.nextStreamID)+2*int64(cc.pendingRequests+cc.uploadOpening) < math.MaxInt32 &&
		!cc.tooIdleLocked()
	return
}

func (cc *ClientConn) canTakeNewRequestLocked() bool {
	st := cc.idleStateLocked()
	return st.canTakeNewRequest
}

// tooIdleLocked reports whether this connection has been sitting idle
// for too much wall time.
func (cc *ClientConn) tooIdleLocked() bool {
	// The Round(0) strips the monotonic clock reading so the
	// times are compared based on their wall time. We don't want
	// to reuse a connection that's been sitting idle during
	// VM/laptop suspend if monotonic time was also frozen.
	return cc.idleTimeout != 0 && !cc.lastIdle.IsZero() && time.Since(cc.lastIdle.Round(0)) > cc.idleTimeout
}

// onIdleTimeout is called from a time.AfterFunc goroutine. It will
// only be called when we're idle, but because we're coming from a new
// goroutine, there could be a new request coming in at the same time,
// so this simply calls the synchronized closeIfIdle to shut down this
// connection. The timer could just call closeIfIdle, but this is more
// clear.
func (cc *ClientConn) onIdleTimeout() {
	cc.closeIfIdle()
}

func (cc *ClientConn) closeConn() {
	t := time.AfterFunc(250*time.Millisecond, cc.forceCloseConn)
	defer t.Stop()
	cc.tconn.Close()
}

// A tls.Conn.Close can hang for a long time if the peer is unresponsive.
// Try to shut it down more aggressively.
func (cc *ClientConn) forceCloseConn() {
	tc, ok := cc.tconn.(*tls.Conn)
	if !ok {
		return
	}
	if nc := tc.NetConn(); nc != nil {
		nc.Close()
	}
}

func (cc *ClientConn) closeIfIdle() {
	cc.mu.Lock()
	if cc.closed || len(cc.streams) > 0 || cc.streamsReserved > 0 || cc.uploadOpening != 0 || cc.uploadDrainErr != nil {
		cc.mu.Unlock()
		return
	}
	if cc.uploadWriting || len(cc.uploadEvents) > 0 || cc.uploadTaskRunning {
		if !cc.closing {
			// 空闲请求已结束，但 ACK／取消帧等任务尚未排空。
			// 先禁止新请求复用，再异步等待；回收调用不占住写循环。
			cc.closing = true
			cc.cond.Broadcast()
			go cc.closeIdleAfterUploadTasks()
		}
		cc.mu.Unlock()
		return
	}
	cc.closed = true
	nextID := cc.nextStreamID
	// TODO: do clients send GOAWAY too? maybe? Just Close:
	cc.mu.Unlock()

	if VerboseLogs {
		cc.vlogf("http2: Transport closing idle conn %p (forSingleUse=%v, maxStream=%v)", cc, cc.singleUse, nextID-2)
	}
	cc.closeConn()
}

func (cc *ClientConn) closeIdleAfterUploadTasks() {
	cc.mu.Lock()
	for (cc.uploadWriting || len(cc.uploadEvents) > 0 || cc.uploadTaskRunning) && !cc.closed && cc.uploadDrainErr == nil {
		cc.cond.Wait()
	}
	cc.mu.Unlock()
	cc.closeIfIdle()
}

func (cc *ClientConn) isDoNotReuseAndIdle() bool {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return cc.doNotReuse && len(cc.streams) == 0 && cc.uploadOpening == 0
}

var shutdownEnterWaitStateHook = func() {}

// Shutdown gracefully closes the client connection, waiting for running streams to complete.
func (cc *ClientConn) Shutdown(ctx context.Context) error {
	if err := cc.sendGoAway(); err != nil {
		return err
	}
	// Wait for all in-flight streams to complete or connection to close
	done := make(chan struct{})
	cancelled := false // guarded by cc.mu
	go func() {
		cc.mu.Lock()
		defer cc.mu.Unlock()
		for {
			if cc.closed || len(cc.streams) == 0 && cc.uploadOpening == 0 && !cc.uploadWriting && len(cc.uploadEvents) == 0 && !cc.uploadTaskRunning {
				cc.closed = true
				close(done)
				break
			}
			if cancelled {
				break
			}
			cc.cond.Wait()
		}
	}()
	shutdownEnterWaitStateHook()
	select {
	case <-done:
		cc.closeConn()
		return nil
	case <-ctx.Done():
		cc.mu.Lock()
		// Free the goroutine above
		cancelled = true
		cc.cond.Broadcast()
		cc.mu.Unlock()
		return ctx.Err()
	}
}

func (cc *ClientConn) sendGoAway() error {
	cc.mu.Lock()
	closing := cc.closing
	cc.closing = true
	if cc.t.ChromiumUpload {
		// Chromium 正常关闭不发送 GOAWAY，已开启的流继续完成。
		cc.discardQueuedUploadHeadersLocked(errClientConnUnusable, true)
		cc.cond.Broadcast()
		cc.mu.Unlock()
		return nil
	}
	maxStreamID := cc.nextStreamID
	cc.mu.Unlock()
	if closing {
		// GOAWAY sent already
		return nil
	}

	cc.wmu.Lock()
	defer cc.wmu.Unlock()
	// Send a graceful shutdown frame to server
	if err := cc.fr.WriteGoAway(maxStreamID, ErrCodeNo, nil); err != nil {
		return err
	}
	if err := cc.bw.Flush(); err != nil {
		return err
	}
	// Prevent new requests
	return nil
}

// closes the client connection immediately. In-flight requests are interrupted.
// err is sent to streams.
func (cc *ClientConn) closeForError(err error) {
	cc.mu.Lock()
	cc.closed = true
	if cc.t.ChromiumUpload {
		cc.discardQueuedUploadHeadersLocked(err, false)
		cc.scheduleUploadTasksLocked()
	}
	for _, cs := range cc.streams {
		cs.abortStreamLocked(err)
	}
	cc.cond.Broadcast()
	cc.mu.Unlock()
	cc.closeConn()
}

// Close closes the client connection immediately.
//
// In-flight requests are interrupted. For a graceful shutdown, use Shutdown instead.
func (cc *ClientConn) Close() error {
	err := errors.New("http2: client connection force closed via ClientConn.Close")
	cc.closeForError(err)
	return nil
}

// closes the client connection immediately. In-flight requests are interrupted.
func (cc *ClientConn) closeForLostPing() {
	err := errors.New("http2: client connection lost")
	if f := cc.t.CountError; f != nil {
		f("conn_close_lost_ping")
	}
	cc.closeForError(err)
}

func commaSeparatedTrailers(req *http.Request) (string, error) {
	keys := make([]string, 0, len(req.Trailer))
	for k := range req.Trailer {
		k = canonicalHeader(k)
		switch k {
		case "Transfer-Encoding", "Trailer", "Content-Length":
			return "", fmt.Errorf("invalid Trailer key %q", k)
		}
		keys = append(keys, k)
	}
	if len(keys) > 0 {
		sort.Strings(keys)
		return strings.Join(keys, ","), nil
	}
	return "", nil
}

func (cc *ClientConn) responseHeaderTimeout() time.Duration {
	return cc.t.ResponseHeaderTimeout
}

// checkConnHeaders checks whether req has any invalid connection-level headers.
// per RFC 7540 section 8.1.2.2: Connection-Specific Header Fields.
// Certain headers are special-cased as okay but not transmitted later.
func checkConnHeaders(req *http.Request) error {
	if v := req.Header.Get("Upgrade"); v != "" {
		return fmt.Errorf("http2: invalid Upgrade request header: %q", req.Header["Upgrade"])
	}
	if vv := req.Header["Transfer-Encoding"]; len(vv) > 0 && (len(vv) > 1 || vv[0] != "" && vv[0] != "chunked") {
		return fmt.Errorf("http2: invalid Transfer-Encoding request header: %q", vv)
	}
	if vv := req.Header["Connection"]; len(vv) > 0 && (len(vv) > 1 || vv[0] != "" && !ascii.EqualFold(vv[0], "close") && !ascii.EqualFold(vv[0], "keep-alive")) {
		return fmt.Errorf("http2: invalid Connection request header: %q", vv)
	}
	return nil
}

// actualContentLength returns a sanitized version of
// req.ContentLength, where 0 actually means zero (not unknown) and -1
// means unknown.
func actualContentLength(req *http.Request) int64 {
	if req.Body == nil || req.Body == http.NoBody {
		return 0
	}
	if req.ContentLength != 0 {
		return req.ContentLength
	}
	return -1
}

func (cc *ClientConn) decrStreamReservations() {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.decrStreamReservationsLocked()
}

func (cc *ClientConn) decrStreamReservationsLocked() {
	if cc.streamsReserved > 0 {
		cc.streamsReserved--
	}
}

func (cc *ClientConn) RoundTrip(req *http.Request) (*http.Response, error) {
	return cc.roundTrip(req, nil)
}

func (cc *ClientConn) roundTrip(req *http.Request, streamf func(*clientStream)) (*http.Response, error) {
	if cc.t != nil && cc.t.Debugf != nil {
		cc.t.Debugf("HTTP/2 %s %s", req.Method, req.URL.String())
	}
	ctx := req.Context()
	cs := &clientStream{
		currentRequest:       req,
		cc:                   cc,
		ctx:                  ctx,
		reqCancel:            req.Cancel,
		isHead:               req.Method == "HEAD",
		reqBody:              req.Body,
		reqBodyContentLength: actualContentLength(req),
		trace:                httptrace.ContextClientTrace(ctx),
		peerClosed:           make(chan struct{}),
		abort:                make(chan struct{}),
		respHeaderRecv:       make(chan struct{}),
		donec:                make(chan struct{}),
	}

	// TODO(bradfitz): this is a copy of the logic in net/http. Unify somewhere?
	if !cc.t.DisableCompression &&
		req.Header.Get("Accept-Encoding") == "" &&
		req.Header.Get("Range") == "" &&
		!cs.isHead {
		// Request gzip only, not deflate. Deflate is ambiguous and
		// not as universally supported anyway.
		// See: https://zlib.net/zlib_faq.html#faq39
		//
		// Note that we don't request this for HEAD requests,
		// due to a bug in nginx:
		//   http://trac.nginx.org/nginx/ticket/358
		//   https://golang.org/issue/5522
		//
		// We don't request gzip if the request is for a range, since
		// auto-decoding a portion of a gzipped document will just fail
		// anyway. See https://golang.org/issue/8923
		cs.requestedGzip = true
	}
	go cs.doRequest(req, streamf)

	waitDone := func() error {
		select {
		case <-cs.donec:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-cs.reqCancel:
			return common.ErrRequestCanceled
		}
	}

	handleResponseHeaders := func() (*http.Response, error) {
		res := cs.res
		if res.StatusCode > 299 {
			// On error or status code 3xx, 4xx, 5xx, etc abort any
			// ongoing write, assuming that the server doesn't care
			// about our request body. If the server replied with 1xx or
			// 2xx, however, then assume the server DOES potentially
			// want our body (e.g. full-duplex streaming:
			// golang.org/issue/13444). If it turns out the server
			// doesn't, they'll RST_STREAM us soon enough. This is a
			// heuristic to avoid adding knobs to Transport. Hopefully
			// we can keep it.
			cs.abortRequestBodyWrite()
		}
		res.Request = req
		res.TLS = cc.tlsState
		if res.Body == noBody && actualContentLength(req) == 0 {
			// If there isn't a request or response body still being
			// written, then wait for the stream to be closed before
			// RoundTrip returns.
			if err := waitDone(); err != nil {
				return nil, err
			}
		}
		return res, nil
	}

	cancelRequest := func(cs *clientStream, err error) error {
		cs.cc.mu.Lock()
		bodyClosed := cs.reqBodyClosed
		cs.cc.mu.Unlock()
		// Wait for the request body to be closed.
		//
		// If nothing closed the body before now, abortStreamLocked
		// will have started a goroutine to close it.
		//
		// Closing the body before returning avoids a race condition
		// with net/http checking its readTrackingBody to see if the
		// body was read from or closed. See golang/go#60041.
		//
		// The body is closed in a separate goroutine without the
		// connection mutex held, but dropping the mutex before waiting
		// will keep us from holding it indefinitely if the body
		// close is slow for some reason.
		if bodyClosed != nil {
			<-bodyClosed
		}
		return err
	}

	for {
		select {
		case <-cs.respHeaderRecv:
			return handleResponseHeaders()
		case <-cs.abort:
			select {
			case <-cs.respHeaderRecv:
				// If both cs.respHeaderRecv and cs.abort are signaling,
				// pick respHeaderRecv. The server probably wrote the
				// response and immediately reset the stream.
				// golang.org/issue/49645
				return handleResponseHeaders()
			default:
				waitDone()
				return nil, cs.abortErr
			}
		case <-ctx.Done():
			err := ctx.Err()
			cs.abortStream(err)
			return nil, cancelRequest(cs, err)
		case <-cs.reqCancel:
			cs.abortStream(common.ErrRequestCanceled)
			return nil, cancelRequest(cs, common.ErrRequestCanceled)
		}
	}
}

// doRequest runs for the duration of the request lifetime.
//
// It sends the request and performs post-request cleanup (closing Request.Body, etc.).
func (cs *clientStream) doRequest(req *http.Request, streamf func(*clientStream)) {
	err := cs.writeRequest(req, streamf)
	cs.cleanupWriteRequest(err)
}

// writeRequest sends a request.
//
// It returns nil after the request is written, the response read,
// and the request stream is half-closed by the peer.
//
// It returns non-nil if the request ends otherwise.
// If the returned error is StreamError, the error Code may be used in resetting the stream.
func (cs *clientStream) writeRequest(req *http.Request, streamf func(*clientStream)) (err error) {
	cc := cs.cc
	ctx := cs.ctx

	if err := checkConnHeaders(req); err != nil {
		return err
	}

	// Acquire the new-request lock by writing to reqHeaderMu.
	// This lock guards the critical section covering allocating a new stream ID
	// (requires mu) and creating the stream (requires wmu).
	if cc.reqHeaderMu == nil {
		panic("RoundTrip on uninitialized ClientConn") // for tests
	}
	select {
	case cc.reqHeaderMu <- struct{}{}:
	case <-cs.reqCancel:
		return common.ErrRequestCanceled
	case <-ctx.Done():
		return ctx.Err()
	}

	cc.mu.Lock()
	if cc.idleTimer != nil {
		cc.idleTimer.Stop()
	}
	cc.decrStreamReservationsLocked()
	cs.reservationReleased = true
	if err := cc.awaitOpenSlotForStreamLocked(cs); err != nil {
		cc.mu.Unlock()
		<-cc.reqHeaderMu
		return err
	}
	if cc.t.ChromiumUpload {
		cc.uploadOpening++
	} else {
		cc.addStreamLocked(cs) // assigns stream ID
	}
	if isConnectionCloseRequest(req) {
		cc.doNotReuse = true
	}
	cc.mu.Unlock()

	if streamf != nil && !cc.t.ChromiumUpload {
		streamf(cs)
	}

	continueTimeout := cc.t.ExpectContinueTimeout
	if continueTimeout != 0 {
		if !httpguts.HeaderValuesContainsToken(req.Header["Expect"], "100-continue") {
			continueTimeout = 0
		} else {
			cs.on100 = make(chan struct{}, 1)
		}
	}

	var dumps []*dump.Dumper
	if t := cs.cc.t; t != nil {
		dumps = dump.GetDumpers(req.Context(), t.Dump)
	}
	bodyDumps := []*dump.Dumper{}
	for _, dump := range dumps {
		if dump.RequestBody() {
			bodyDumps = append(bodyDumps, dump)
		}
	}
	hasBody := cs.reqBodyContentLength != 0
	var upload *queuedUpload

	// Past this point (where we send request headers), it is possible for
	// RoundTrip to return successfully. Since the RoundTrip contract permits
	// the caller to "mutate or reuse" the Request after closing the Response's Body,
	// we must take care when referencing the Request from here on.
	if cc.t.ChromiumUpload {
		var onSent func()
		if hasBody && continueTimeout == 0 {
			onSent = func() { upload = cs.startQueuedUploadLocked(req, bodyDumps) }
		}
		err = cs.queueRequestHeaders(req, dumps, onSent)
		if err == nil && streamf != nil {
			streamf(cs)
		}
	} else {
		err = cs.encodeAndWriteHeaders(req, dumps)
		<-cc.reqHeaderMu
	}
	if err != nil {
		return err
	}

	if !hasBody {
		cs.sentEndStream = true
	} else {
		if continueTimeout != 0 {
			traceWait100Continue(cs.trace)
			timer := time.NewTimer(continueTimeout)
			select {
			case <-timer.C:
				err = nil
			case <-cs.on100:
				err = nil
			case <-cs.abort:
				err = cs.abortErr
			case <-ctx.Done():
				err = ctx.Err()
			case <-cs.reqCancel:
				err = common.ErrRequestCanceled
			}
			timer.Stop()
			if err != nil {
				traceWroteRequest(cs.trace, err)
				return err
			}
		}
		if upload != nil {
			err = cs.awaitQueuedUpload(req, bodyDumps, upload)
		} else {
			err = cs.writeRequestBody(req, bodyDumps)
		}
		if err != nil {
			if err != errStopReqBodyWrite {
				traceWroteRequest(cs.trace, err)
				return err
			}
		} else {
			cs.sentEndStream = true
			for _, dump := range bodyDumps {
				dump.DumpDefault([]byte("\r\n\r\n"))
			}
		}
	}

	traceWroteRequest(cs.trace, err)

	var respHeaderTimer <-chan time.Time
	var respHeaderRecv chan struct{}
	if d := cc.responseHeaderTimeout(); d != 0 {
		timer := cc.t.newTimer(d)
		defer timer.Stop()
		respHeaderTimer = timer.C()
		respHeaderRecv = cs.respHeaderRecv
	}
	// Wait until the peer half-closes its end of the stream,
	// or until the request is aborted (via context, error, or otherwise),
	// whichever comes first.
	for {
		select {
		case <-cs.peerClosed:
			return nil
		case <-respHeaderTimer:
			return errH2Timeout
		case <-respHeaderRecv:
			respHeaderRecv = nil
			respHeaderTimer = nil // keep waiting for END_STREAM
		case <-cs.abort:
			return cs.abortErr
		case <-ctx.Done():
			return ctx.Err()
		case <-cs.reqCancel:
			return common.ErrRequestCanceled
		}
	}
}

func (cs *clientStream) encodeAndWriteHeaders(req *http.Request, dumps []*dump.Dumper) error {
	cc := cs.cc
	ctx := cs.ctx
	cc.wmu.Lock()
	defer cc.wmu.Unlock()

	// If the request was canceled while waiting for cc.mu, just quit.
	select {
	case <-cs.abort:
		return cs.abortErr
	case <-ctx.Done():
		return ctx.Err()
	case <-cs.reqCancel:
		return common.ErrRequestCanceled
	default:
	}

	// Encode headers.
	//
	// we send: HEADERS{1}, CONTINUATION{0,} + DATA{0,} (DATA is
	// sent by writeRequestBody below, along with any Trailers,
	// again in form HEADERS{1}, CONTINUATION{0,})
	trailers, err := commaSeparatedTrailers(req)
	if err != nil {
		return err
	}
	hasTrailers := trailers != ""
	contentLen := actualContentLength(req)
	hasBody := contentLen != 0
	hdrs, err := cc.encodeHeaders(req, cs.requestedGzip, trailers, contentLen, dumps)
	if err != nil {
		return err
	}

	// Write the request.
	endStream := !hasBody && !hasTrailers
	cs.sentHeaders = true
	err = cc.writeHeaders(cs.ID, endStream, int(cc.maxFrameSize), hdrs, cc.t.HeaderPriority)
	traceWroteHeaders(cs.trace)
	return err
}

// cleanupWriteRequest performs post-request tasks.
//
// If err (the result of writeRequest) is non-nil and the stream is not closed,
// cleanupWriteRequest will send a reset to the peer.
func (cs *clientStream) cleanupWriteRequest(err error) {
	cc := cs.cc

	if !cs.reservationReleased {
		// We were canceled before creating the stream, so return our reservation.
		cc.decrStreamReservations()
	}

	// TODO: write h12Compare test showing whether
	// Request.Body is closed by the Transport,
	// and in multiple cases: server replies <=299 and >299
	// while still writing request body
	cc.mu.Lock()
	mustCloseBody := false
	if cs.reqBody != nil && cs.reqBodyClosed == nil {
		mustCloseBody = true
		cs.reqBodyClosed = make(chan struct{})
	}
	bodyClosed := cs.reqBodyClosed
	cc.mu.Unlock()
	if mustCloseBody {
		cs.reqBody.Close()
		close(bodyClosed)
	}
	if bodyClosed != nil {
		<-bodyClosed
	}

	if err != nil && cs.sentEndStream {
		// If the connection is closed immediately after the response is read,
		// we may be aborted before finishing up here. If the stream was closed
		// cleanly on both sides, there is no error.
		select {
		case <-cs.peerClosed:
			err = nil
		default:
		}
	}
	if err != nil {
		cs.abortStream(err) // possibly redundant, but harmless
		if cs.sentHeaders {
			if se, ok := err.(StreamError); ok {
				if se.Cause != errFromPeer {
					cs.writeStreamReset(se.Code)
				}
			} else {
				cs.writeStreamReset(ErrCodeCancel)
			}
		}
		cs.bufPipe.CloseWithError(err) // no-op if already closed
	} else {
		if cs.sentHeaders && !cs.sentEndStream {
			cs.writeStreamReset(ErrCodeNo)
		}
		cs.bufPipe.CloseWithError(common.ErrRequestCanceled)
	}
	if cs.ID != 0 {
		cc.forgetStreamID(cs.ID)
	}

	// Chromium 排空会话控制帧后再完成失败请求；取消仍可从 RoundTrip 返回。
	cc.mu.Lock()
	for cc.uploadDrainErr != nil && (cc.uploadWriting || len(cc.uploadEvents) > 0 || cc.uploadTaskRunning) && !cc.closed {
		cc.cond.Wait()
	}
	cc.mu.Unlock()
	cc.wmu.Lock()
	werr := cc.werr
	cc.wmu.Unlock()
	if werr != nil {
		cc.Close()
	}

	close(cs.donec)
}

// awaitOpenSlotForStreamLocked waits until len(streams) < maxConcurrentStreams.
// Must hold cc.mu.
func (cc *ClientConn) awaitOpenSlotForStreamLocked(cs *clientStream) error {
	for {
		cc.lastActive = time.Now()
		if cc.closed || !cc.canTakeNewRequestLocked() {
			return errClientConnUnusable
		}
		cc.lastIdle = time.Time{}
		if int64(len(cc.streams)+cc.uploadOpening) < int64(cc.maxConcurrentStreams) {
			return nil
		}
		cc.pendingRequests++
		cc.cond.Wait()
		cc.pendingRequests--
		select {
		case <-cs.abort:
			return cs.abortErr
		default:
		}
	}
}

// requires cc.wmu be held
func (cc *ClientConn) writeHeaders(streamID uint32, endStream bool, maxFrameSize int, hdrs []byte, priority http2.PriorityParam) error {
	first := true // first frame written (HEADERS is first, then CONTINUATION)
	for len(hdrs) > 0 && cc.werr == nil {
		chunk := hdrs
		if len(chunk) > maxFrameSize {
			chunk = chunk[:maxFrameSize]
		}
		hdrs = hdrs[len(chunk):]
		endHeaders := len(hdrs) == 0
		if first {
			cc.fr.WriteHeaders(HeadersFrameParam{
				StreamID:      streamID,
				BlockFragment: chunk,
				EndStream:     endStream,
				EndHeaders:    endHeaders,
				Priority:      priority,
			})
			first = false
		} else {
			cc.fr.WriteContinuation(streamID, endHeaders, chunk)
		}
	}
	cc.bw.Flush()
	return cc.werr
}

// internal error values; they don't escape to callers
var (
	// abort request body write; don't send cancel
	errStopReqBodyWrite = errors.New("http2: aborting request body write")

	// abort request body write, but send stream reset of cancel.
	errStopReqBodyWriteAndCancel = errors.New("http2: canceling request")

	errReqBodyTooLong = errors.New("http2: request body larger than specified content length")
)

// frameScratchBufferLen returns the length of a buffer to use for
// outgoing request bodies to read/write to/from.
//
// It returns max(1, min(peer's advertised max frame size,
// Request.ContentLength+1, 512KB)).
func (cs *clientStream) frameScratchBufferLen(maxFrameSize int) int {
	const max = 512 << 10
	n := int64(maxFrameSize)
	if n > max {
		n = max
	}
	if cl := cs.reqBodyContentLength; cl != -1 && cl+1 < n {
		// Add an extra byte past the declared content-length to
		// give the caller's Request.Body io.textprotoReader a chance to
		// give us more bytes than they declared, so we can catch it
		// early.
		n = cl + 1
	}
	if n < 1 {
		return 1
	}
	return int(n) // doesn't truncate; max is 512K
}

// Seven bufPools manage different frame sizes. This helps to avoid scenarios where long-running
// streaming requests using small frame sizes occupy large buffers initially allocated for prior
// requests needing big buffers. The size ranges are as follows:
// {0 KB, 16 KB], {16 KB, 32 KB], {32 KB, 64 KB], {64 KB, 128 KB], {128 KB, 256 KB],
// {256 KB, 512 KB], {512 KB, infinity}
// In practice, the maximum scratch buffer size should not exceed 512 KB due to
// frameScratchBufferLen(maxFrameSize), thus the "infinity pool" should never be used.
// It exists mainly as a safety measure, for potential future increases in max buffer size.
var bufPools [7]sync.Pool // of *[]byte
func bufPoolIndex(size int) int {
	if size <= 16384 {
		return 0
	}
	size -= 1
	bits := bits.Len(uint(size))
	index := bits - 14
	if index >= len(bufPools) {
		return len(bufPools) - 1
	}
	return index
}

func (cs *clientStream) writeRequestBody(req *http.Request, dumps []*dump.Dumper) (err error) {
	cc := cs.cc
	if cc.t.ChromiumUpload {
		return cs.writeQueuedUpload(req, dumps)
	}
	body := cs.reqBody
	sentEnd := false // whether we sent the final DATA frame w/ END_STREAM

	hasTrailers := req.Trailer != nil
	remainLen := cs.reqBodyContentLength
	hasContentLen := remainLen != -1

	cc.mu.Lock()
	maxFrameSize := int(cc.maxFrameSize)
	cc.mu.Unlock()
	uploadFrameSize := maxFrameSize
	if limit := cc.t.MaxUploadFrameSize; limit != 0 && uint32(uploadFrameSize) > limit {
		uploadFrameSize = int(limit)
	}

	// Scratch buffer for reading into & writing from.
	scratchLen := cs.frameScratchBufferLen(uploadFrameSize)
	var buf []byte
	index := bufPoolIndex(scratchLen)
	if bp, ok := bufPools[index].Get().(*[]byte); ok && len(*bp) >= scratchLen {
		defer bufPools[index].Put(bp)
		buf = *bp
	} else {
		buf = make([]byte, scratchLen)
		defer bufPools[index].Put(&buf)
	}

	writeData := cc.fr.WriteData
	if len(dumps) > 0 {
		writeData = func(streamID uint32, endStream bool, data []byte) error {
			for _, dump := range dumps {
				dump.DumpRequestBody(data)
			}
			return cc.fr.WriteData(streamID, endStream, data)
		}
	}

	var sawEOF bool
	for !sawEOF {
		n, err := body.Read(buf[:scratchLen])
		if hasContentLen {
			remainLen -= int64(n)
			if remainLen == 0 && err == nil {
				// The request body's Content-Length was predeclared and
				// we just finished reading it all, but the underlying io.textprotoReader
				// returned the final chunk with a nil error (which is one of
				// the two valid things a textprotoReader can do at EOF). Because we'd prefer
				// to send the END_STREAM bit early, double-check that we're actually
				// at EOF. Subsequent reads should return (0, EOF) at this point.
				// If either value is different, we return an error in one of two ways below.
				var scratch [1]byte
				var n1 int
				n1, err = body.Read(scratch[:])
				remainLen -= int64(n1)
			}
			if remainLen < 0 {
				err = errReqBodyTooLong
				return err
			}
		}
		if err != nil {
			cc.mu.Lock()
			bodyClosed := cs.reqBodyClosed != nil
			cc.mu.Unlock()
			switch {
			case bodyClosed:
				return errStopReqBodyWrite
			case err == io.EOF:
				sawEOF = true
				err = nil
			default:
				return err
			}
		}

		remain := buf[:n]
		for len(remain) > 0 && err == nil {
			var allowed int32
			allowed, err = cs.awaitFlowControl(len(remain))
			if err != nil {
				return err
			}
			cc.wmu.Lock()
			data := remain[:allowed]
			remain = remain[allowed:]
			sentEnd = sawEOF && len(remain) == 0 && !hasTrailers
			err = writeData(cs.ID, sentEnd, data)
			if err == nil {
				// TODO(bradfitz): this flush is for latency, not bandwidth.
				// Most requests won't need this. Make this opt-in or
				// opt-out?  Use some heuristic on the body type? Nagel-like
				// timers?  Based on 'n'? Only last chunk of this for loop,
				// unless flow control tokens are low? For now, always.
				// If we change this, see comment below.
				err = cc.bw.Flush()
			}
			cc.wmu.Unlock()
		}
		if err != nil {
			return err
		}
	}

	if sentEnd {
		// Already sent END_STREAM (which implies we have no
		// trailers) and flushed, because currently all
		// WriteData frames above get a flush. So we're done.
		return nil
	}

	return cs.writeUploadTrailers(req, dumps, maxFrameSize)
}

func (cs *clientStream) writeUploadTrailers(req *http.Request, dumps []*dump.Dumper, maxFrameSize int) (err error) {
	cc := cs.cc

	// Since the RoundTrip contract permits the caller to "mutate or reuse"
	// a request after the Response's Body is closed, verify that this hasn't
	// happened before accessing the trailers.
	cc.mu.Lock()
	trailer := req.Trailer
	err = cs.abortErr
	cc.mu.Unlock()
	if err != nil {
		return err
	}

	cc.wmu.Lock()
	var trls []byte
	if len(trailer) > 0 {
		if cc.t.ChromiumUpload {
			cc.mu.Lock()
		}
		trls, err = cc.encodeTrailers(trailer, dumps)
		if cc.t.ChromiumUpload {
			cc.mu.Unlock()
		}
		if err != nil {
			cc.wmu.Unlock()
			return err
		}
	}
	if len(trls) == 0 && cc.t.ChromiumFlowControl {
		// TTNet 的独立空结束 DATA 也经过 Chromium 的窗口检查。
		// 等待时释放写锁，避免阻塞 SETTINGS ACK 等控制帧。
		cc.wmu.Unlock()
		if _, err = cs.awaitFlowControl(0); err != nil {
			return err
		}
		cc.wmu.Lock()
	}
	defer cc.wmu.Unlock()

	// Two ways to send END_STREAM: either with trailers, or
	// with an empty DATA frame.
	if len(trls) > 0 {
		err = cc.writeHeaders(cs.ID, true, maxFrameSize, trls, cc.t.HeaderPriority)
	} else {
		err = cc.fr.WriteData(cs.ID, true, nil)
	}
	if ferr := cc.bw.Flush(); ferr != nil && err == nil {
		err = ferr
	}
	return err
}

// awaitFlowControl 等连接和流窗口均为正后，预留最多 maxBytes 的额度。
// maxBytes 为零时只等待窗口，不消耗额度，用于 Chromium 的空结束 DATA。
func (cs *clientStream) awaitFlowControl(maxBytes int) (taken int32, err error) {
	cc := cs.cc
	ctx := cs.ctx
	cc.mu.Lock()
	defer cc.mu.Unlock()
	for {
		if cc.closed {
			return 0, errClientConnClosed
		}
		if cs.reqBodyClosed != nil {
			return 0, errStopReqBodyWrite
		}
		select {
		case <-cs.abort:
			return 0, cs.abortErr
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-cs.reqCancel:
			return 0, common.ErrRequestCanceled
		default:
		}
		if a := cs.flow.available(); a > 0 {
			take := a
			if int(take) > maxBytes {
				take = int32(maxBytes) // can't truncate int; take is int32
			}
			if take > int32(cc.maxFrameSize) {
				take = int32(cc.maxFrameSize)
			}
			cs.flow.take(take)
			return take, nil
		}
		cc.cond.Wait()
	}
}

func validateHeaders(hdrs http.Header) string {
	for k, vv := range hdrs {
		if !httpguts.ValidHeaderFieldName(k) {
			return fmt.Sprintf("name %q", k)
		}
		for _, v := range vv {
			if !httpguts.ValidHeaderFieldValue(v) {
				// Don't include the value in the error,
				// because it may be sensitive.
				return fmt.Sprintf("value for header %q", k)
			}
		}
	}
	return ""
}

var errNilRequestURL = errors.New("http2: Request.URI is nil")

// requires cc.wmu be held. ChromiumUpload also holds cc.mu.
func (cc *ClientConn) encodeHeaders(req *http.Request, addGzipHeader bool, trailers string, contentLength int64, dumps []*dump.Dumper) ([]byte, error) {
	cc.hbuf.Reset()
	if req.URL == nil {
		return nil, errNilRequestURL
	}

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	host, err := httpguts.PunycodeHostPort(host)
	if err != nil {
		return nil, err
	}
	if !httpguts.ValidHostHeader(host) {
		return nil, errors.New("http2: invalid Host header")
	}

	var path string
	if req.Method != "CONNECT" {
		path = req.URL.RequestURI()
		if !validPseudoPath(path) {
			orig := path
			path = strings.TrimPrefix(path, req.URL.Scheme+"://"+host)
			if !validPseudoPath(path) {
				if req.URL.Opaque != "" {
					return nil, fmt.Errorf("invalid request :path %q from URL.Opaque = %q", orig, req.URL.Opaque)
				}
				return nil, fmt.Errorf("invalid request :path %q", orig)
			}
		}
	}

	// Check for any invalid headers+trailers and return an error before we
	// potentially pollute our hpack state. (We want to be able to
	// continue to reuse the hpack encoder for future requests)
	if err := validateHeaders(req.Header); err != "" {
		return nil, fmt.Errorf("invalid HTTP header %s", err)
	}
	if err := validateHeaders(req.Trailer); err != "" {
		return nil, fmt.Errorf("invalid HTTP trailer %s", err)
	}

	enumerateHeaders := func(f func(name, value string)) {
		var writeHeader func(name string, value ...string)
		var kvs []header.KeyValues
		sort := false

		if req.Header != nil && len(req.Header[header.PseudoHeaderOderKey]) > 0 {
			writeHeader = func(name string, value ...string) {
				kvs = append(kvs, header.KeyValues{
					Key:    name,
					Values: value,
				})
			}
			sort = true
		} else {
			writeHeader = func(name string, value ...string) {
				for _, v := range value {
					f(name, v)
				}
			}
		}

		// 8.1.2.3 Request Pseudo-Header Fields
		// The :path pseudo-header field includes the path and query parts of the
		// target URI (the path-absolute production and optionally a '?' character
		// followed by the query production, see Sections 3.3 and 3.4 of
		// [RFC3986]).
		writeHeader(":authority", host)
		m := req.Method
		if m == "" {
			m = http.MethodGet
		}
		writeHeader(":method", m)
		if req.Method != "CONNECT" {
			writeHeader(":path", path)
			writeHeader(":scheme", req.URL.Scheme)
		}
		if sort {
			header.SortKeyValues(kvs, req.Header[header.PseudoHeaderOderKey])
			for _, kv := range kvs {
				for _, v := range kv.Values {
					f(kv.Key, v)
				}
			}
		}

		if req.Header != nil && len(req.Header[header.HeaderOderKey]) > 0 {
			sort = true
			kvs = nil
			writeHeader = func(name string, value ...string) {
				kvs = append(kvs, header.KeyValues{
					Key:    name,
					Values: value,
				})
			}
		} else {
			sort = false
			writeHeader = func(name string, value ...string) {
				for _, v := range value {
					f(name, v)
				}
			}
		}

		if trailers != "" {
			writeHeader("trailer", trailers)
		}

		var didUA bool
		for k, vv := range req.Header {
			if header.IsExcluded(k) {
				continue
			} else if ascii.EqualFold(k, "user-agent") {
				// Match Go's http1 behavior: at most one
				// User-Agent. If set to nil or empty string,
				// then omit it. Otherwise if not mentioned,
				// include the default (below).
				didUA = true
				if len(vv) < 1 {
					continue
				}
				vv = vv[:1]
				if vv[0] == "" {
					continue
				}
			} else if ascii.EqualFold(k, "cookie") {
				var vals []string
				// Per 8.1.2.5 To allow for better compression efficiency, the
				// Cookie header field MAY be split into separate header fields,
				// each with one or more cookie-pairs.
				for _, v := range vv {
					for {
						p := strings.IndexByte(v, ';')
						if p < 0 {
							break
						}
						vals = append(vals, v[:p])
						// writeHeader("cookie", v[:p])
						p++
						// strip space after semicolon if any.
						for p+1 <= len(v) && v[p] == ' ' {
							p++
						}
						v = v[p:]
					}
					if len(v) > 0 {
						vals = append(vals, v)
						// writeHeader("cookie", v)
					}
				}
				writeHeader("cookie", vals...)
				continue
			}

			writeHeader(k, vv...)
		}
		if shouldSendReqContentLength(req.Method, contentLength) {
			writeHeader("content-length", strconv.FormatInt(contentLength, 10))
		}
		if addGzipHeader {
			writeHeader("accept-encoding", "gzip")
		}
		if !didUA {
			writeHeader("user-agent", header.DefaultUserAgent)
		}

		if sort {
			header.SortKeyValues(kvs, req.Header[header.HeaderOderKey])
			for _, kv := range kvs {
				for _, v := range kv.Values {
					f(kv.Key, v)
				}
			}
		}
	}

	// Do a first pass over the headers counting bytes to ensure
	// we don't exceed cc.peerMaxHeaderListSize. This is done as a
	// separate pass before encoding the headers to prevent
	// modifying the hpack state.
	hlSize := uint64(0)
	enumerateHeaders(func(name, value string) {
		hf := hpack.HeaderField{Name: name, Value: value}
		hlSize += uint64(hf.Size())
	})

	if hlSize > cc.peerMaxHeaderListSize {
		return nil, errRequestHeaderListSize
	}

	trace := httptrace.ContextClientTrace(req.Context())
	traceHeaders := traceHasWroteHeaderField(trace)

	writeHeader := cc.writeHeader
	headerDumps := []*dump.Dumper{}
	if len(dumps) > 0 {
		for _, dump := range dumps {
			if dump.RequestHeader() {
				headerDumps = append(headerDumps, dump)
			}
		}
		if len(headerDumps) > 0 {
			writeHeader = func(name, value string) {
				for _, dump := range headerDumps {
					dump.DumpRequestHeader([]byte(fmt.Sprintf("%s: %s\r\n", name, value)))
				}
				cc.writeHeader(name, value)
			}
		}
	}

	// Header list size is ok. Write the headers.
	enumerateHeaders(func(name, value string) {
		name, ascii := lowerHeader(name)
		if !ascii {
			// Skip writing invalid headers. Per RFC 7540, Section 8.1.2, header
			// field names have to be ASCII characters (just as in HTTP/1.x).
			return
		}
		writeHeader(name, value)
		if traceHeaders {
			traceWroteHeaderField(trace, name, value)
		}
	})

	for _, dump := range headerDumps {
		dump.DumpRequestHeader([]byte("\r\n"))
	}

	return cc.hbuf.Bytes(), nil
}

// shouldSendReqContentLength reports whether the http2.Transport should send
// a "content-length" request header. This logic is basically a copy of the net/http
// transferWriter.shouldSendContentLength.
// The contentLength is the corrected contentLength (so 0 means actually 0, not unknown).
// -1 means unknown.
func shouldSendReqContentLength(method string, contentLength int64) bool {
	if contentLength > 0 {
		return true
	}
	if contentLength < 0 {
		return false
	}
	// For zero bodies, whether we send a content-length depends on the method.
	// It also kinda doesn't matter for http2 either way, with END_STREAM.
	switch method {
	case "POST", "PUT", "PATCH":
		return true
	default:
		return false
	}
}

// requires cc.wmu be held. ChromiumUpload also holds cc.mu.
func (cc *ClientConn) encodeTrailers(trailer http.Header, dumps []*dump.Dumper) ([]byte, error) {
	cc.hbuf.Reset()

	hlSize := uint64(0)
	for k, vv := range trailer {
		for _, v := range vv {
			hf := hpack.HeaderField{Name: k, Value: v}
			hlSize += uint64(hf.Size())
		}
	}
	if hlSize > cc.peerMaxHeaderListSize {
		return nil, errRequestHeaderListSize
	}

	writeHeader := cc.writeHeader
	if len(dumps) > 0 {
		writeHeader = func(name, value string) {
			for _, dump := range dumps {
				dump.DumpRequestHeader([]byte(fmt.Sprintf("%s: %s\r\n", name, value)))
			}
			cc.writeHeader(name, value)
		}
	}

	for k, vv := range trailer {
		lowKey, ascii := lowerHeader(k)
		if !ascii {
			// Skip writing invalid headers. Per RFC 7540, Section 8.1.2, header
			// field names have to be ASCII characters (just as in HTTP/1.x).
			continue
		}
		// Transfer-Encoding, etc.. have already been filtered at the
		// start of RoundTrip
		for _, v := range vv {
			writeHeader(lowKey, v)
		}
	}
	return cc.hbuf.Bytes(), nil
}

func (cc *ClientConn) writeHeader(name, value string) {
	if VerboseLogs {
		log.Printf("http2: Transport encoding header %q = %q", name, value)
	}
	if cc.t.PathWithoutIndex && name == ":path" && value != "/" && value != "/index.html" {
		// 伪头中先发送的 :method 已让编码器写出待处理的表大小更新。
		// 不调用编码器，因此路径不会占据其动态表；后续字段仍由原编码器维护。
		cc.hbuf.Write(pathWithoutIndex(value))
		return
	}
	cc.henc.WriteField(clienthpack.HeaderField{Name: name, Value: value})
}

type resAndError struct {
	_   incomparable
	res *http.Response
	err error
}

// requires cc.mu be held.
func (cc *ClientConn) addStreamLocked(cs *clientStream) {
	cs.flow.add(int32(cc.initialWindowSize))
	cs.flow.setConnFlow(&cc.flow)
	streamWindow := int32(transportDefaultStreamFlow)
	for _, setting := range cc.t.Settings {
		if setting.ID == http2.SettingInitialWindowSize {
			streamWindow = int32(setting.Val)
		}
	}
	cs.inflow.init(streamWindow)
	if cc.t.ChromiumFlowControl {
		cs.inflow.initChromium(streamWindow)
	}
	cs.ID = cc.nextStreamID
	cc.nextStreamID += 2
	cc.streams[cs.ID] = cs
	if cs.ID == 0 {
		panic("assigned stream ID 0")
	}
}

func (cc *ClientConn) forgetStreamID(id uint32) {
	cc.mu.Lock()
	slen := len(cc.streams)
	delete(cc.streams, id)
	if len(cc.streams) != slen-1 {
		panic("forgetting unknown stream id")
	}
	cc.lastActive = time.Now()
	if len(cc.streams) == 0 && cc.uploadOpening == 0 && cc.idleTimer != nil {
		cc.idleTimer.Reset(cc.idleTimeout)
		cc.lastIdle = time.Now()
	}
	// Wake up writeRequestBody via clientStream.awaitFlowControl and
	// wake up RoundTrip if there is a pending request.
	cc.cond.Broadcast()

	closeOnIdle := cc.singleUse || cc.doNotReuse || cc.t.DisableKeepAlives || cc.goAway != nil
	if closeOnIdle && cc.uploadDrainErr == nil && cc.streamsReserved == 0 && cc.uploadOpening == 0 && len(cc.streams) == 0 {
		// 已排队的 RST_STREAM 独立于流，最后一个流释放后仍要写出。
		for (cc.uploadWriting || len(cc.uploadEvents) > 0 || cc.uploadTaskRunning) && !cc.closed && cc.uploadDrainErr == nil {
			cc.cond.Wait()
		}
		if cc.closed || cc.uploadDrainErr != nil || cc.streamsReserved != 0 || cc.uploadOpening != 0 || len(cc.streams) != 0 {
			cc.mu.Unlock()
			return
		}
		if VerboseLogs {
			cc.vlogf("http2: Transport closing idle conn %p (forSingleUse=%v, maxStream=%v)", cc, cc.singleUse, cc.nextStreamID-2)
		}
		cc.closed = true
		defer cc.closeConn()
	}

	cc.mu.Unlock()
}

// clientConnReadLoop is the state owned by the clientConn's frame-reading readLoop.
type clientConnReadLoop struct {
	_  incomparable
	cc *ClientConn
}

// readLoop runs in its own goroutine and reads and dispatches frames.
func (cc *ClientConn) readLoop() {
	rl := &clientConnReadLoop{cc: cc}
	defer rl.cleanup()
	cc.readerErr = rl.run()
	if cc.uploadReader != nil {
		cc.uploadReader.idle()
	}
	if cc.t.ChromiumUpload && uploadDrainable(cc.readerErr) {
		cc.drainUploadControls(cc.readerErr)
		return
	}
	if ce, ok := cc.readerErr.(ConnectionError); ok {
		cc.wmu.Lock()
		cc.fr.WriteGoAway(0, ErrCode(ce), nil)
		cc.wmu.Unlock()
	}
}

// GoAwayError is returned by the Transport when the server closes the
// TCP connection after sending a GOAWAY frame.
type GoAwayError struct {
	LastStreamID uint32
	ErrCode      ErrCode
	DebugData    string
}

func (e GoAwayError) Error() string {
	return fmt.Sprintf("http2: server sent GOAWAY and closed the connection; LastStreamID=%v, ErrCode=%v, debug=%q",
		e.LastStreamID, e.ErrCode, e.DebugData)
}

func isEOFOrNetReadError(err error) bool {
	if err == io.EOF {
		return true
	}
	ne, ok := err.(*net.OpError)
	return ok && ne.Op == "read"
}

func (rl *clientConnReadLoop) cleanup() {
	cc := rl.cc
	cc.t.connPool().MarkDead(cc)
	defer cc.closeConn()
	defer close(cc.readerDone)

	if cc.idleTimer != nil {
		cc.idleTimer.Stop()
	}

	// Close any response bodies if the server closes prematurely.
	// TODO: also do this if we've written the headers but not
	// gotten a response yet.
	err := cc.readerErr
	cc.mu.Lock()
	if cc.goAway != nil && isEOFOrNetReadError(err) {
		err = GoAwayError{
			LastStreamID: cc.goAway.LastStreamID,
			ErrCode:      cc.goAway.ErrCode,
			DebugData:    cc.goAwayDebug,
		}
	} else if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	cc.closed = true
	if cc.t.ChromiumUpload {
		cc.discardQueuedUploadHeadersLocked(err, false)
	}

	for _, cs := range cc.streams {
		select {
		case <-cs.peerClosed:
			// The server closed the stream before closing the conn,
			// so no need to interrupt it.
		default:
			cs.abortStreamLocked(err)
		}
	}
	if cc.t.ChromiumUpload {
		cc.scheduleUploadTasksLocked()
	}
	cc.cond.Broadcast()
	cc.mu.Unlock()
}

// countReadFrameError calls Transport.CountError with a string
// representing err.
func (cc *ClientConn) countReadFrameError(err error) {
	f := cc.t.CountError
	if f == nil || err == nil {
		return
	}
	var ce ConnectionError
	if errors.As(err, &ce) {
		errCode := ErrCode(ce)
		f(fmt.Sprintf("read_frame_conn_error_%s", errCode.stringToken()))
		return
	}
	if errors.Is(err, io.EOF) {
		f("read_frame_eof")
		return
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		f("read_frame_unexpected_eof")
		return
	}
	if errors.Is(err, errFrameTooLarge) {
		f("read_frame_too_large")
		return
	}
	f("read_frame_other")
}

func (rl *clientConnReadLoop) run() error {
	cc := rl.cc
	gotSettings := false
	readIdleTimeout := cc.t.ReadIdleTimeout
	var t timer
	if readIdleTimeout != 0 {
		t = cc.t.afterFunc(readIdleTimeout, cc.healthCheck)
	}
	for {
		f, err := cc.fr.ReadFrame()
		if cc.t.ChromiumUpload {
			cc.mu.Lock()
			drainErr := cc.uploadDrainErr
			cc.mu.Unlock()
			if drainErr != nil {
				return drainErr
			}
		}
		if t != nil {
			t.Reset(readIdleTimeout)
		}
		if err != nil {
			cc.vlogf("http2: Transport readFrame error on conn %p: (%T) %v", cc, err, err)
		}
		if se, ok := err.(StreamError); ok {
			if cs := rl.streamByID(se.StreamID); cs != nil {
				if se.Cause == nil {
					se.Cause = cc.fr.errDetail
				}
				rl.endStreamError(cs, se)
			}
			continue
		} else if err != nil {
			cc.countReadFrameError(err)
			return err
		}
		if VerboseLogs {
			cc.vlogf("http2: Transport received %s", summarizeFrame(f))
		}
		if !gotSettings {
			if _, ok := f.(*SettingsFrame); !ok {
				cc.logf("protocol error: received %T before a SETTINGS frame", f)
				return ConnectionError(ErrCodeProtocol)
			}
			gotSettings = true
		}

		switch f := f.(type) {
		case *MetaHeadersFrame:
			err = rl.processHeaders(f)
		case *DataFrame:
			err = rl.processData(f)
		case *GoAwayFrame:
			err = rl.processGoAway(f)
		case *RSTStreamFrame:
			err = rl.processResetStream(f)
		case *SettingsFrame:
			err = rl.processSettings(f)
		case *PushPromiseFrame:
			err = rl.processPushPromise(f)
		case *WindowUpdateFrame:
			err = rl.processWindowUpdate(f)
		case *PingFrame:
			err = rl.processPing(f)
		default:
			cc.logf("Transport: unhandled response frame type %T", f)
		}
		if err != nil {
			if VerboseLogs {
				cc.vlogf("http2: Transport conn %p received error from processing frame %v: %v", cc, summarizeFrame(f), err)
			}
			return err
		}
	}
}

func (rl *clientConnReadLoop) processHeaders(f *MetaHeadersFrame) error {
	cs := rl.streamByID(f.StreamID)
	if cs == nil {
		// We'd get here if we canceled a request while the
		// server had its response still in flight. So if this
		// was just something we canceled, ignore it.
		return nil
	}
	if cs.readClosed {
		rl.endStreamError(cs, StreamError{
			StreamID: f.StreamID,
			Code:     ErrCodeProtocol,
			Cause:    errors.New("protocol error: headers after END_STREAM"),
		})
		return nil
	}
	if !cs.firstByte {
		if cs.trace != nil {
			// TODO(bradfitz): move first response byte earlier,
			// when we first read the 9 byte header, not waiting
			// until all the HEADERS+CONTINUATION frames have been
			// merged. This works for now.
			traceFirstResponseByte(cs.trace)
		}
		cs.firstByte = true
	}
	if !cs.pastHeaders {
		cs.pastHeaders = true
	} else {
		return rl.processTrailers(cs, f)
	}

	res, err := rl.handleResponse(cs, f)
	if err != nil {
		if _, ok := err.(ConnectionError); ok {
			return err
		}
		// Any other error type is a stream error.
		rl.endStreamError(cs, StreamError{
			StreamID: f.StreamID,
			Code:     ErrCodeProtocol,
			Cause:    err,
		})
		return nil // return nil from process* funcs to keep conn alive
	}
	if res == nil {
		// (nil, nil) special case. See handleResponse docs.
		return nil
	}
	cs.resTrailer = &res.Trailer
	cs.res = res
	close(cs.respHeaderRecv)
	if f.StreamEnded() {
		rl.endStream(cs)
	}
	return nil
}

// foreachHeaderElement splits v according to the "#rule" construction
// in RFC 7230 section 7 and calls fn for each non-empty element.
func foreachHeaderElement(v string, fn func(string)) {
	v = textproto.TrimString(v)
	if v == "" {
		return
	}
	if !strings.Contains(v, ",") {
		fn(v)
		return
	}
	for _, f := range strings.Split(v, ",") {
		if f = textproto.TrimString(f); f != "" {
			fn(f)
		}
	}
}

// may return error types nil, or ConnectionError. Any other error value
// is a StreamError of type ErrCodeProtocol. The returned error in that case
// is the detail.
//
// As a special case, handleResponse may return (nil, nil) to skip the
// frame (currently only used for 1xx responses).
func (rl *clientConnReadLoop) handleResponse(cs *clientStream, f *MetaHeadersFrame) (*http.Response, error) {
	if f.Truncated {
		return nil, errResponseHeaderListSize
	}

	status := f.PseudoValue("status")
	if status == "" {
		return nil, errors.New("malformed response from server: missing status pseudo header")
	}
	if cs.cc.t.ChromiumUpload {
		// 官方 HeaderCoalescer 合并同名字段；多个 :status 合并后不能解析。
		var statuses int
		for _, field := range f.PseudoFields() {
			if field.Name == ":status" {
				statuses++
			}
		}
		if statuses != 1 {
			return nil, errors.New("malformed response from server: duplicate status pseudo header")
		}
	}
	statusCode, err := strconv.Atoi(status)
	if err != nil {
		return nil, errors.New("malformed response from server: malformed non-numeric status pseudo header")
	}
	if cs.cc.t.ChromiumUpload && (statusCode > math.MaxInt32 || statusCode < math.MinInt32) {
		return nil, errors.New("malformed response from server: status pseudo header is outside int32 range")
	}

	regularFields := f.RegularFields()
	if cs.cc.t.ChromiumUpload && (statusCode/100 != 1 || statusCode == 101 || statusCode == 103) {
		for _, field := range regularFields {
			if field.Name == "transfer-encoding" {
				return nil, errors.New("protocol error: received transfer-encoding header")
			}
		}
	}
	if cs.cc.t.ChromiumUpload && statusCode == 101 {
		// SpdyStream 将 101 交给非 WebSocket 的事务层；事务层跳过后
		// 再读同一响应，已清空的 HttpResponseHeaders 按官方默认解析为 200。
		// 该行为不适用于普通 req 路径，也不把后一块 HEADERS 当新最终响应。
		status, statusCode, regularFields = "200", 200, nil
	}
	strs := make([]string, len(regularFields))
	header := make(http.Header, len(regularFields))
	res := &http.Response{
		Proto:      "HTTP/2.0",
		ProtoMajor: 2,
		Header:     header,
		StatusCode: statusCode,
		Status:     status + " " + http.StatusText(statusCode),
	}
	for _, hf := range regularFields {
		key := canonicalHeader(hf.Name)
		if key == "Trailer" {
			t := res.Trailer
			if t == nil {
				t = make(http.Header)
				res.Trailer = t
			}
			foreachHeaderElement(hf.Value, func(v string) {
				t[canonicalHeader(v)] = nil
			})
		} else {
			vv := header[key]
			if vv == nil && len(strs) > 0 {
				// More than likely this will be a single-element key.
				// Most headers aren't multi-valued.
				// Set the capacity on strs[0] to 1, so any future append
				// won't extend the slice into the other strings.
				vv, strs = strs[:1:1], strs[1:]
				vv[0] = hf.Value
				header[key] = vv
			} else {
				header[key] = append(vv, hf.Value)
			}
		}
	}

	if statusCode >= 100 && statusCode <= 199 {
		if f.StreamEnded() {
			return nil, errors.New("1xx informational response with END_STREAM flag")
		}
		if !cs.cc.t.ChromiumUpload {
			cs.num1xx++
			const max1xxResponses = 5 // arbitrary bound on number of informational responses, same as net/http
			if cs.num1xx > max1xxResponses {
				return nil, errors.New("http2: too many 1xx informational responses")
			}
		}
		if fn := cs.get1xxTraceFunc(); fn != nil {
			if err := fn(statusCode, textproto.MIMEHeader(header)); err != nil {
				return nil, err
			}
		}
		if statusCode == 100 {
			traceGot100Continue(cs.trace)
			select {
			case cs.on100 <- struct{}{}:
			default:
			}
		}
		cs.pastHeaders = false // do it all again
		return nil, nil
	}

	res.ContentLength = -1
	if clens := res.Header["Content-Length"]; len(clens) == 1 {
		if cl, err := strconv.ParseUint(clens[0], 10, 63); err == nil {
			res.ContentLength = int64(cl)
		} else {
			// TODO: care? unlike http/1, it won't mess up our framing, so it's
			// more safe smuggling-wise to ignore.
		}
	} else if len(clens) > 1 {
		// TODO: care? unlike http/1, it won't mess up our framing, so it's
		// more safe smuggling-wise to ignore.
	} else if f.StreamEnded() && !cs.isHead {
		res.ContentLength = 0
	}

	if cs.isHead {
		res.Body = noBody
		return res, nil
	}

	if f.StreamEnded() {
		if res.ContentLength > 0 && !cs.cc.t.ChromiumUpload {
			res.Body = missingBody{}
		} else {
			res.Body = noBody
		}
		return res, nil
	}

	cs.bufPipe.setBuffer(&dataBuffer{expected: res.ContentLength})
	if !cs.cc.t.ChromiumUpload {
		cs.bytesRemain = res.ContentLength
	}
	res.Body = transportResponseBody{cs}

	if cs.requestedGzip && ascii.EqualFold(res.Header.Get("Content-Encoding"), "gzip") {
		res.Header.Del("Content-Encoding")
		res.Header.Del("Content-Length")
		res.ContentLength = -1
		res.Body = compress.NewGzipReader(res.Body)
		res.Uncompressed = true
	} else if cs.cc.t.AutoDecompression {
		contentEncoding := res.Header.Get("Content-Encoding")
		if contentEncoding != "" {
			res.Header.Del("Content-Encoding")
			res.Header.Del("Content-Length")
			res.ContentLength = -1
			res.Uncompressed = true
			res.Body = compress.NewCompressReader(res.Body, contentEncoding)
		}
	}

	return res, nil
}

func (rl *clientConnReadLoop) processTrailers(cs *clientStream, f *MetaHeadersFrame) error {
	if cs.pastTrailers {
		if cs.cc.t.ChromiumUpload {
			rl.endStreamError(cs, StreamError{StreamID: cs.ID, Code: ErrCodeProtocol, Cause: errors.New("protocol error: headers after trailers")})
			return nil
		}
		// Too many HEADERS frames for this stream.
		return ConnectionError(ErrCodeProtocol)
	}
	cs.pastTrailers = true
	if !f.StreamEnded() && !cs.cc.t.ChromiumUpload {
		// We expect that any headers for trailers also
		// has END_STREAM.
		return ConnectionError(ErrCodeProtocol)
	}
	if len(f.PseudoFields()) > 0 && !cs.cc.t.ChromiumUpload {
		// No pseudo header fields are defined for trailers.
		// TODO: ConnectionError might be overly harsh? Check.
		return ConnectionError(ErrCodeProtocol)
	}

	trailer := make(http.Header)
	for _, hf := range f.RegularFields() {
		key := canonicalHeader(hf.Name)
		trailer[key] = append(trailer[key], hf.Value)
	}
	cs.trailer = trailer

	if f.StreamEnded() {
		rl.endStream(cs)
	}
	return nil
}

// transportResponseBody is the concrete type of Transport.RoundTrip's
// Response.Body. It is an io.ReadCloser.
type transportResponseBody struct {
	cs *clientStream
}

func (b transportResponseBody) Read(p []byte) (n int, err error) {
	cs := b.cs
	cc := cs.cc
	if cc.t.ChromiumUpload {
		return cs.readChromiumResponse(p)
	}

	if cs.readErr != nil {
		return 0, cs.readErr
	}
	n, err = b.cs.bufPipe.Read(p)
	if cs.bytesRemain != -1 {
		if int64(n) > cs.bytesRemain {
			n = int(cs.bytesRemain)
			if err == nil {
				err = errors.New("net/http: server replied with more than declared Content-Length; truncated")
				cs.abortStream(err)
			}
			cs.readErr = err
			return int(cs.bytesRemain), err
		}
		cs.bytesRemain -= int64(n)
		if err == io.EOF && cs.bytesRemain > 0 {
			err = io.ErrUnexpectedEOF
			cs.readErr = err
			return n, err
		}
	}
	if n == 0 {
		// No flow control tokens to send back.
		return
	}

	cc.mu.Lock()
	connAdd := cc.inflow.add(n)
	var streamAdd int32
	if err == nil { // No need to refresh if the stream is over or failed.
		streamAdd = cs.inflow.add(n)
	}
	cc.mu.Unlock()

	if connAdd != 0 || streamAdd != 0 {
		cc.wmu.Lock()
		defer cc.wmu.Unlock()
		if connAdd != 0 {
			cc.fr.WriteWindowUpdate(0, mustUint31(connAdd))
		}
		if streamAdd != 0 {
			cc.fr.WriteWindowUpdate(cs.ID, mustUint31(streamAdd))
		}
		cc.bw.Flush()
	}
	return
}

var errClosedResponseBody = errors.New("http2: response body closed")

func (b transportResponseBody) Close() error {
	cs := b.cs
	cc := cs.cc

	cs.bufPipe.BreakWithError(errClosedResponseBody)
	cs.abortStream(errClosedResponseBody)

	unread := cs.bufPipe.Len()
	if unread > 0 {
		cc.mu.Lock()
		// Return connection-level flow control.
		if cc.t.ChromiumUpload {
			var writeErr error
			if !cc.closed && cc.uploadDrainErr == nil {
				writeErr = cc.queueWindowUpdatesLocked(0, cc.inflow.add(unread), 0, false)
			}
			cc.mu.Unlock()
			cc.uploadControlError(writeErr)
		} else {
			connAdd := cc.inflow.add(unread)
			cc.mu.Unlock()

			// TODO(dneil): Acquiring this mutex can block indefinitely.
			// Move flow control return to a goroutine?
			cc.wmu.Lock()
			// Return connection-level flow control.
			if connAdd > 0 {
				cc.fr.WriteWindowUpdate(0, uint32(connAdd))
			}
			cc.bw.Flush()
			cc.wmu.Unlock()
		}
	}

	select {
	case <-cs.donec:
	case <-cs.ctx.Done():
		// See golang/go#49366: The net/http package can cancel the
		// request context after the response body is fully read.
		// Don't treat this as an error.
		return nil
	case <-cs.reqCancel:
		return common.ErrRequestCanceled
	}
	return nil
}

func (rl *clientConnReadLoop) processData(f *DataFrame) error {
	cc := rl.cc
	cs := rl.streamByID(f.StreamID)
	data := f.Data()
	if cs == nil {
		cc.mu.Lock()
		neverSent := cc.nextStreamID
		cc.mu.Unlock()
		if f.StreamID >= neverSent {
			// We never asked for this.
			cc.logf("http2: Transport received unsolicited DATA frame; closing connection")
			return ConnectionError(ErrCodeProtocol)
		}
		if cc.t.ChromiumUpload {
			return rl.processChromiumData(nil, f)
		}
		// We probably did ask for this, but canceled. Just ignore it.
		// TODO: be stricter here? only silently ignore things which
		// we canceled, but not things which were closed normally
		// by the peer? Tough without accumulating too much state.

		// But at least return their flow control:
		if f.Length > 0 {
			cc.mu.Lock()
			ok := cc.inflow.take(f.Length)
			connAdd := cc.inflow.add(int(f.Length))
			cc.mu.Unlock()
			if !ok {
				return ConnectionError(ErrCodeFlowControl)
			}
			if connAdd > 0 {
				cc.wmu.Lock()
				cc.fr.WriteWindowUpdate(0, uint32(connAdd))
				cc.bw.Flush()
				cc.wmu.Unlock()
			}
		}
		return nil
	}
	if cs.readClosed {
		cc.logf("protocol error: received DATA after END_STREAM")
		rl.endStreamError(cs, StreamError{
			StreamID: f.StreamID,
			Code:     ErrCodeProtocol,
		})
		return nil
	}
	if !cs.pastHeaders {
		cc.logf("protocol error: received DATA before a HEADERS frame")
		rl.endStreamError(cs, StreamError{
			StreamID: f.StreamID,
			Code:     ErrCodeProtocol,
		})
		return nil
	}
	if cc.t.ChromiumUpload {
		return rl.processChromiumData(cs, f)
	}
	if f.Length > 0 {
		if cs.isHead && len(data) > 0 {
			cc.logf("protocol error: received DATA on a HEAD request")
			rl.endStreamError(cs, StreamError{
				StreamID: f.StreamID,
				Code:     ErrCodeProtocol,
			})
			return nil
		}
		// Check connection-level flow control.
		cc.mu.Lock()
		if !takeInflows(&cc.inflow, &cs.inflow, f.Length) {
			cc.mu.Unlock()
			return ConnectionError(ErrCodeFlowControl)
		}
		// Return any padded flow control now, since we won't
		// refund it later on body reads.
		var refund int
		if pad := int(f.Length) - len(data); pad > 0 {
			refund += pad
		}

		didReset := false
		var err error
		if len(data) > 0 {
			if _, err = cs.bufPipe.Write(data); err != nil {
				// Return len(data) now if the stream is already closed,
				// since data will never be read.
				didReset = true
				refund += len(data)
			}
		}

		sendConn := cc.inflow.add(refund)
		var sendStream int32
		if !didReset {
			sendStream = cs.inflow.add(refund)
		}
		cc.mu.Unlock()

		if sendConn > 0 || sendStream > 0 {
			cc.wmu.Lock()
			if sendConn > 0 {
				cc.fr.WriteWindowUpdate(0, uint32(sendConn))
			}
			if sendStream > 0 {
				cc.fr.WriteWindowUpdate(cs.ID, uint32(sendStream))
			}
			cc.bw.Flush()
			cc.wmu.Unlock()
		}

		if err != nil {
			rl.endStreamError(cs, err)
			return nil
		}
	}

	if f.StreamEnded() {
		rl.endStream(cs)
	}
	return nil
}

// Chromium 的 padding 长度字节先于正文和尾部 padding 消费；
// 五秒或半窗口边界不能把这些消费合并成一次补窗。
func (cc *ClientConn) refundChromiumDataLocked(cs *clientStream, n int) error {
	if n == 0 {
		return nil
	}
	connAdd := cc.inflow.add(n)
	var streamAdd int32
	var id uint32
	if cs != nil && cs.chromiumStreamActiveLocked() {
		id, streamAdd = cs.ID, cs.inflow.add(n)
	}
	return cc.queueWindowUpdatesLocked(id, connAdd, streamAdd, true)
}

func (rl *clientConnReadLoop) processChromiumData(cs *clientStream, f *DataFrame) error {
	cc := rl.cc
	data := f.Data()
	cc.mu.Lock()
	// 官方先消费连接窗口，再检查流窗口；只超出流窗口时关闭该流，
	// 当前片段随流缓冲销毁而归还连接窗口，不影响其余活动流。
	if !cc.inflow.take(f.Length) {
		err := uploadProtocolError{ConnectionError(ErrCodeFlowControl), fmt.Sprintf("delta_window_size is %d in DecreaseRecvWindowSize, which is larger than the receive window size of %d", f.Length, cc.inflow.avail)}
		cc.mu.Unlock()
		return err
	}
	if cs != nil && !cs.inflow.take(f.Length) {
		writeErr := cs.resetFromReadLocked(StreamError{StreamID: cs.ID, Code: ErrCodeFlowControl})
		if writeErr == nil {
			writeErr = cc.refundChromiumDataLocked(nil, int(f.Length))
		}
		cc.mu.Unlock()
		return writeErr
	}
	if cs != nil && cs.pastTrailers && len(data) > 0 {
		writeErr := cs.resetFromReadLocked(StreamError{StreamID: cs.ID, Code: ErrCodeProtocol, Cause: errors.New("protocol error: DATA after trailers")})
		if writeErr == nil {
			writeErr = cc.refundChromiumDataLocked(nil, int(f.Length))
		}
		cc.mu.Unlock()
		return writeErr
	}
	padding := int(f.Length) - len(data)
	var writeErr, bodyErr error
	if f.Flags.Has(FlagDataPadded) {
		writeErr = cc.refundChromiumDataLocked(cs, 1)
		padding--
	}
	if writeErr == nil && len(data) > 0 {
		if cs != nil {
			_, bodyErr = cs.bufPipe.Write(data)
			if bodyErr == nil && cs.responseRead != nil {
				cs.responseRead.scheduleLocked()
			}
		}
		if cs == nil || bodyErr != nil {
			writeErr = cc.refundChromiumDataLocked(nil, len(data))
		}
	}
	if writeErr == nil {
		stream := cs
		if bodyErr != nil {
			stream = nil
		}
		writeErr = cc.refundChromiumDataLocked(stream, padding)
	}
	cc.mu.Unlock()
	if writeErr != nil {
		return writeErr
	}
	if bodyErr != nil {
		rl.endStreamError(cs, bodyErr)
		return nil
	}
	if cs != nil && f.StreamEnded() {
		rl.endStream(cs)
	}
	return nil
}

func (rl *clientConnReadLoop) endStream(cs *clientStream) {
	// TODO: check that any declared content-length matches, like
	// server.go's (*stream).endStream method.
	if !cs.readClosed {
		cs.readClosed = true
		// Close cs.bufPipe and cs.peerClosed with cc.mu held to avoid a
		// race condition: The caller can read io.EOF from Response.Body
		// and close the body before we close cs.peerClosed, causing
		// cleanupWriteRequest to send a RST_STREAM.
		rl.cc.mu.Lock()
		defer rl.cc.mu.Unlock()
		cs.bufPipe.closeWithErrorAndCode(io.EOF, cs.copyTrailers)
		close(cs.peerClosed)
		if cs.responseRead != nil {
			cs.responseRead.completeLocked()
		}
	}
}

func (rl *clientConnReadLoop) endStreamError(cs *clientStream, err error) {
	if se, ok := err.(StreamError); rl.cc.t.ChromiumUpload && ok && se.Cause != errFromPeer {
		rl.cc.mu.Lock()
		writeErr := cs.resetFromReadLocked(se)
		rl.cc.mu.Unlock()
		rl.cc.uploadControlError(writeErr)
		return
	}
	cs.readAborted = true
	cs.abortStream(err)
}

func (rl *clientConnReadLoop) streamByID(id uint32) *clientStream {
	rl.cc.mu.Lock()
	defer rl.cc.mu.Unlock()
	cs := rl.cc.streams[id]
	if cs != nil && !cs.readAborted {
		return cs
	}
	return nil
}

func (cs *clientStream) copyTrailers() {
	for k, vv := range cs.trailer {
		t := cs.resTrailer
		if *t == nil {
			*t = make(http.Header)
		}
		(*t)[k] = vv
	}
}

func (rl *clientConnReadLoop) processGoAway(f *GoAwayFrame) error {
	cc := rl.cc
	cc.t.connPool().MarkDead(cc)
	if f.ErrCode != 0 {
		// TODO: deal with GOAWAY more. particularly the error code
		cc.vlogf("transport got GOAWAY with error code = %v", f.ErrCode)
		if fn := cc.t.CountError; fn != nil {
			fn("recv_goaway_" + f.ErrCode.stringToken())
		}
	}
	cc.setGoAway(f)
	return nil
}

func (rl *clientConnReadLoop) processSettings(f *SettingsFrame) error {
	cc := rl.cc
	if cc.t.ChromiumUpload {
		// Framer 会复用下一帧的缓冲，异步事件必须持有自己的 SETTINGS。
		owned := *f
		owned.p = bytes.Clone(f.p)
		cc.mu.Lock()
		defer cc.mu.Unlock()
		return cc.addUploadEventLocked(uploadEvent{settings: &owned, fromRead: true})
	}
	// Locking both mu and wmu here allows frame encoding to read settings with only wmu held.
	// Acquiring wmu when f.IsAck() is unnecessary, but convenient and mostly harmless.
	cc.wmu.Lock()
	defer cc.wmu.Unlock()

	if err := rl.processSettingsNoWrite(f); err != nil {
		return err
	}
	if !f.IsAck() {
		cc.fr.WriteSettingsAck()
		cc.bw.Flush()
	}
	return nil
}

func (rl *clientConnReadLoop) processSettingsNoWrite(f *SettingsFrame) error {
	cc := rl.cc
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return rl.processSettingsLocked(f)
}

func (rl *clientConnReadLoop) processSettingsLocked(f *SettingsFrame) error {
	cc := rl.cc

	if f.IsAck() {
		if cc.wantSettingsAck {
			cc.wantSettingsAck = false
			return nil
		}
		return ConnectionError(ErrCodeProtocol)
	}

	var seenMaxConcurrentStreams bool
	err := f.ForeachSetting(func(s http2.Setting) error {
		switch s.ID {
		case http2.SettingHeaderTableSize:
			if cc.t.RespectHeaderTableSize {
				// ChromiumUpload 的编码与表大小变更都持 mu；设置不写 socket，
				// 实际 table-size-update 仍由编码器在下一头块发出。
				cc.henc.SetMaxDynamicTableSizeLimit(s.Val)
				cc.henc.SetMaxDynamicTableSize(s.Val)
			}
		case http2.SettingMaxFrameSize:
			if !cc.t.ChromiumUpload {
				cc.maxFrameSize = s.Val
			}
		case http2.SettingMaxConcurrentStreams:
			cc.maxConcurrentStreams = s.Val
			seenMaxConcurrentStreams = true
		case http2.SettingMaxHeaderListSize:
			cc.peerMaxHeaderListSize = uint64(s.Val)
		case http2.SettingInitialWindowSize:
			// Values above the maximum flow-control
			// window size of 2^31-1 MUST be treated as a
			// connection error (Section 5.4.1) of type
			// FLOW_CONTROL_ERROR.
			if s.Val > math.MaxInt32 {
				if cc.t.ChromiumUpload {
					return nil
				}
				return ConnectionError(ErrCodeFlowControl)
			}

			// Adjust flow control of currently-open
			// frames by the difference of the old initial
			// window size and this one.
			delta := int32(s.Val) - int32(cc.initialWindowSize)
			if cc.t.ChromiumUpload {
				// 官方 active_streams_ 按流编号遍历，溢出报告不能依赖 Go map 顺序。
				ids := make([]uint32, 0, len(cc.streams))
				for id := range cc.streams {
					ids = append(ids, id)
				}
				sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
				for _, id := range ids {
					if !cc.streams[id].flow.add(delta) {
						return uploadProtocolError{ConnectionError(ErrCodeFlowControl), fmt.Sprintf("New spdy::SETTINGS_INITIAL_WINDOW_SIZE value overflows flow control window of stream %d.", id)}
					}
				}
			} else {
				for _, cs := range cc.streams {
					cs.flow.add(delta)
				}
			}
			cc.cond.Broadcast()

			cc.initialWindowSize = s.Val
		default:
			cc.vlogf("Unhandled Setting: %v", s)
		}
		return nil
	})
	if err != nil {
		return err
	}

	cc.resumeUploadsLocked(nil)
	if !cc.seenSettings {
		if !seenMaxConcurrentStreams {
			// This was the servers initial SETTINGS frame and it
			// didn't contain a MAX_CONCURRENT_STREAMS field so
			// increase the number of concurrent streams this
			// connection can establish to our default.
			cc.maxConcurrentStreams = defaultMaxConcurrentStreams
		}
		cc.seenSettings = true
	}

	return nil
}

func (rl *clientConnReadLoop) processWindowUpdate(f *WindowUpdateFrame) error {
	cc := rl.cc
	cs := rl.streamByID(f.StreamID)
	if f.StreamID != 0 && cs == nil {
		return nil
	}

	cc.mu.Lock()
	defer cc.mu.Unlock()

	// Chromium 的同步写循环会排空当前可写队列，再处理下一次 socket 读取。
	// TLS 真正 pending 时事件循环已经让出，补窗可以恢复其他流。
	if cc.t.ChromiumUpload {
		return cc.addUploadEventLocked(uploadEvent{cs: cs, increment: int32(f.Increment), fromRead: true})
	}

	fl := &cc.flow
	if cs != nil {
		fl = &cs.flow
	}
	if !fl.add(int32(f.Increment)) {
		// For stream, the sender sends RST_STREAM with an error code of FLOW_CONTROL_ERROR
		if cs != nil {
			cs.readAborted = true
			cs.abortStreamLocked(StreamError{
				StreamID: f.StreamID,
				Code:     ErrCodeFlowControl,
			})
			return nil
		}

		return ConnectionError(ErrCodeFlowControl)
	}
	cc.resumeUploadsLocked(cs)
	cc.cond.Broadcast()
	return nil
}

func (rl *clientConnReadLoop) processResetStream(f *RSTStreamFrame) error {
	cs := rl.streamByID(f.StreamID)
	if cs == nil {
		// TODO: return error if server tries to RST_STREAM an idle stream
		return nil
	}
	serr := streamError(cs.ID, f.ErrCode)
	serr.Cause = errFromPeer
	if f.ErrCode == ErrCodeProtocol {
		rl.cc.SetDoNotReuse()
	}
	if fn := cs.cc.t.CountError; fn != nil {
		fn("recv_rststream_" + f.ErrCode.stringToken())
	}
	cs.abortStream(serr)

	cs.bufPipe.CloseWithError(serr)
	return nil
}

// Ping sends a PING frame to the server and waits for the ack.
func (cc *ClientConn) Ping(ctx context.Context) error {
	c := make(chan struct{})
	// Generate a random payload
	var p [8]byte
	for {
		if _, err := rand.Read(p[:]); err != nil {
			return err
		}
		cc.mu.Lock()
		// check for dup before insert
		if _, found := cc.pings[p]; !found {
			cc.pings[p] = c
			cc.mu.Unlock()
			break
		}
		cc.mu.Unlock()
	}
	var pingError error
	errc := make(chan struct{})
	go func() {
		cc.wmu.Lock()
		defer cc.wmu.Unlock()
		if pingError = cc.fr.WritePing(false, p); pingError != nil {
			close(errc)
			return
		}
		if pingError = cc.bw.Flush(); pingError != nil {
			close(errc)
			return
		}
	}()
	select {
	case <-c:
		return nil
	case <-errc:
		return pingError
	case <-ctx.Done():
		return ctx.Err()
	case <-cc.readerDone:
		// connection closed
		return cc.readerErr
	}
}

func (rl *clientConnReadLoop) processPing(f *PingFrame) error {
	if f.IsAck() {
		cc := rl.cc
		cc.mu.Lock()
		defer cc.mu.Unlock()
		if cc.t.ChromiumUpload && len(cc.pings) == 0 {
			return uploadProtocolError{ConnectionError(ErrCodeProtocol), "Unexpected PING ACK."}
		}
		// If ack, notify listener if any
		if c, ok := cc.pings[f.Data]; ok {
			close(c)
			delete(cc.pings, f.Data)
		}
		return nil
	}
	cc := rl.cc
	if cc.t.ChromiumUpload {
		data := f.Data
		cc.mu.Lock()
		defer cc.mu.Unlock()
		return cc.addUploadEventLocked(uploadEvent{control: func() error { return cc.fr.WritePing(true, data) }, fromRead: true})
	}
	cc.wmu.Lock()
	defer cc.wmu.Unlock()
	if err := cc.fr.WritePing(true, f.Data); err != nil {
		return err
	}
	return cc.bw.Flush()
}

func (rl *clientConnReadLoop) processPushPromise(f *PushPromiseFrame) error {
	// We told the peer we don't want them.
	// Spec says:
	// "PUSH_PROMISE MUST NOT be sent if the SETTINGS_ENABLE_PUSH
	// setting of the peer endpoint is set to 0. An endpoint that
	// has set this setting and has received acknowledgement MUST
	// treat the receipt of a PUSH_PROMISE frame as a connection
	// error (Section 5.4.1) of type PROTOCOL_ERROR."
	code := ConnectionError(ErrCodeProtocol)
	if rl.cc.t.ChromiumUpload {
		return uploadProtocolError{code, "PUSH_PROMISE received"}
	}
	return code
}

func (cs *clientStream) resetFromReadLocked(err StreamError) error {
	cs.readAborted = true
	// 先排入网络序列，再通知调用方错误，避免后续读取越过未投递的 RST。
	writeErr := cs.queueStreamResetLocked(err.Code, true)
	cs.abortStreamLocked(err)
	return writeErr
}

func (cs *clientStream) queueStreamResetLocked(code ErrCode, fromRead bool) error {
	cc := cs.cc
	if cs.resetQueued || cc.closed || cc.uploadDrainErr != nil {
		return nil
	}
	// reset 属于会话写入，流清理后仍保留在常规优先级队列。
	err := cc.addUploadEventLocked(uploadEvent{normal: true, fromRead: fromRead, control: func() error {
		return cc.fr.WriteRSTStream(cs.ID, code)
	}})
	if err == nil {
		cs.resetQueued = true
	}
	return err
}

func (cs *clientStream) writeStreamReset(code ErrCode) {
	cc := cs.cc
	if cc.t.ChromiumUpload {
		cc.mu.Lock()
		err := cs.queueStreamResetLocked(code, false)
		cc.mu.Unlock()
		cc.uploadControlError(err)
		return
	}
	cc.wmu.Lock()
	cc.fr.WriteRSTStream(cs.ID, code)
	cc.bw.Flush()
	cc.wmu.Unlock()
}

var (
	errResponseHeaderListSize = errors.New("http2: response header list larger than advertised limit")
	errRequestHeaderListSize  = errors.New("http2: request header list larger than peer's advertised limit")
)

func (cc *ClientConn) logf(format string, args ...any) {
	cc.t.logf(format, args...)
}

func (cc *ClientConn) vlogf(format string, args ...any) {
	cc.t.vlogf(format, args...)
}

func (t *Transport) vlogf(format string, args ...any) {
	if VerboseLogs {
		t.logf(format, args...)
	}
}

func (t *Transport) logf(format string, args ...any) {
	log.Printf(format, args...)
}

var noBody io.ReadCloser = noBodyReader{}

type noBodyReader struct{}

func (noBodyReader) Close() error             { return nil }
func (noBodyReader) Read([]byte) (int, error) { return 0, io.EOF }

type missingBody struct{}

func (missingBody) Close() error { return nil }

func (missingBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func strSliceContains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

type erringRoundTripper struct{ err error }

func (rt erringRoundTripper) RoundTripErr() error { return rt.err }

func (rt erringRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, rt.err
}

// isConnectionCloseRequest reports whether req should use its own
// connection for a single request and then close the connection.
func isConnectionCloseRequest(req *http.Request) bool {
	return req.Close || httpguts.HeaderValuesContainsToken(req.Header["Connection"], "close")
}

// noDialH2RoundTripper is a RoundTripper which only tries to complete the request
// if there's already has a cached connection to the host.
// (The field is exported so it can be accessed via reflect from net/http; tested
// by TestNoDialH2RoundTripperType)
type noDialH2RoundTripper struct{ *Transport }

func (rt noDialH2RoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := rt.Transport.RoundTrip(req)
	if IsNoCachedConnError(err) {
		return nil, http.ErrSkipAltProtocol
	}
	return res, err
}
