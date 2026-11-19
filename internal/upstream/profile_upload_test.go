// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package upstream

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

type profileUploadTestSource struct {
	io.Reader
	closeFn func() error
}

func (b profileUploadTestSource) Close() error { return b.closeFn() }

func TestProfileStreamUploadCancellation(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeCode} {
		for _, full := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/full=%t", httpProfileName(profile, false), full), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancel()
				reader, writer := io.Pipe()
				defer writer.Close()
				var closes atomic.Int32
				body := newProfileStreamUploadBody(ctx, profileUploadTestSource{Reader: reader, closeFn: func() error {
					closes.Add(1)
					return reader.Close()
				}}, profile)
				defer body.Close()
				if full {
					// 先启动上传读取，再检查窗口阻塞期间的独立生产与背压。
					body.ReadAvailable(make([]byte, 1))
					// Work 最多 64 KiB 队列加一块生产缓冲；TTNet 是三条队列加一块。
					chunks := 2
					if profile == ProfileTraeCode {
						chunks = 4
					}
					for range chunks {
						if _, err := io.WriteString(writer, strings.Repeat("u", 64<<10)); err != nil {
							t.Fatal(err)
						}
					}
					blocked := make(chan error, 1)
					go func() { _, err := io.WriteString(writer, "must-wait"); blocked <- err }()
					select {
					case err := <-blocked:
						t.Fatalf("缓冲未施加背压：%v", err)
					case <-time.After(30 * time.Millisecond):
					}
					cancel()
					if err := <-blocked; err == nil {
						t.Fatal("取消后生产者仍写入成功")
					}
				} else {
					cancel()
				}
				select {
				case <-body.done:
				case <-time.After(time.Second):
					t.Fatal("取消后生产 goroutine 没有退出")
				}
				if _, err := body.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
					t.Fatalf("取消原因丢失：%v", err)
				}
				if err := body.Close(); err != nil || closes.Load() != 1 {
					t.Fatalf("正文未恰好关闭一次：次数=%d，err=%v", closes.Load(), err)
				}
			})
		}
	}
}

func TestProfileWorkUploadWaitsForFirstRead(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		reader, writer := io.Pipe()
		body := newProfileStreamUploadBody(ctx, reader, ProfileTraeWork)
		written := make(chan error, 1)
		go func() { _, err := io.WriteString(writer, "pending"); written <- err }()
		select {
		case err := <-written:
			t.Fatalf("排队请求提前消费了正文：%v", err)
		case <-time.After(30 * time.Millisecond):
		}
		if canceled {
			cancel()
			if err := <-written; err == nil {
				t.Fatal("排队取消后正文仍被消费")
			}
		} else {
			buf := make([]byte, 7)
			if n, err := body.Read(buf); err != nil || n != len(buf) || string(buf) != "pending" {
				t.Fatalf("取得上传槽位后读取失败：n=%d err=%v", n, err)
			}
			if err := <-written; err != nil {
				t.Fatal(err)
			}
		}
		body.Close()
		writer.Close()
		cancel()
	}
}

func TestProfileAuthUploadFailureBeforeDial(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			reader, writer := io.Pipe()
			defer writer.Close()
			failure := errors.New("fixture upload read failure")
			var closes, dials atomic.Int32
			source := profileUploadTestSource{Reader: reader, closeFn: func() error {
				closes.Add(1)
				return reader.Close()
			}}
			base := newTransport(nil)
			base.DialContext = func(context.Context, string, string) (net.Conn, error) {
				dials.Add(1)
				return nil, errors.New("不应开始拨号")
			}
			tr := newProfileHTTP2(base, ProfileTraeAuth)
			defer tr.CloseIdleConnections()
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://fixture.invalid/upload", source)
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() { _, err := tr.RoundTrip(request); result <- err }()
			if _, err := io.WriteString(writer, "partial-body"); err != nil {
				t.Fatal(err)
			}
			want := failure
			if canceled {
				cancel()
				want = context.Canceled
			} else if err := writer.CloseWithError(failure); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if !errors.Is(err, want) {
					t.Fatalf("正文错误被覆盖：%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("取消或读取失败后授权请求仍未退出")
			}
			if closes.Load() != 1 || dials.Load() != 0 {
				t.Fatalf("失败正文仍开始请求或关闭次数错误：close=%d dial=%d", closes.Load(), dials.Load())
			}
		})
	}
}

