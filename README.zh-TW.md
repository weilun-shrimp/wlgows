[English](./README.md) · **繁體中文**

# WLGOWS

一個簡單、直覺、強大且快速的 Go WebSocket 函式庫：送出時零配置，幾乎每項都比
gorilla/websocket 快（[benchmark](./BENCHMARK_COMPARISON.zh-TW.md)）。容易上手，
也不隱藏 WebSocket 的運作方式。

下方的 Quick Start 是一個能執行的 echo server。`conn.NewStandardListener()` 會替你
處理 ping、pong 與 close。需要更多時，你可以串流比記憶體還大的 message、自己處理
每個 frame、依自己的節奏送 ping，或手動組出 frame。handshake 由你自己執行，RFC 6455
要你負責的每條規則都有文件說明。

**正確性優先。** 我們追求最快的程式，但前提是正確且安全：不用 `unsafe`，也不跳過
bounds check。如果你清楚自己在做什麼，你的程式可以更進一步。見
[更快、更省記憶體](./SENDING_README.zh-TW.md#更快更省記憶體用-unsafe-送出-string)。

## Features

- **WebSocket Server**：掛在 `net/http` 或 Gin 之下，或在你清楚自己在做什麼時直接用原始 TCP
- **WebSocket Client**：支援 `ws://` 與 `wss://`
- **TLS/SSL**：兩端皆支援
- **Subprotocols**：兩端都能提出、選擇或要求一個
- **HTTP Hijacking**：支援 `http.Server` 與 Gin
- **Listener**：單一 read loop，依 RFC 6455 驗證每個 frame 後交給你的 hook
- **Frame-level control**：需要時可自行組出 frame 並送出
- **Streaming**：以一個個 fragment 傳送比記憶體還大的 message
- **Memory in your hands**：每條連線的 write buffer 由你提供，最小 14 byte，而且隨時可以更換；每次傳送也能自己選 frame 大小。見 [SENDING_README.zh-TW.md](./SENDING_README.zh-TW.md#掌控權在你手上)
- **Keepalive**：定期發送 ping，每次的 payload 由你決定
- **Concurrent sending**：以 lock 保護，且 control frame 不會被卡在大型 message
  後面

## Contents

- [Installation](#installation)
- [Quick Start](#quick-start) — [Server](#server) · [Client](#client)
- [說明文件](#說明文件) — server、client、讀取、傳送、benchmark
- [Examples](#examples) — 三個 echo server、一個 client，以及一組 streaming 範例
- [從 v6 升級](#從-v6-升級)
- [Testing](#testing)
- [License](#license)

## Installation

```bash
go get github.com/weilun-shrimp/wlgows/v7
```

依照 Go 對 major version 2 以上的規定，import path 需要加上 `/v7` 後綴；package
名稱仍然是 `wlgows`，所以程式中的寫法依然是 `wlgows.DialContext(...)`。

## Quick Start

### Server

```go
package main

import (
	"log"
	"net/http"
	"time"

	"github.com/weilun-shrimp/wlgows/v7"
)

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

	// ping、pong、close 與保留 opcode 都已處理好，masking 也已依連線端別設定完成。
	listener := conn.NewStandardListener()

	// SetConfig 會整份替換設定，所以請從標準 hook 設好的那份開始修改。
	config := listener.GetConfig()
	config.MaxDataFramesSize = 10 * 1024 * 1024
	config.MaxFrameSize = 10 * 1024 * 1024
	config.MaxDataFrameCount = 10 * 1024 * 1024 / 256
	config.Text = func(frames wlgows.DataFrames) {
		conn.SendText(frames.Bytes(), wlgows.SendDataConfig{}) // echo
	}
	listener.SetConfig(config)

	// 心跳，在獨立的 goroutine 中執行，連線結束時會自動停止。若要偵測
	// 「連線還在卻一直不回 pong」的對方，請見 LISTENER_README.zh-TW.md。
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
[HTTP Hijacking](./SERVER_README.zh-TW.md#http-hijacking)。

### Client

你不需要設定 masking：`Conn` 知道自己是哪一端。

```go
package main

import (
	"bufio"
	"context"
	"log"
	"net"
	"time"

	"github.com/weilun-shrimp/wlgows/v7"
)

func main() {
	// DialContext 只負責建立連線並組好 request，handshake 由你執行，所以送出前
	// 仍可替 request 加上 header。ctx 與 dialer 都會限制建立連線的時間，以先到的
	// 為準。詳見 CLIENT_README.zh-TW.md。
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	netConn, request, err := wlgows.DialContext(ctx, "ws://localhost:8001", dialer, nil)
	if err != nil {
		panic(err)
	}
	defer netConn.Close()

	reader := bufio.NewReader(netConn) // 每條連線 4096 byte；想縮小請見 LISTENER_README.zh-TW.md
	conn, _, err := wlgows.ClientHandShake(netConn, reader, bufio.NewWriter(netConn), request, wlgows.ClientHandShakeConfig{}) // 每條連線 4096 byte；見 SENDING_README.zh-TW.md
	if err != nil {
		panic(err)
	}

	listener := conn.NewStandardListener()

	config := listener.GetConfig() // 標準 hook 設好的那份
	config.MaxDataFramesSize = 10 * 1024 * 1024
	config.MaxFrameSize = 10 * 1024 * 1024
	config.MaxDataFrameCount = 10 * 1024 * 1024 / 256
	config.Text = func(frames wlgows.DataFrames) {
		log.Println("received:", frames.String())
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
| **[SERVER_README.zh-TW.md](./SERVER_README.zh-TW.md)** | 接受連線、handshake 與它的拒絕回應、從 `net/http` 或 Gin hijack、TLS、subprotocol |
| **[CLIENT_README.zh-TW.md](./CLIENT_README.zh-TW.md)** | 建立連線、TLS、handshake、加上 header、subprotocol |
| **[LISTENER_README.zh-TW.md](./LISTENER_README.zh-TW.md)** | 讀取：hook、上限、error、偵測已斷線的對方、一次讀一個 frame、read buffer |
| **[SENDING_README.zh-TW.md](./SENDING_README.zh-TW.md)** | 傳送：完整 message、streaming、壓縮、control frame、定期送出 ping、write buffer、lock |
| **[BENCHMARK_COMPARISON.zh-TW.md](./BENCHMARK_COMPARISON.zh-TW.md)** | 與 gorilla/websocket、gobwas/ws 比較：每個數字，以及每一項落後為何保留 |

## Examples

三個 server 以三種方式執行同一支 echo 程式，client 可以連任何一個。streaming 範例
自成一組。

| Example | |
|---|---|
| [`hijack_http`](./example/hijack_http/main.go) | 掛在 `net/http` 之下的 echo server，使用 `http.Hijacker` |
| [`hijack_gin`](./example/hijack_gin/main.go) | 同一支 server，改掛在 Gin 之下，使用 `c.Writer.Hijack()`。`GET /ping` 依然正常回傳 JSON，與 WebSocket 並存 |
| [`echo`](./example/echo/main.go) | 同一支 server，直接用原始 TCP，使用 `wlgows.Run` 與 `Accept`。request 有加上逾時，但沒有大小上限 |
| [`client`](./example/client/main.go) | 互動式 client。輸入一行文字即送出，輸入 `exit` 會正常關閉連線。啟動時會詢問 CA 路徑，因此也能連線 `wss://` |
| [`stream_server`](./example/stream_server/main.go) | 逐一接收 streaming message 的 frame 並直接寫入磁碟。使用 `Data` hook，適合大到無法放進記憶體的 message |
| [`stream_client`](./example/stream_client/main.go) | 將一個檔案當作單一 binary message 串流送出，一次一個 chunk |

範例是獨立的 module，所以要在 `example/` 底下執行：

```bash
cd example
go run ./echo      # 或 hijack_http、hijack_gin
go run ./client    # 另開一個終端機，然後開始輸入
```

streaming 範例兩端各印出一個 SHA-256，兩者會一致：

```bash
cd example
go run ./stream_server
go run ./stream_client   # 另開一個終端機，按兩次 Enter 就會傳送 README.md
```

兩個 hijack server 啟動時會詢問憑證與金鑰。按兩次 Enter 用 `ws://`，輸入路徑用
`wss://`。

每個範例都使用 [`Listener`](./LISTENER_README.zh-TW.md)，它會回應 ping 與 close，
並擋下不合法的 frame。每個檔案剩下的是你自己要寫的部分：hook。

## 從 v6 升級

v7 直接從你的 `bufio.Reader` 讀 frame header，所以一個 frame 只配置它自己與 payload。
Listener 的上限也改了名。其他 `Conn` 程式碼不用改。以下會編譯失敗：

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
| `wlgows.ServerHandShake(netConn, reader, writer, request)` | `wlgows.ServerHandShake(netConn, reader, writer, request, wlgows.ServerHandShakeConfig{})` |
| 你自己的 `ListenerConn`，實作 `GetNextFrame(max)` | 改為 `LockRead()`、`UnlockRead()`、`GetNextFrameHeader()` 與 `ReadFramePayload(frame)`，與 `*Conn` 相同 |

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
- **`ClientHandShake` 遇到 RFC 6455 4.1 判定失敗的回應時，會關閉連線。**
  `Upgrade`、`Connection`、`Sec-WebSocket-Accept` 或 `Sec-WebSocket-Protocol` 不正確時，
  會在回傳 error 之前關閉 `netConn`（7.1.7）。其他 error 仍由你負責關閉。在 v6 中
  一律不會關閉。`defer netConn.Close()` 照樣可以用：第二次關閉只會回傳 `net.ErrClosed`。
- **101 回應不再送出 `Sec-WebSocket-Version`。** RFC 6455 4.2.2 沒有把它列在 101 裡；
  server 只有在拒絕某個版本時，才會在 426 中送回它。錯誤地要求 101 帶有它的 client 現在會失敗。
- **`TransmitData` 會把空的 chunk 以空的 frame 送出。** 在 v6 中它會被略過。如果你不想
  送出那個 frame，請像 streaming 範例那樣先檢查長度。
- **`NewStandardListener` 現在會設定上限。** 每個 message 與每個 frame 4 KB、16 個
  frame、60 秒讀取逾時。更大的 message 會讓 `Listen` 回傳
  `ErrFrameByteLengthExceeded`（1009），閒置的對方會逾時。請像 quick start 那樣調高，
  並以 30 秒以內的間隔送 ping。在 v6 中它不設任何上限。
- **`RFC6455UnknownHook` 會以 `ErrReservedOpcode` 停止 `Listen`。** 在 v6 中它以
  `nil` 暫停。
- **payload 長度不是最短編碼時，會回傳 `ErrPayloadLengthNotMinimal`**（RFC 6455 5.2，
  close 1002）。在 v6 中會被接受。
- **`ServerHandShake` 會拒絕不是 16 byte base64 的 `Sec-WebSocket-Key`**，回 400 與
  `ErrHttpSecWebSocketKeyInvalid`。在 v6 中任何值都會被接受。

## Testing

```bash
go test ./...                                  # 完整測試
go test -race ./...                            # integration test 會啟動 goroutine
go test -cover .                               # 97.6% 的 statement
go test -bench BenchmarkDataFramesAssembly .   # benchmark，一般的 `go test` 不會執行
```

每個原始檔對應一個 `_test.go`，全部都在 package `wlgows` 裡，所以能使用 `di`
替換點。每個做 I/O 或使用亂數的函式，都是一層薄薄的匯出包裝，底下是一個接收 `di`
struct（裡面是函式值）的函式，測試以 fake 直接測那個函式。有三個檔案沒有對應的
原始檔：

| 檔案 | 內容 |
|---|---|
| [`Benchmark_test.go`](./Benchmark_test.go) | 送出與讀取的 benchmark，以及 `TestBenchmarkReport`，BENCHMARK_COMPARISON.zh-TW.md 的數字來源 |
| [`Fakes_test.go`](./Fakes_test.go) | 共用的 fake，例如 `fakeConn`，一個在記憶體中的 `net.Conn` |
| [`ListenerIntegration_test.go`](./ListenerIntegration_test.go) | 透過真正的 `net.Pipe` 端到端測試 `Listen`，**完全不**替換 `di` |

## License

MIT
