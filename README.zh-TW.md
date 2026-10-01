[English](./README.md) · **繁體中文**

# WLGOWS

一個簡單、直覺且功能完整的 Go WebSocket 函式庫：容易上手，也不對底層協定做任何
隱藏。

下方 Quick Start 的程式碼複製貼上就能執行，是一個完整的 echo server：一個
constructor、一個 hook，再加上 `conn.NewStandardListener()` 自動處理 ping、pong
和 close。需要更多控制時，該有的功能都有：串流傳送比記憶體還大的 message、自行
處理每一個 data frame、依自己的節奏發送 ping，甚至完全手動組出 frame 再送出。
這個函式庫以 frame 為單位運作，而不是包一層抽象，所以沒有任何細節被藏起來：
server 與 client、手動 handshake，以及 RFC 6455 要求由你負責的每一條規則，都
清楚擺在你看得到的地方。

## Features

- **WebSocket Server**：直接在 raw TCP 上執行，搭配 HTTP handshake
- **WebSocket Client**：支援 `ws://` 與 `wss://`
- **TLS/SSL**：兩端皆支援
- **HTTP Hijacking**：支援 `http.Server` 與 Gin
- **Listener**：單一 read loop，依 RFC 6455 驗證每個 frame 後交給你的 hook
- **Frame-level control**：需要時可自行組出 frame 並送出
- **Streaming**：以一個個 fragment 傳送比記憶體還大的 message
- **Memory in your hands**：隨時完整掌控傳送用的記憶體：每次傳送都能自己選 chunk size，也能隨時把 write buffer 釋放到零 —— 見 [SENDING_README.zh-TW.md](./SENDING_README.zh-TW.md#掌控權在你手上)
- **Keepalive**：定期發送 ping，每次的 payload 由你決定
- **Concurrent sending**：以 lock 保護，且 control frame 不會被卡在大型 message
  後面

## Contents

- [Quick Start](#quick-start) — [Server](#server) · [Client](#client) · [TLS](#tls-wss) · [HTTP Hijacking](#http-hijacking)
- [Examples](#examples) — 三個 echo server、一個 client，以及一組 streaming 範例
- [從 v4 升級至 v5](#從-v4-升級至-v5)
- [Design](#design)
- [記憶體 vs. 速度](#記憶體-vs-速度自己決定-reader-的大小) — 自行決定 reader 的大小
- [Reading](#reading) — 使用 `Listener`，或一次讀取一個 frame
- [Sending](#sending) — 完整 message、control frame、streaming、自訂 frame
- [Keepalive](#keepalive) — 定期 ping，以及如何偵測對方已無回應
- [Concurrency](#concurrency) — 每個 lock 分別保護什麼
- [Errors](#errors) — sentinel error 與 `StandardClosePayloadFor`
- [Testing](#testing)

`Listener` 有獨立的說明文件：**[LISTENER_README.zh-TW.md](./LISTENER_README.zh-TW.md)**，
傳送也有：**[SENDING_README.zh-TW.md](./SENDING_README.zh-TW.md)**。

## Installation

```bash
go get github.com/weilun-shrimp/wlgows/v5
```

依照 Go 對 major version 2 以上的規定，import path 需要加上 `/v5` 後綴；package
名稱仍然是 `wlgows`，所以程式中的寫法依然是 `wlgows.Dial(...)`。

## Quick Start

### Server

```go
package main

import (
	"bufio"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/weilun-shrimp/wlgows/v5"
)

func main() {
	server, err := wlgows.Run(":8001")
	if err != nil {
		panic(err)
	}
	defer server.Close()

	for {
		netConn, err := server.Accept()
		if err != nil {
			continue
		}
		go handle(netConn)
	}
}

func handle(netConn net.Conn) {
	defer netConn.Close()

	// Accept 只負責取得 TCP 連線，讀取 request 與執行 handshake 都由你處理，
	// 確保每個送上線的 byte 都在你的掌握之中。
	r := bufio.NewReader(netConn) // 每條連線 4096 byte；想縮小請見「記憶體 vs. 速度」
	req, err := http.ReadRequest(r)
	if err != nil {
		return
	}
	conn, _, err := wlgows.ServerHandShake(netConn, r, req)
	if err != nil {
		return
	}

	// ping、pong、close 與保留 opcode 都已處理好，masking 也已依連線端別設定完成。
	listener := conn.NewStandardListener()

	// SetConfig 會整份替換設定，所以請從標準 hook 設好的那份開始修改。
	config := listener.GetConfig()
	config.MaxMsgPayloadByteLen = 10 * 1024 * 1024
	config.Text = func(frames wlgows.Frames) {
		conn.SendText(frames.Bytes(), 0) // echo
	}
	listener.SetConfig(config)

	// 心跳，在獨立的 goroutine 中執行，連線結束時會自動停止。若要偵測
	// 「連線還在卻一直不回 pong」的對方，請見 Keepalive。
	go conn.StartPingLoop(30*time.Second, nil)

	if err := listener.Listen(); err != nil {
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		log.Println("closing:", err)
	}
}
```

### Client

架構和 server 相同。masking 的方向相反（server 送出的 frame 不做 mask），但這裡
不需要特別處理：`Conn` 知道自己是哪一端，`NewStandardListener` 會直接沿用。

```go
package main

import (
	"bufio"
	"log"
	"time"

	"github.com/weilun-shrimp/wlgows/v5"
)

func main() {
	// Dial 只負責建立連線並組好 request，handshake 由你執行，所以送出前仍可
	// 替 req 加上 header。
	netConn, req, err := wlgows.Dial("ws://localhost:8001", nil)
	if err != nil {
		panic(err)
	}
	defer netConn.Close()

	r := bufio.NewReader(netConn) // 每條連線 4096 byte；想縮小請見「記憶體 vs. 速度」
	conn, _, err := wlgows.ClientHandShake(netConn, r, req)
	if err != nil {
		panic(err)
	}

	listener := conn.NewStandardListener()

	config := listener.GetConfig() // 標準 hook 設好的那份
	config.MaxMsgPayloadByteLen = 10 * 1024 * 1024
	config.Text = func(frames wlgows.Frames) {
		log.Println("received:", frames.String())
	}
	listener.SetConfig(config)

	go conn.StartPingLoop(30*time.Second, nil) // 心跳；會自動停止

	conn.SendText([]byte("Hello, WebSocket!"), 0)

	if err := listener.Listen(); err != nil {
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		log.Println("closing:", err)
	}
}
```

兩端都不必處理 masking：`ClientHandShake` 與 `ServerHandShake` 在建立回傳的
`Conn` 時就會設定好 `maskSendFrame`，因此 client 呼叫 `SendText`、`SendPong`
時會做 mask，server 則不會。

### TLS (wss://)

```go
caCert, _ := os.ReadFile("ca.crt")
caCertPool := x509.NewCertPool()
caCertPool.AppendCertsFromPEM(caCert)

netConn, req, err := wlgows.Dial("wss://localhost:8001", &tls.Config{RootCAs: caCertPool})
```

### HTTP Hijacking

```go
func handler(w http.ResponseWriter, r *http.Request) {
	netConn, bufReader, err := wlgows.HijackFromHttp(w) // 或 HijackFromGin(c)
	if err != nil {
		return
	}
	defer netConn.Close()

	// net/http 已經解析過 r(*http.Request)，不需要再讀一次。
	conn, _, err := wlgows.ServerHandShake(netConn, bufReader, r)
	if err != nil {
		return
	}
	// ... read and send
}
```

## Examples

共有六個範例。三個 server 是同一支 echo 程式，只是以三種不同方式接入，所以互動式
client 可以連到其中任何一個；streaming 範例則自成一組：

| Example | |
|---|---|
| [`echo`](./example/echo/main.go) | 在 raw TCP 上執行的 echo server，使用 `wlgows.Run` 與 `Accept` |
| [`hijack_http`](./example/hijack_http/main.go) | 同一支 server，改掛在 `net/http` 之下，使用 `HijackFromHttp` |
| [`hijack_gin`](./example/hijack_gin/main.go) | 同一支 server，改掛在 Gin 之下，使用 `HijackFromGin`。`GET /ping` 依然正常回傳 JSON，與 WebSocket 並存 |
| [`client`](./example/client/main.go) | 互動式 client。輸入一行文字即送出，輸入 `exit` 會正常關閉連線。啟動時會詢問 CA 路徑，因此也能連線 `wss://` |
| [`stream_server`](./example/stream_server/main.go) | 逐一接收 streaming message 的 frame 並直接寫入磁碟。使用 `Data` hook，適合大到無法放進記憶體的 message |
| [`stream_client`](./example/stream_client/main.go) | 將一個檔案當作單一 binary message 串流送出，一次一個 chunk |

```bash
go run ./example/echo      # 或 hijack_http、hijack_gin
go run ./example/client    # 另開一個終端機，然後開始輸入
```

streaming 範例是獨立的示範，兩端會各自印出 SHA-256，而且兩者會一致：

```bash
go run ./example/stream_server
go run ./example/stream_client   # 另開一個終端機，按兩次 Enter 就會傳送 README.md
```

兩個 hijack server 啟動時都會詢問憑證與金鑰的路徑：直接按兩次 Enter 會以一般的
`ws://` 提供服務，輸入路徑則以 `wss://` 提供服務。

每個範例都使用了 [`Listener`](./LISTENER_README.zh-TW.md)，所以它們會回應 ping、
完成 close handshake、擋下 RFC 6455 不允許的 frame，而這些邏輯都不會出現在範例的
程式碼裡。每個檔案留下的，就只有你本來就要寫的部分：hook。

## 從 v4 升級至 v5

大部分 v4 的程式碼在 v5 下會編譯失敗，而編譯器會指出每一個需要修改的地方。只有一個
改動照樣能編譯，然後卡住：請先讀
[Streaming 的 End 不再釋放連線](#streaming-的-end-不再釋放連線)。

| v4 | v5 |
|---|---|
| `conn.SendText(text)` | `conn.SendText(text, 0)` —— 0 代表以一個 frame 送出，和 v4 相同 |
| `conn.SendBinary(data)` | `conn.SendBinary(data, 0)` |
| `defer conn.EndLongDataTransmission()` | `defer conn.ReleaseLongDataTransmission()`，成功時再 `return conn.EndLongDataTransmission(nil)` |
| `wire := frame.Seal()` | 單次使用：`wire := frame.Seal(nil)`；重複使用：`buffer = frame.Seal(buffer)` |

v5 新增的：

- `conn.SendData(opcode, payload, chunkSize)` 送出一則在執行時才決定 opcode 的
  message。
- `SendText`、`SendBinary` 與 `SendData` 的 `chunkSize`，會把 message 切成每個
  payload 為該 byte 數的 frame。
- `conn.ReleaseLongDataTransmission()` 在串流結束後釋放連線。
- `conn.RenewWriteBuffer(capacity)` 把 `Conn` 的 write buffer 還給 GC。`Conn` 現在
  會把每個 frame 封裝進它保留的同一塊 buffer，所以傳送時不再每次配置記憶體；但在你
  呼叫它之前，這塊 buffer 會一直維持在送過的最大 frame 的大小。見
  [記憶體與 write buffer](./SENDING_README.zh-TW.md#記憶體與-write-buffer)。

### Streaming 的 End 不再釋放連線

在 v4 中，`EndLongDataTransmission` 會送出 FIN 並釋放連線，所以一般的寫法會 defer
它。在 v5 中，它只送出 FIN，釋放連線則由 `ReleaseLongDataTransmission` 負責。

最直覺的一字修改可以編譯，但是錯的：

```go
defer conn.EndLongDataTransmission(nil) // 可以編譯，但永遠不會釋放連線
```

第一次串流會正常運作，但之後這條連線上的每一個 data 傳送都會永遠等下去。請改成：

```go
if err := conn.StartLongDataTransmission(wlgows.OpcodeBinary); err != nil {
	return err
}
defer conn.ReleaseLongDataTransmission()

// ... 每個 chunk 呼叫 TransmitData，出錯就 return ...

return conn.EndLongDataTransmission(nil)
```

另外兩個行為上的改變：

- **失敗的串流不再被當成完整的 message 送達。** 在 v4 中，chunk 失敗後被 defer 的
  End 仍會送出 FIN，所以對方會把被截斷的 message 當成完整的收下。在 v5 中，除非你
  走到 End，否則不會送出 FIN。失敗後請關閉連線。
- **Start 之後什麼都沒送就 End，現在會送出一則空的 message。** v4 什麼都不送。
  RFC 6455 允許空的 message。

## Design

**單一 package。** 所有功能都在根目錄的 `wlgows` package 中：

```go
import "github.com/weilun-shrimp/wlgows/v5"

netConn, req, _ := wlgows.Dial(url, nil)
s, _             := wlgows.Run(":8001")
hjConn, r, _     := wlgows.HijackFromHttp(w)
```

**`Conn` 必須透過 constructor 建立。** 它包含未匯出的相依欄位，若自行撰寫 struct
literal，第一次使用時就會 panic。`ClientHandShake` 與 `ServerHandShake` 已經處理好
這件事：先用 `Dial`、`Server.Accept` 或 `HijackFromHttp` 建立連線（或接受連線、
hijack），再呼叫其中一個函式取得設定完成的 `Conn`。只有直接建立 `Conn` 時需要
特別注意：

```go
c := wlgows.NewConn(netConn, r, false) // false：server 不做 mask（5.1）
```

最後一個參數對應 RFC 6455 5.1，取決於你是哪一端：client 送出的每個 frame 都必須
mask，server 則一律不 mask，對方收到不符規定的 frame 會直接中斷連線。這個值在建立
時就決定好，因此之後任何傳送都不可能設錯。`ClientHandShake` 與 `ServerHandShake`
會自動幫你填入。

`Server` 匯出的欄位（`TCPAddr`、`TCPListener`）可以讀取，也可以修改。

## 記憶體 vs. 速度：自己決定 reader 的大小

上面每個範例使用的 `bufio.NewReader(netConn)`，預設會為每條連線配置 4096 byte 的
buffer。`ClientHandShake`、`ServerHandShake` 與 `NewConn` 都不限制這個大小，你傳入
什麼樣的 `*bufio.Reader` 就使用什麼。如果願意犧牲一點速度，換取每條連線大幅減少的
記憶體用量，只要自行指定大小即可：

```go
r := bufio.NewReaderSize(netConn, 16) // 16 是下限，小於 16 會被 bufio 調整為 16
```

下限 16 並非隨意決定：frame header（2 byte）加上最長的 extended length（8 byte）
再加上 masking key（4 byte）共 14 byte，剛好小於 16。因此無論 buffer 設多大，
frame 中 payload 以外的部分都能一次讀完。buffer 大於 16 的好處，在於較小的
payload 可以跟 header 在同一次讀取中一起進來；一旦 payload 超過 buffer 的剩餘
空間，無論 buffer 多大都需要另外讀取，所以 message 越大，兩者的差距就越小。

記憶體用量會隨同時連線數增加：

| | 16 byte | 4096 byte（預設） |
|---|---|---|
| 每條連線 | 16 B | 4096 B |
| 1,000 條連線 | 約 16 KB | 約 4 MB |
| 100,000 條連線 | 約 1.6 MB | 約 400 MB |

讀取次數則會隨 frame 大小與連續到達的數量增加：

| | 16 byte | 4096 byte（預設） |
|---|---|---|
| 一個 10 B 的 frame | 1 次讀取 | 1 次讀取 |
| 連續 100 個 10 B 的 frame（共 1000 B） | 約 63 次讀取 | 最少 1 次讀取 |
| 一個 1 MB 的 frame | payload 不經過 buffer，兩者讀取次數相同 | payload 不經過 buffer，兩者讀取次數相同 |

（100 個 frame 那一列假設呼叫 `Read` 時資料都已到達，這是穩定串流下的常態；如果
對方傳送得很慢或斷斷續續，差距就會縮小。）

傳送端也有同樣的取捨，只是方向相反 —— 每個 `Conn` 一塊 write buffer，大小由你決定：
見 [記憶體與 write buffer](./SENDING_README.zh-TW.md#記憶體與-write-buffer)。

## Reading

有兩種方式，差別在於你想自己處理多少細節。

**`Listener`** 負責讀取、依 RFC 6455 驗證、將分段的 message 組合起來，再依 opcode
呼叫對應的 hook。它本身從不寫入、也從不關閉連線；RFC 對接收端的每一項要求，最終
都交由某個 hook 處理。`SetConfig` 以複製的方式整份替換設定，隨時都可以呼叫，包括
在 hook 內部：

```go
listener := conn.NewStandardListener() // 或 wlgows.NewListener(conn)，不含任何 hook

config := listener.GetConfig()
config.MaxMsgPayloadByteLen = 10 * 1024 * 1024
config.Text = func(frames wlgows.Frames) { log.Print(frames.String()) }
listener.SetConfig(config)

err := listener.Listen()
```

`NewStandardListener` 跟一般的 Listener 相同，只是已經處理好協定本身規定的部分
（ping、pong、close 與保留 opcode），而且 `PeerIsClient` 會依連線端別自動設定。
這些 hook 每一個都可以替換。

詳細內容請見 **[LISTENER_README.zh-TW.md](./LISTENER_README.zh-TW.md)**，包括 hook、
各項上限、暫停，以及每個 error 的意義。

**`GetNextFrame`** 一次回傳一個 frame，由你自行組合：

```go
var frames wlgows.Frames
for {
	f, err := conn.GetNextFrame(10 * 1024 * 1024) // 0 表示不限制
	if err != nil {
		return err
	}
	frames = append(frames, f)
	if f.FIN {
		break
	}
}
```

一次處理一個 frame，才能把超大的 message 直接串流到其他地方，而不必整個放在記憶體
裡。`Frames` 只在你需要時才組合：

| 呼叫 | 回傳 |
|---|---|
| `frames.String()` | 串接後的 payload，型別為 `string` |
| `frames.Bytes()` | 串接後的 payload，是一份新的 `[]byte`，可自由使用 |
| `frames.ByteLen()` | 串接後的大小，單位為 **byte**，完全不配置記憶體 |

這兩個組合方法都會把所有 fragment 串接起來，所以即使多 byte 的 rune 被 frame 邊界
切開，也能完整還原。`ByteLen` 計算的是 byte 數而非字元數：`"中文字"` 會回傳 9，
而不是 3。

opcode 只存在於第一個 frame，continuation frame 的 opcode 是 0：

```go
switch frames[0].Opcode {
case wlgows.OpcodeText:   // 0x1
case wlgows.OpcodeBinary: // 0x2
case wlgows.OpcodeClose:  // 0x8：停止讀取
case wlgows.OpcodePing:   // 0x9
case wlgows.OpcodePong:   // 0xA
}
```

## Sending

傳送有獨立的說明文件：**[SENDING_README.zh-TW.md](./SENDING_README.zh-TW.md)** ——
該用哪個呼叫、streaming、記憶體管理、失敗處理與 lock。

| 你手上有的是 | 呼叫 |
|---|---|
| 一則已在記憶體中的 message | `SendText(text, chunkSize)`、`SendBinary(data, chunkSize)`、`SendData(opcode, payload, chunkSize)` |
| 一則大到放不進記憶體的 message | `StartLongDataTransmission` → `TransmitData` → `EndLongDataTransmission`，並 defer `ReleaseLongDataTransmission` |
| 一個 close、ping 或 pong | `SendClose(payload)`、`SendPing(payloadData)`、`SendPong(payloadData)` |
| 一個你自行組出的 frame | `SendFrame(frame)` |

```go
conn.SendText([]byte("hello"), 0)  // 一個 frame
conn.SendBinary(data, 4*1024)      // 每個 frame 的 payload 為 4 KB
```

`chunkSize` 是每個 frame 的 payload 大小。0 或以下代表以一個 frame 送出。

**掌控權在你手上，所以記憶體由你管理。** 大多數 package 會在建立連線時就固定
frame 大小；wlgows 則讓每一次傳送自己決定。每個 frame 都會封裝進每個 `Conn` 各自
一塊的 write buffer，它會長到送過的最大 frame 的大小，而且永遠不會自己縮小。用
固定的 `chunkSize` 限制它的上限，並呼叫 `conn.RenewWriteBuffer(capacity)` 把記憶體
還回去 —— 見 [記憶體與 write buffer](./SENDING_README.zh-TW.md#記憶體與-write-buffer)。

**close 之後什麼都不送。** `SendClose` 送出之後，data 傳送會回傳
`ErrCloseAlreadySent`（5.5.1）。

## Keepalive

已經斷線的對方和只是沒有傳送資料的對方，從這一端看起來完全一樣。RFC 6455 5.5.2
以 ping 發出詢問、以 pong 作為回應，所以確認對方是否還在，需要兩個部分：一是定期
發送 ping，二是判斷收到的 pong。

`StartPingLoop` 負責定期發送。它會 block，所以要在哪個 goroutine 執行由你決定：

```go
go conn.StartPingLoop(30*time.Second, nil)   // nil：不帶 payload 的 ping
```

它會自動停止：ping 送不出去時，或任一端已送出 close 之後。它不會重試，所以送失敗
的那個 ping 就是最後一個，你也不需要保存任何 handle。

`payload` 會在每次 ping 時呼叫一次，而不是只呼叫一次。5.5.2 規定對方必須原封不動
地回傳這些 byte，所以讓每次的內容不同，就能分辨收到的 pong 對應哪一個 ping：

```go
var seq atomic.Uint64
go conn.StartPingLoop(30*time.Second, func() []byte {
	return []byte(fmt.Sprintf("ping-%d", seq.Add(1)))
})
```

pong 會帶回同樣的 `ping-4`，所以 `Pong` hook 能判斷它回應的是哪一個 ping。對協定
來說 payload 只是一串 byte，可以是序號、時間戳記，或任何你能在收到時辨認的內容。

**判斷回應需由你自行處理**，這部分函式庫不會代勞。在 `Pong` hook 中記錄時間，與
你自訂的期限比較，超過就關閉連線：

```go
config.Pong = func(*wlgows.Frame) { lastPong.Store(time.Now()) }
```

記錄時間時不要比對 payload。5.5.3 允許對方主動送出 pong，5.5.2 也允許對方在有多個
ping 尚未回應時只回應最新的那一個，所以即使收到的 pong 對不上你送過的任何 ping，
也足以證明對方仍在線上。

底層的 `Loop` 也有匯出，可用於任何週期性工作：它會定期呼叫傳入的 func，直到該 func
表示要停止為止：

```go
wlgows.Loop(func(stop chan<- struct{}) {
	if done() {
		stop <- struct{}{}   // 只送一次；func 回傳後就會檢查
		return
	}
	work()
}, time.Second)
```

## Concurrency

`Conn` 持有三個 `sync.Locker`，預設都是 `*sync.Mutex`：

| Locker | 保護對象 | 使用者 |
|---|---|---|
| `writeLocker` | 同一時間只有一個 frame 在傳送 | 每個 `Send*`，持有時間為傳送一個 frame |
| `dataFramesWriteLocker` | 同一時間只有一個 data message | `SendText`、`SendBinary`、`SendData`，以及 `Start`…`Release` |
| `readLocker` | 同一時間只有一個讀取者 | `GetNextFrame` |

**可以安全地從多個 goroutine 傳送。** 之所以用兩個 lock 而不是一個，是為了避免
pong 被卡在 10 GB 的傳輸後面：control frame 只取得 `writeLocker`，所以能插在
fragment 之間傳送，這正是 5.4 明確允許、5.5.2 所需要的。

**請只用一個 goroutine 讀取。** `readLocker` 能避免兩個讀取者同時讀取同一段
byte，但它們仍然會各自讀到不完整的 frame。

**`conn.Write` 與 `conn.Read` 會略過所有 lock**，因為它們是從內嵌的 `net.Conn`
直接繼承而來。傳送請使用 `Send*` 系列方法。

`Close` 刻意不取得任何 lock：關閉 fd 正是解除卡在已斷線對方上的讀寫動作的方法。

## Errors

每個 error 都以 `%w` 包裝一個 package 層級的 sentinel error，所以請用 `errors.Is`
比對，不要用 `==`：

```go
if _, _, err := wlgows.ServerHandShake(netConn, r, req); errors.Is(err, wlgows.ErrHttpMethodNotAllowed) {
	// ...
}
```

`StandardClosePayloadFor` 會將 `Listen` 回傳的 error 對應到 RFC 6455 7.4.1 規定的
close code：UTF-8 無效為 1007，message 超過上限為 1009，其他協定錯誤為 1002；
無法以 close frame 回應的情況則回傳 `nil`：

```go
if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
	conn.SendClose(payload)
}
```

`nil` 的意思是「無法判定是哪一方的問題」，而不是「請關閉連線」。你自己的 error
也會落在這一類：`PauseListen(err)` 會結束這一輪執行，`Listen` 就回傳這個 error；
如果這一輪讀取是因為關閉連線而結束，它還會和 socket 本身的 error 合併後一起回傳。
這些情況一樣會得到 `nil`，因為這個 package 無法替它不認識的規則決定 close code。
如有需要，請自行先做對應。詳見
[LISTENER_README.zh-TW.md](./LISTENER_README.zh-TW.md) 的 Errors 章節。

## Testing

```bash
go test ./...                              # 全部
go test -race ./...                        # integration test 會啟動 goroutine
go test -cover .                           # 99.7% of statements
go test -bench BenchmarkFramesAssembly .   # benchmark，一般的 go test 不會執行
```

每個原始檔都有對應的 `_test.go`，全部都在 package `wlgows` 內，才能存取 `di`
注入點。有兩個檔案沒有對應的原始檔：

| 檔案 | 內容 |
|---|---|
| [`Fakes_test.go`](./Fakes_test.go) | 共用的測試替身：`fakeConn`（在記憶體中運作的 `net.Conn`）、`fakeIOWriter`/`fakeIOReader`（單純的 `io.Writer`/`io.Reader` 替身）、`fakeLocker`（會計算次數，也會偵測錯誤用法）、`fixedRandRead`、`scriptedReadFromReader` |
| [`ListenerIntegration_test.go`](./ListenerIntegration_test.go) | `Listen` 在真正的 `net.Pipe` 上端對端執行，**不**替換任何 `di` |

integration test 能抓到 unit test 在結構上無法發現的串接錯誤，例如 constructor
漏填某個 `di` 欄位，或某個預設值指向錯誤的 function。

`BenchmarkFramesAssembly` 說明了為什麼處理 70000 byte 的 message 時應該用
`Bytes()`，而不是 `[]byte(String())`：

```
[]byte(String())   10774 ns/op   147458 B/op   2 allocs/op
Bytes()             5722 ns/op    73729 B/op   1 allocs/op
```

時間減半、記憶體也減半：中間那個 `string` 只是為了再轉換一次而存在，完全是多餘的。

### 替換相依

每個會做 I/O 或用到隨機數的 function，都是一層很薄的匯出包裝，底下實作則接收一個
`di` struct（內含 function value）；type 則把相依放在由 constructor 填入的 `di`
欄位中。這些都沒有匯出，所以只有 package 內的測試能存取。純函式（`Frame.Seal`、
`Frames.String`、`ValidateHandShakeRequest`…）沒有注入點，直接測試即可。

```go
// package 層級的 func：傳入 di struct
netConn, req, err := dial("ws://localhost:8001", nil, dialDI{...})

// method：覆寫 constructor 設定好的欄位
conn := NewConn(netConn, bufio.NewReader(netConn), false)
conn.di.newDataFrame = func(config NewFrameConfig) (*Frame, error) { ... }
```

固定隨機來源後，才能驗證 mask 過的輸出；否則 masked frame 的內容每次都不同，無法
寫出固定的預期值：

```go
f, _ := newFrame(NewFrameConfig{PayloadData: []byte("hi"), Opcode: 1, Mask: true, FIN: true},
	newFrameDI{generateMaskingKey: func() ([]byte, error) { return []byte{1, 2, 3, 4}, nil }})
// f.Seal(nil) == []byte{0x81, 0x82, 1, 2, 3, 4, 'h'^1, 'i'^2}
```

有一個測試刻意驗證的是「目前的行為」而非「正確的行為」，並在名稱與註解中清楚說明：

- `TestDial/an_unknown_scheme_yields_a_nil_conn_and_no_error`：`dial` 中的 scheme
  switch 沒有 default 分支。正式環境不會走到這裡，因為前面有
  `ValidateWebsocketUrl` 把關；只有使用較寬鬆的 fake 時才會觸發。

測試覆蓋率最主要的缺口是 `ClientHandShake` 本身的成功路徑：真正的 client 每次呼叫
都會用 `crypto/rand` 產生新的 key，因此無法用固定的 response 內容比對，除非真的
建立一條雙向連線。它所包裝的 `clientHandShake`（DI 那一層）以及成功時會呼叫的
`NewConn`，都各自有 100% 的測試覆蓋率。

## License

MIT