func TestProfileStreamUploadDialFailureClosesSource(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeCode} {
		t.Run(httpProfileName(profile, false), func(t *testing.T) {
			reader, writer := io.Pipe()
			defer writer.Close()
			var closes atomic.Int32
			failure := errors.New("fixture dial failure")
			base := newTransport(nil)
			base.DialContext = func(context.Context, string, string) (net.Conn, error) { return nil, failure }
			tr := newProfileHTTP2(base, profile)
			defer tr.CloseIdleConnections()
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://fixture.invalid/upload", profileUploadTestSource{Reader: reader, closeFn: func() error {
				closes.Add(1)
				return reader.Close()
			}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = tr.RoundTrip(request)
			if !errors.Is(err, failure) || closes.Load() != 1 {
				t.Fatalf("拨号失败未关闭正文或错误原因丢失：close=%d err=%v", closes.Load(), err)
			}
		})
	}
}

// 长度与结束标志来自两轮官方逐块上传；暖连接先消耗两个字节的连接窗口。
func TestProfileUnknownLengthUploadWire(t *testing.T) {
	for _, profile := range []ClientProfile{ProfileTraeWork, ProfileTraeAuth, ProfileTraeCode} {
		t.Run(httpProfileName(profile, false), func(t *testing.T) {
			ln := profileDynamicListener(t)
			done := make(chan error, 1)
			go func() {
				done <- func() error {
					conn, err := ln.Accept()
					if err != nil {
						return err
					}
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
					if _, err := io.CopyN(io.Discard, conn, int64(len(http2.ClientPreface))); err != nil {
						return err
					}
					fr := http2.NewFramer(conn, conn)
					fr.ReadMetaHeaders = hpack.NewDecoder(65536, nil)
					if err := fr.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 32768}); err != nil {
						return err
					}
					completed, connectionWindow := 0, 65535
					windows := map[uint32]int{}
					bodies := map[uint32][]byte{}
					var lengths []int
					for completed < 3 {
						frame, err := fr.ReadFrame()
						if err != nil {
							return err
						}
						switch f := frame.(type) {
						case *http2.SettingsFrame:
							if !f.IsAck() {
								if err := fr.WriteSettingsAck(); err != nil {
									return err
								}
							}
						case *http2.MetaHeadersFrame:
							windows[f.StreamID] = 32768
							if completed == 1 {
								length := ""
								for _, field := range f.RegularFields() {
									if field.Name == "content-length" {
										length = field.Value
									}
								}
								want := ""
								if profile == ProfileTraeAuth {
									want = "81877"
								}
								if length != want {
									return fmt.Errorf("未知长度正文线上 content-length=%q，期望 %q", length, want)
								}
							}
						case *http2.DataFrame:
							n := len(f.Data())
							connectionWindow -= n
							windows[f.StreamID] -= n
							if connectionWindow < 0 || windows[f.StreamID] < 0 {
								return fmt.Errorf("上传超出服务端窗口")
							}
							bodies[f.StreamID] = append(bodies[f.StreamID], f.Data()...)
							if completed == 1 {
								lengths = append(lengths, n)
								if f.StreamEnded() != (len(bodies[f.StreamID]) == 81877) {
									return fmt.Errorf("结束帧不匹配官方：已收 %d 字节，DATA=%d，end=%t", len(bodies[f.StreamID]), n, f.StreamEnded())
								}
							}
							if !f.StreamEnded() && min(connectionWindow, windows[f.StreamID]) == 0 {
								// 使后续生产块和完成通知在补窗前到达。
								time.Sleep(80 * time.Millisecond)
								if connectionWindow == 0 {
									if err := fr.WriteWindowUpdate(0, 65535); err != nil {
										return err
									}
									connectionWindow = 65535
								}
								if windows[f.StreamID] == 0 {
									if err := fr.WriteWindowUpdate(f.StreamID, 32768); err != nil {
										return err
									}
									windows[f.StreamID] = 32768
								}
							}
							if !f.StreamEnded() {
								continue
							}
							want := "{}"
							if completed == 1 {
								want = strings.Repeat("u", 81877)
								wantLengths := []int{1, 16374, 16375, 18, 16357, 16375, 33, 3, 16339, 2}
								if profile == ProfileTraeAuth {
									wantLengths = wantLengths[1:]
									wantLengths[0] = 16375
								}
								if !slices.Equal(lengths, wantLengths) {
									return fmt.Errorf("DATA 长度=%v，官方为 %v", lengths, wantLengths)
								}
							}
							if string(bodies[f.StreamID]) != want {
								return fmt.Errorf("第 %d 次上传正文不完整", completed+1)
							}
							completed++
							if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: f.StreamID, EndHeaders: true, BlockFragment: []byte{0x88}}); err != nil {
								return err
							}
							if err := fr.WriteData(f.StreamID, true, []byte("{}")); err != nil {
								return err
							}
						}
					}
					return nil
				}()
			}()
			base := newTransport(nil)
			base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			tr := newProfileHTTP2(base, profile)
			defer tr.CloseIdleConnections()
			client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
			reader, writer := io.Pipe()
			producer := make(chan error, 1)
			go func() {
				for _, size := range []int{1, 16374, 16375, 16376, 32751} {
					time.Sleep(25 * time.Millisecond)
					if _, err := io.WriteString(writer, strings.Repeat("u", size)); err != nil {
						producer <- err
						return
					}
				}
				time.Sleep(25 * time.Millisecond)
				producer <- writer.Close()
			}()
			defer reader.Close()
			for _, body := range []io.Reader{strings.NewReader("{}"), reader, strings.NewReader("{}")} {
				response, err := client.Post("https://"+ln.Addr().String()+"/fixture", "text/plain", body)
				if err != nil {
					select {
					case serverErr := <-done:
						t.Fatalf("逐块上传失败：%v；假上游：%v", err, serverErr)
					default:
						t.Fatal(err)
					}
				}
				reply, err := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if err != nil || string(reply) != "{}" {
					t.Fatalf("上传响应不完整：%s: %v", reply, err)
				}
			}
			if err := <-producer; err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProfileTTNetUploadPendingRemainder(t *testing.T) {
	for _, ending := range []string{"data", "eof", "error", "cancel"} {
		t.Run(ending, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			reader, writer := io.Pipe()
			defer writer.Close()
			var closes atomic.Int32
			body := newProfileStreamUploadBody(ctx, profileUploadTestSource{Reader: reader, closeFn: func() error { closes.Add(1); return reader.Close() }}, ProfileTraeCode)
			defer body.Close()
			if _, err := io.WriteString(writer, strings.Repeat("u", 16384)); err != nil {
				t.Fatal(err)
			}
			first := make([]byte, 16375)
			if n, err := body.Read(first); n != len(first) || err != nil || string(first) != strings.Repeat("u", len(first)) {
				t.Fatalf("首块损坏：n=%d err=%v", n, err)
			}
			type result struct {
				n    int
				err  error
				data string
			}
			done := make(chan result, 1)
			go func() { p := make([]byte, 16375); n, err := body.Read(p); done <- result{n, err, string(p[:n])} }()
			select {
			case row := <-done:
				t.Fatalf("TTNet 将残片提前单发：n=%d err=%v", row.n, row.err)
			case <-time.After(30 * time.Millisecond):
			}
			failure := errors.New("fixture remaining upload failure")
			switch ending {
			case "data":
				if _, err := io.WriteString(writer, strings.Repeat("v", 16384)); err != nil {
					t.Fatal(err)
				}
			case "eof":
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
			case "error":
				if err := writer.CloseWithError(failure); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				cancel()
			}
			select {
			case row := <-done:
				switch ending {
				case "data":
					if row.n != 16375 || row.err != nil || row.data != strings.Repeat("u", 9)+strings.Repeat("v", 16366) {
						t.Fatalf("残片与下一生产块没有连贯：%+v", row)
					}
				case "eof":
					if row.n != 9 || row.err != io.EOF || row.data != strings.Repeat("u", 9) {
						t.Fatalf("最后残片没有保留完成通知：%+v", row)
					}
				case "error":
					if row.n != 9 || !errors.Is(row.err, failure) || !strings.Contains(row.err.Error(), "读取上传正文") {
						t.Fatalf("残片后的读取错误丢失：n=%d err=%v", row.n, row.err)
					}
				case "cancel":
					if row.n != 0 || !errors.Is(row.err, context.Canceled) {
						t.Fatalf("取消后仍输出残片：n=%d err=%v", row.n, row.err)
					}
				}
			case <-time.After(time.Second):
				t.Fatal("读取残片未被数据、完成、错误或取消唤醒")
			}
			if err := body.Close(); err != nil || closes.Load() != 1 {
				t.Fatalf("源没有恰好关闭一次：次数=%d err=%v", closes.Load(), err)
			}
		})
	}
}

func TestProfileTTNetReadyUploadStillCompletesAsynchronously(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	body := newProfileStreamUploadBody(t.Context(), reader, ProfileTraeCode)
	defer body.Close()
	for _, text := range []string{strings.Repeat("u", 16384), strings.Repeat("v", 16384)} {
		if _, err := io.WriteString(writer, text); err != nil {
			t.Fatal(err)
		}
	}
	body.mu.Lock()
	for len(body.chunks) < 2 {
		body.cond.Wait()
	}
	body.mu.Unlock()
	buf := make([]byte, 16375)
	if n, err, ready := body.ReadAvailable(buf); n != 0 || err != nil || ready {
		t.Fatalf("TTNet 的已到达消息绕过了 Cronet 异步读取完成：n=%d err=%v ready=%t", n, err, ready)
	}
	if n, err := body.Read(buf); n != len(buf) || err != nil || string(buf) != strings.Repeat("u", len(buf)) {
		t.Fatalf("异步读取前正文被消费：n=%d err=%v", n, err)
	}
}
