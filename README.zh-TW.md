[English](./README.md) · **繁體中文**

# WLGOWS

[![Go Reference](https://pkg.go.dev/badge/github.com/weilun-shrimp/wlgows/v7.svg)](https://pkg.go.dev/github.com/weilun-shrimp/wlgows/v7)
[![Autobahn](https://img.shields.io/badge/Autobahn-0%20failures%2C%20517%20cases-brightgreen)](https://weilun-shrimp.github.io/wlgows/zh-TW/)
[![Master CI](https://github.com/weilun-shrimp/wlgows/actions/workflows/master_ci.yml/badge.svg?branch=master)](https://github.com/weilun-shrimp/wlgows/actions/workflows/master_ci.yml)
![Statement coverage](https://img.shields.io/badge/Statement%20coverage-97.6%25-brightgreen)

高效能的開源 Go WebSocket 函式庫。為速度與記憶體效率而打造，且不犧牲安全與正確性。
容易上手、功能豐富，經過充分測試，文件完善。在幾乎所有
[benchmark](./BENCHMARK_COMPARISON.zh-TW.md) 中，表現都優於 gorilla/websocket。
而且不隱藏 WebSocket 的運作方式。

下方的[快速開始](#快速開始)是一個能執行的 echo server。

**安全與正確性優先。** wlgows 遵循 RFC 6455 與 RFC 7692，兩端的
[Autobahn](./example/AUTOBAHN.zh-TW.md#我們的結果) 測試都是 0 失敗。我們追求最快的
程式與最少的記憶體，但前提是安全且正確：不用 `unsafe`，也不跳過
bounds check。如果你清楚自己在做什麼，你的程式可以更進一步。見
[更快、更省記憶體](./SENDING.zh-TW.md#更快更省記憶體用-unsafe-送出-string)。

## 功能

- **WebSocket Server**：掛在 `net/http` 或 Gin 之下，或在你清楚自己在做什麼時直接用原始 TCP
- **WebSocket Client**：支援 `ws://` 與 `wss://`
- **TLS/SSL**：兩端皆支援
- **Subprotocols**：兩端都能提出、選擇或要求一個
- **HTTP Hijacking**：支援 `http.Server` 與 Gin
- **Listener**：單一 read loop，依 RFC 6455 驗證每個 frame 後交給你的 hook
- **Frame 層級控制**：需要時可自行組出 frame 並送出
- **Streaming**：以一個個 frame 送出或讀取比記憶體還大的 message
- **壓縮**：permessage-deflate（RFC 7692），兩端皆支援。見 [PERMESSAGE_DEFLATE.zh-TW.md](./PERMESSAGE_DEFLATE.zh-TW.md)
- **記憶體由你掌控**：每條連線的 write buffer（最小 14 byte，隨時可以更換）與 read buffer（最小 16 byte）都由你決定。見 [SENDING.zh-TW.md](./SENDING.zh-TW.md#掌控權在你手上) 與 [READING.zh-TW.md](./READING.zh-TW.md#記憶體連線的-read-buffer)
- **記憶體保護**：用 `MaxFrameSize` 與 `MaxDataFramesSize` 限制對方能送來的 byte，用 `NewSizeLimitReader` 限制解壓縮後的大小。見 [READING.zh-TW.md](./READING.zh-TW.md#限制大小)
- **Keepalive**：定期發送 ping，每次的 payload 由你決定
- **並行傳送**：以 lock 保護，且 control frame 不會被卡在大型 message
  後面

還有更多，見[說明文件](#說明文件)。

## 目錄

- [安裝](#安裝)
- [緣起與為什麼是 v7](#緣起與為什麼是-v7)
- [目標](#目標)
- [設計理念](#設計理念)
- [快速開始](#快速開始) — [Server](#server) · [Client](#client)
- [說明文件](#說明文件) — server、client、讀取、Listener、傳送、壓縮、benchmark
- [範例](#範例) — 三個 echo server、一個 client，以及一組 streaming 範例
- [測試](#測試)
- [授權](#授權)
- [從 v6 升級](#從-v6-升級)

## 安裝

```bash
go get github.com/weilun-shrimp/wlgows/v7
```

依照 Go 對 major version 2 以上的規定，import path 需要加上 `/v7` 後綴；package
名稱仍然是 `wlgows`，所以程式中的寫法依然是 `wlgows.DialContext(...)`。

### v7 是長期支援（LTS）版本

見[緣起與為什麼是 v7](#緣起與為什麼是-v7)。

## 緣起與為什麼是 v7

很多函式庫把細節藏起來，替你做決定。這樣很難知道底下發生了什麼事，有時甚至沒辦法
客製。我不喜歡這樣，所以開始了 wlgows 這個練習專案。唯一的使用者是我自己，只用在
我私人的專案裡。每個版本都依那些專案的需要修改 API，所以常常出現不相容的改動。
這就是版本號這麼大的原因。

到了 v6，我發現 wlgows 幾乎已經符合 RFC 6455 的所有規範。我也發現它還能更進一步：
和其他熱門的 Go WebSocket 函式庫相比，它有機會成為最快、最省記憶體、最正確的，
也更容易使用和理解。所以 v7 是目前最後一次重新打造，目標就是這個。

這也是 v7 成為長期支援（LTS）版本的原因。目前我沒有 v8 的計畫，至少短期內
不會。修正與新功能都會放進 v7。

## 目標

- 最快的程式、最少的記憶體。
- 永遠正確，遵循 RFC 6455 與 RFC 7692。
- 依然容易學、容易用。

## 設計理念

我認為開發應該是自由的，而不是被函式庫限制住。開發者應該要能自己做取捨。所以
wlgows 從不替你做決定。從低階到高階，幾乎所有東西你都可以控制：

- 原始連線
- I/O buffer
- handshake
- 每一個 frame
- 每一則 message
- keepalive ping
- 還有更多，見[說明文件](#說明文件)

幾乎每個函式都是 helper，幫你完成 WebSocket 的其中一個步驟。wlgows 自己內部也是用
這些 helper 組成的，每一層都是。所以每個步驟你都可以自己來，做法和 wlgows 一樣，
你也永遠知道發生了什麼事。

## 快速開始

### Server

```go
package main

import (
	"io"
	"log"
	"net/http"
	"time"

	"github.com/weilun-shrimp/wlgows/v7"
)

const maxMessageSize = 10 * 1024 * 1024 // 10 MB

// readMessage 回傳一則 message 的內容，最多 maxMessageSize byte。壓縮過的
// message 會先解壓縮，text 會檢查是不是 UTF-8。message 超過 maxMessageSize 時，
// 回傳 ErrSizeLimitExceeded。見 READING.zh-TW.md。
func readMessage(conn *wlgows.Conn, frames wlgows.DataFrames) ([]byte, error) {
	reader, err := conn.DataFramesReader(frames)
	if err != nil {
		return nil, err
	}
	defer reader.Close() // 一定要 Close
	return io.ReadAll(wlgows.NewSizeLimitReader(reader, maxMessageSize))
}

func main() {
	server := &http.Server{
		Addr:              ":8001",
		Handler:           http.HandlerFunc(handle),
		ReadHeaderTimeout: 10 * time.Second, // net/http 預設不設逾時
		IdleTimeout:       60 * time.Second, // 保持連線時，等待下一個 request
		MaxHeaderBytes:    16 * 1024,        // 預設 1 MB；handshake 只有幾百 byte
	}
	log.Fatal(server.ListenAndServe())
}

func handle(w http.ResponseWriter, r *http.Request) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "could not hijack connection", http.StatusInternalServerError)
		return
	}
	netConn, bufRW, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, "could not hijack connection", http.StatusInternalServerError)
		return
	}
	defer netConn.Close()

	// 要傳 bufRW.Reader：net/http 可能已經把第一個 frame 的開頭讀進去了。
	conn, _, err := wlgows.ServerHandShake(netConn, bufRW.Reader, bufRW.Writer, r, wlgows.ServerHandShakeConfig{})
	if err != nil {
		return
	}

	// ping、pong、close 與保留 opcode 都已處理好。
	listener := conn.NewStandardListener()

	// SetConfig 會整份替換設定，所以請從標準 hook 設好的那份開始修改。
	config := listener.GetConfig()
	config.MaxDataFramesSize = maxMessageSize
	config.MaxFrameSize = maxMessageSize
	config.MaxDataFrameCount = maxMessageSize / 256
	config.Text = func(frames wlgows.DataFrames) {
		message, err := readMessage(conn, frames)
		if err != nil {
			listener.PauseListen(err) // Listen 會回傳 err
			return
		}
		conn.SendText(message, wlgows.SendDataConfig{}) // echo
	}
	listener.SetConfig(config)

	// 心跳，在獨立的 goroutine 中執行，連線結束時會自動停止。若要偵測
	// 「連線還在卻一直不回 pong」的對方，請見 LISTENER.zh-TW.md。
	go conn.StartPingLoop(30*time.Second, nil)

	if err := listener.Listen(); err != nil {
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		log.Println("closing:", err)
	}
}
```

Gin 之下做法相同，改用 `c.Writer.Hijack()` 與 `c.Request`。因為 Gin 的 `Run` 不設逾時，
請用你自己的 `http.Server` 來提供 Gin engine。見
[HTTP Hijacking](./SERVER.zh-TW.md#http-hijacking)。

### Client

```go
package main

import (
	"bufio"
	"context"
	"io"
	"log"
	"net"
	"time"

	"github.com/weilun-shrimp/wlgows/v7"
)

const maxMessageSize = 10 * 1024 * 1024 // 10 MB

// readMessage 回傳一則 message 的內容，最多 maxMessageSize byte。壓縮過的
// message 會先解壓縮，text 會檢查是不是 UTF-8。message 超過 maxMessageSize 時，
// 回傳 ErrSizeLimitExceeded。見 READING.zh-TW.md。
func readMessage(conn *wlgows.Conn, frames wlgows.DataFrames) ([]byte, error) {
	reader, err := conn.DataFramesReader(frames)
	if err != nil {
		return nil, err
	}
	defer reader.Close() // 一定要 Close
	return io.ReadAll(wlgows.NewSizeLimitReader(reader, maxMessageSize))
}

func main() {
	// DialContext 只負責建立連線並組好 request。handshake 由你自己執行，所以送出前
	// 還可以替 `request` 加上 header。ctx 與 dialer 都會限制 TCP 連線與 TLS
	// handshake 的時間，以較短的為準。見 CLIENT.zh-TW.md。
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	netConn, request, err := wlgows.DialContext(ctx, "ws://localhost:8001", dialer, nil)
	if err != nil {
		panic(err)
	}
	defer netConn.Close()

	// reader 與 writer 每條連線各佔 4096 byte；想縮小請見 READING.zh-TW.md 與 SENDING.zh-TW.md。
	conn, _, err := wlgows.ClientHandShake(netConn, bufio.NewReader(netConn), bufio.NewWriter(netConn), request, wlgows.ClientHandShakeConfig{})
	if err != nil {
		panic(err)
	}

	listener := conn.NewStandardListener()

	config := listener.GetConfig() // 標準 hook 設好的那份
	config.MaxDataFramesSize = maxMessageSize
	config.MaxFrameSize = maxMessageSize
	config.MaxDataFrameCount = maxMessageSize / 256
	config.Text = func(frames wlgows.DataFrames) {
		message, err := readMessage(conn, frames)
		if err != nil {
			listener.PauseListen(err) // Listen 會回傳 err
			return
		}
		log.Printf("received: %s", message)
	}
	listener.SetConfig(config)

	go conn.StartPingLoop(30*time.Second, nil) // 心跳；會自動停止

	conn.SendText([]byte("Hello, WebSocket!"), wlgows.SendDataConfig{})

	if err := listener.Listen(); err != nil {
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		log.Println("closing:", err)
	}
}
```

兩端都不必處理 masking：每個 handshake 都會告訴 `Conn` 它是哪一端，所以 client 送出
的每個 frame 都會 mask，server 則一個都不會（RFC 6455 5.1）。

## 說明文件

| 文件 | 內容 |
|---|---|
| **[SERVER.zh-TW.md](./SERVER.zh-TW.md)** | 接受連線、handshake 與它的拒絕回應、從 `net/http` 或 Gin hijack、TLS、subprotocol、壓縮 |
| **[CLIENT.zh-TW.md](./CLIENT.zh-TW.md)** | 建立連線、TLS、handshake、加上 header、subprotocol、壓縮 |
| **[READING.zh-TW.md](./READING.zh-TW.md)** | 讀取 message：限制大小、error 與對應的 close code、壓縮的 message、一次讀一個 frame、read buffer |
| **[LISTENER.zh-TW.md](./LISTENER.zh-TW.md)** | read loop：標準 Listener、hook、上限、error、偵測已斷線的對方、暫停 |
| **[SENDING.zh-TW.md](./SENDING.zh-TW.md)** | 傳送：完整 message、streaming、壓縮、control frame、定期送出 ping、write buffer、lock |
| **[PERMESSAGE_DEFLATE.zh-TW.md](./PERMESSAGE_DEFLATE.zh-TW.md)** | 壓縮：每個設定欄位、context takeover、記憶體、自己壓縮大型資料 |
| **[BENCHMARK_COMPARISON.zh-TW.md](./BENCHMARK_COMPARISON.zh-TW.md)** | 與 gorilla/websocket、gobwas/ws 比較：每個數字，以及 client 送出為何落後 |

## 範例

三個 server 以三種方式執行同一支 echo 程式，client 可以連任何一個。streaming 範例
自成一組。

| 範例 | |
|---|---|
| [`hijack_http`](./example/hijack_http/main.go) | `net/http` 的 echo server，使用 `http.Hijacker` |
| [`hijack_gin`](./example/hijack_gin/main.go) | Gin 的 echo server，使用 `c.Writer.Hijack()`。`GET /ping` 依然正常回傳 JSON，與 WebSocket 並存 |
| [`echo`](./example/echo/main.go) | 原始 TCP 的 echo server，使用 `wlgows.Run` 與 `Accept`。request 有加上逾時，但沒有大小上限。TLS 由 `crypto/tls` 提供 |
| [`client`](./example/client/main.go) | 互動式 client。輸入一行文字即送出，輸入 `exit` 會正常關閉連線 |
| [`stream_server`](./example/stream_server/main.go) | 逐一接收 streaming message 的 frame 並直接寫入磁碟。使用 `Data` hook，適合大到無法放進記憶體的 message。壓縮過的 message 也一樣逐個 frame 解壓縮 |
| [`stream_client`](./example/stream_client/main.go) | 將一個檔案當作單一 binary message 串流送出，一次一個 chunk |

每個範例都在自己的資料夾裡執行。先啟動 server，再另開一個終端機啟動 client：

```bash
cd example/echo && go run .     # 或 hijack_http、hijack_gin
cd example/client && go run .   # 然後開始輸入
```

```bash
cd example/stream_server && go run .
cd example/stream_client && go run .
```

每支程式啟動時會問幾個問題，直接按 Enter 即可使用預設值。

- **壓縮由 client 決定**：client 提議哪種模式，server 都會接受。
- **Subprotocol**：server 會從 client 的提議中，挑第一個自己有支援的。都沒有支援
  的話，連線一樣會建立，只是不使用 subprotocol。
- **Stream client** 可以只送幾個 chunk 就停下來，看看 server 怎麼處理一則永遠不會
  結束的 message。

兩端都會印出協商結果。整個檔案送完後，stream server 會回傳它算出的 SHA-256，
stream client 再拿來和自己送出的內容比對。

`example/` 裡的憑證僅供測試，請勿用於正式環境。

每個範例都使用 [`Listener`](./LISTENER.zh-TW.md)，它會回應 ping 與 close，
並擋下不合法的 frame。每個檔案剩下的是你自己要寫的部分：hook。

## 測試

statement coverage 97.6%。Autobahn：server 與 client 都是 510 OK、0 失敗。見
[Autobahn](./example/AUTOBAHN.zh-TW.md#我們的結果)。

```bash
go test -race ./...
```

## 授權

MIT

## 從 v6 升級

v7 直接從你的 `bufio.Reader` 讀 frame header，所以一個 frame 只配置它自己與 payload。
Listener 的上限也改了名。傳送、兩邊的 handshake、`NewConn` 與 `NewListener` 現在都要
傳入一份設定。以下會編譯失敗：

| v6 | v7 |
|---|---|
| `go get .../wlgows/v6` | `go get github.com/weilun-shrimp/wlgows/v7` |
| Go 1.22 以上 | Go 1.24 以上：從 1.24 起，`crypto/rand.Read` 不會失敗 |
| `wlgows.GetFrameFromReader(netConn, max)` | 分兩步：`frame, err := wlgows.GetFrameHeaderFromReader(reader)`，自己檢查 `frame.GetDeclaredPayloadSize()`，再 `frame.FillFramePayloadFromReader(reader)`，其中 `reader := bufio.NewReader(netConn)`。每條連線只用同一個 `reader`：它保存著已經讀進來的 bytes。 |
| `conn.GetNextFrame(max)` | 在 `conn.LockRead()` 之下同樣兩步：`conn.GetNextFrameHeader()`，再 `conn.ReadFramePayload(frame)`，最後 `conn.UnlockRead()` |
| `frame.GetMaxPayloadLength()` | `frame.GetDeclaredPayloadSize()` |
| `wlgows.ReadFromReader(reader, length)` | 已移除：`buffer := make([]byte, length)`，再 `io.ReadFull(reader, buffer)` |
| `err := wlgows.FillMaskingKey(&key)` | `wlgows.FillMaskingKey(&key)`：不再回傳任何值 |
| `ListenerConfig{MaxMsgFrameCount: n}` | `ListenerConfig{MaxDataFrameCount: n}` |
| `ListenerConfig{MaxMsgPayloadByteLen: n}` | `ListenerConfig{MaxDataFramesSize: n, MaxFrameSize: n}` |
| `wlgows.ErrMsgFrameCountExceeded` | `wlgows.ErrDataFrameCountExceeded` |
| `wlgows.Frames`、`func(frames wlgows.Frames)` hook | `wlgows.DataFrames`、`func(frames wlgows.DataFrames)` |
| `frames.String()`、`frames.Bytes()` | `reader, err := conn.DataFramesReader(frames)`，接著 `defer reader.Close()`，再 `io.ReadAll(wlgows.NewSizeLimitReader(reader, maxSize))`，其中 `maxSize` 是你接受的 message 大小上限。壓縮過的 message 會由 reader 解壓縮，text 也會檢查是不是 UTF-8。見 [READING.zh-TW.md](./READING.zh-TW.md) |
| `conn.SendText(data, chunkSize)`，`SendBinary` 也一樣 | `conn.SendText(data, wlgows.SendDataConfig{ChunkSize: chunkSize})` |
| `conn.SendData(opcode, payload, chunkSize)` | `conn.SendData(opcode, payload, wlgows.SendDataConfig{ChunkSize: chunkSize})` |
| `conn.StartLongDataTransmission(opcode)` | `conn.StartLongDataTransmission(opcode, wlgows.StartLongDataTransmissionConfig{})` |
| `conn.TransmitData(data)`，`EndLongDataTransmission` 也一樣 | `conn.TransmitData(data, wlgows.TransmitDataConfig{})` |
| `wlgows.NewConn(netConn, reader, writer, maskSendFrame)` | `wlgows.NewConn(netConn, reader, writer, wlgows.NewConnConfig{PeerIsClient: ...})`。意思剛好相反：v6 在 client 端傳 `true`，v7 在 server 端傳 `true` |
| `wlgows.NewListener(conn)` | `wlgows.NewListener(conn, wlgows.NewListenerConfig{PeerIsClient: ..., PermessageDeflateAgreedConfig: ...})`。在 server 端把 `PeerIsClient` 設為 `true`。設錯的話，每個 frame 都會以 `ErrFrameMasked` 或 `ErrFrameNotMasked` 失敗。沒有協商出壓縮時，`PermessageDeflateAgreedConfig` 是 `nil` |
| `wlgows.UpgradeRequest(request)` | `wlgows.UpgradeRequest(request, wlgows.ClientHandShakeConfig{})` |
| `wlgows.ServerHandShake(netConn, reader, writer, request)` | `wlgows.ServerHandShake(netConn, reader, writer, request, wlgows.ServerHandShakeConfig{})` |
| `wlgows.ClientHandShake(netConn, reader, writer, request)` | `wlgows.ClientHandShake(netConn, reader, writer, request, wlgows.ClientHandShakeConfig{})` |
| 你自己的 `ListenerConn`，實作 `GetNextFrame(max)` | 改為 `LockRead()`、`UnlockRead()`、`GetNextFrameHeader()` 與 `ReadFramePayload(frame)`，與 `*Conn` 相同 |

`wlgows.Dial(url, tlsConfig)` 還是可以用。

v7 新增：壓縮（permessage-deflate）、subprotocol、`DialContext`、`ReadMessageInto`
與 `NewSizeLimitReader`。見[說明文件](#說明文件)。壓縮與 subprotocol 預設都不開啟：
零值的 `ClientHandShakeConfig` 兩者都不提議，零值的 `ServerHandShakeConfig` 兩者都不同意。

不會出現編譯錯誤、但行為改變的地方：

- **`io.EOF` 現在只代表 stream 在兩個 frame 之間結束。** 如果 stream 在 frame 的第一個
  byte 之後才結束，會回傳 `io.ErrUnexpectedEOF`。在 v6 中，frame 剛好在前 2 個 bytes
  之後、或剛好在 header 之後被截斷時，回傳的是 `io.EOF`。
- **64 bit 長度的最高位元（most significant bit）為 1 時，會回傳
  `ErrPayloadLengthMSBSet`。** RFC 6455 5.2 禁止這個位元為 1。
  `StandardClosePayloadFor` 已經把它對應到 1002，所以像範例那樣送出它回傳值的迴圈，
  不需要任何修改。在 v6 中，這樣的 frame 會讓 `make` panic。
- **`MaxDataFramesSize` 現在連 frame header 也一起計算。** 每個 data frame 除了
  payload，也會扣掉它 2 到 14 byte 的 header，空的 continuation frame 也一樣。在 v6
  中只計算 payload，所以原本剛好放得下的 message 現在可能會被拒絕。請在你接受的最大
  message 之上多留一點空間。
- **空的 continuation frame 不再被丟掉。** `Text`、`Binary` 和 `Data` 會依抵達順序
  收到這個 message 的每一個 frame，每一個也都會算進 `MaxDataFrameCount`。在 v6 中，
  message 中間的空 frame 會被直接略過。
- **`ClientHandShake` 會拒絕 client 沒有提出的 subprotocol。** 依 RFC 6455 4.1，
  `Sec-WebSocket-Protocol` 不是提出過的值時，會回傳
  `ErrHandshakeResponseSubprotocolNotOffered`；不只一個值時，會回傳
  `ErrHandshakeResponseSubprotocolMultiple`。在 v6 中這兩種都會被接受。
- **`ClientHandShake` 會拒絕 client 沒有提出的 extension。** 依 RFC 6455 4.1，
  `Sec-WebSocket-Extensions` 裡有沒提出過的項目時，會回傳
  `ErrHandshakeResponseExtensionNotOffered`。v6 完全不會讀這個 header。
- **RFC 6455 4.1 或 RFC 7692 5 要求中斷連線時，`ClientHandShake` 會關閉連線。**
  例如 `Upgrade`、`Connection`、`Sec-WebSocket-Accept`、`Sec-WebSocket-Protocol` 或
  `Sec-WebSocket-Extensions` 不正確時，會在回傳 error 之前關閉 `netConn`
  （RFC 6455 7.1.7）。其他 error 不會關閉連線，要由你關閉。完整清單見
  [回傳值](./CLIENT.zh-TW.md#回傳值)。在 v6 中一律不會關閉。`defer netConn.Close()`
  照樣可以用：第二次關閉只會回傳 `net.ErrClosed`。
- **101 回應不再送出 `Sec-WebSocket-Version`。** RFC 6455 4.2.2 沒有把它列在 101 裡；
  server 只有在拒絕某個版本時，才會在 426 中送回它。錯誤地要求 101 帶有它的 client 現在會失敗。
- **`TransmitData` 會把空的 chunk 以空的 frame 送出。** 在 v6 中它會被略過。如果你不想
  送出那個 frame，請像 streaming 範例那樣先檢查長度。
- **`NewStandardListener` 現在會設定上限。** 每個 message 與每個 frame 4 KB、16 個
  frame、60 秒讀取逾時。更大的 message 會讓 `Listen` 回傳
  `ErrFrameByteLengthExceeded`（1009），閒置的對方會逾時。請像[快速開始](#快速開始)那樣調高，
  並以 30 秒以內的間隔送 ping。在 v6 中它不設任何上限。
- **`RFC6455UnknownHook` 會以 `ErrReservedOpcode` 停止 `Listen`。** 在 v6 中它以
  `nil` 暫停。
- **payload 長度不是最短編碼時，會回傳 `ErrPayloadLengthNotMinimal`**（RFC 6455 5.2，
  close 1002）。在 v6 中會被接受。
- **`ServerHandShake` 會拒絕不是 16 byte base64 的 `Sec-WebSocket-Key`**，回 400 與
  `ErrHttpSecWebSocketKeyInvalid`。在 v6 中任何值都會被接受。
