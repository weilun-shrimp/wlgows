[English](./SENDING_README.md) · **繁體中文**

# Sending

`Conn` 可以送出完整的 message、一連串的 chunk、control frame，以及你自行組出的
frame。

## 掌控權在你手上

大多數 WebSocket package 會在建立連線時就固定 frame 大小或 buffer 大小，之後每一次
傳送都只能照著它走。

wlgows 把這兩者都交給你，而且它們是兩個獨立的旋鈕：

- **write buffer** 由你提供，每條連線各自一個：建立 `Conn` 時交給它的
  `*bufio.Writer`，之後隨時可以用 `RenewWriter` 更換。它是 `Conn` 為傳送保留的
  唯一一塊記憶體，而且永遠不會長得比你給的更大。見 [記憶體與 write buffer](#記憶體與-write-buffer)。
- **frame 大小** 在每一次傳送時由你決定：`chunkSize`。它決定 message 在哪裡被切成
  frame，從不影響傳送要花多少記憶體。見 [選擇 chunk size](#選擇-chunk-size)。

完整的權力，也是完整的責任：記憶體與速度之間的取捨，就是你選的那個大小。同樣的
掌控權，也讓 server 傳送用的記憶體能壓到每條連線只有 14 byte，代價是寫入次數。見
[最低記憶體的設定方式](#最低記憶體的設定方式)。

## Contents

- [掌控權在你手上](#掌控權在你手上)
- [選擇要用哪個呼叫](#選擇要用哪個呼叫)
- [完整的 message](#完整的-message) — `SendText`、`SendBinary`、`SendData`
- [Streaming](#streaming) — `Start`、`TransmitData`、`End`、`Release`
- [記憶體與 write buffer](#記憶體與-write-buffer) — [更換：`RenewWriter`](#更換renewwriter) · [最低記憶體的設定方式](#最低記憶體的設定方式)
- [選擇 chunk size](#選擇-chunk-size)
- [更快、更省記憶體：用 `unsafe` 送出 string](#更快更省記憶體用-unsafe-送出-string) — 只限清楚自己在做什麼時
- [傳送失敗時](#傳送失敗時)
- [Concurrency](#concurrency)
- [Control frame 與自訂 frame](#control-frame-與自訂-frame)

library 的其他部分在 [main README](./README.zh-TW.md)。

## 選擇要用哪個呼叫

| 你手上有的是 | 呼叫 |
|---|---|
| 一則已在記憶體中的文字 message | `SendText(text, chunkSize)` |
| 一則已在記憶體中的二進位 message | `SendBinary(data, chunkSize)` |
| 一則執行時才決定 opcode 的 message，例如 echo | `SendData(opcode, payload, chunkSize)` |
| 一則大到放不進記憶體的 message | `StartLongDataTransmission` → `TransmitData` → `EndLongDataTransmission`，並 defer `ReleaseLongDataTransmission` |
| 一個 close、ping 或 pong | `SendClose(payload)`、`SendPing(payloadData)`、`SendPong(payloadData)` |
| 一個你自行組出的 frame | `SendFrame(frame)` |

masking 永遠不需要你設定。client 會為每個 frame 加上 mask，server 則一律不加
（RFC 6455 5.1），而 `Conn` 早就知道自己是哪一端。

## 完整的 message

```go
conn.SendText([]byte("hello"), 0)                  // 一個 frame
conn.SendBinary(data, 4*1024)                      // 每個 frame 4 KB
conn.SendData(wlgows.OpcodeText, []byte("abc"), 0) // 和 SendText 一樣，但不檢查 UTF-8
conn.SendData(frames[0].Opcode, frames.Bytes(), 0) // 把收到的內容原樣 echo 回去
```

`chunkSize` 是每個 frame 的 payload 大小，單位是 byte：

- **0 或以下**：整則 message 以一個 frame 送出。
- **正數**：把 message 切成每個 payload 為該 byte 數的 frame。只有最後一個 frame 可能
  比較短，也只有最後一個 frame 帶有 FIN。
- frame header（2 到 14 byte）不算在內。所以同一個 `chunkSize` 在 client 與 server
  上代表的意思完全相同。
- 空的 message 會以一個空的 frame 送出，RFC 6455 允許這麼做。

`SendText` 會在送出任何東西之前，先檢查整則 message 是否為有效的 UTF-8；若不是，
就以 `ErrInvalidUTF8` 拒絕。對方收到無效的文字會關閉連線（8.1）。檢查的對象是整則
message，所以 chunk 的邊界剛好切在某個字元中間也沒關係。`SendBinary` 與 `SendData`
不做任何檢查。

同一則 message 的 frame 會一起進入 write buffer，最後只 flush 一次，所以很多個小
frame 會共用一次 socket 寫入。最後一個 frame 寫出之後，呼叫才會回傳。

**你的 byte 只會被讀取。** client 是在 write buffer 裡對一份複本加上 mask，從不動你
的 slice。所以把同一個 slice 送兩次、送出你之後還要用的 byte，或送出由 string
轉成的 `[]byte`，在任一端都是安全的。

也正是這個保證，讓你可以透過 `unsafe` 完全不複製地送出一個 string。只有在你清楚
自己在做什麼時才這麼做：見 [更快、更省記憶體](#更快更省記憶體用-unsafe-送出-string)。

## Streaming

大到放不進記憶體的 message，就一個 chunk 一個 chunk 地送。每呼叫一次
`TransmitData` 就送出一個 frame。

```go
if err := conn.StartLongDataTransmission(wlgows.OpcodeBinary); err != nil {
	return err
}
defer conn.ReleaseLongDataTransmission()

buffer := make([]byte, 4*1024)
for {
	length, err := file.Read(buffer)
	if length > 0 {
		if err := conn.TransmitData(buffer[:length]); err != nil {
			return err // 沒有 FIN：message 停在未結束的狀態
		}
	}
	if err == io.EOF {
		return conn.EndLongDataTransmission(nil)
	}
	if err != nil {
		return err
	}
}
```

這四個呼叫：

| 呼叫 | 做什麼 |
|---|---|
| `StartLongDataTransmission(opcode)` | 開啟一則文字或二進位 message，並為它佔用連線。 |
| `TransmitData(chunk)` | 把一個 chunk 當作一個 frame 送出。空的 chunk 會以空的 frame 送出。 |
| `EndLongDataTransmission(last)` | 把 `last` 當作最後一個 frame 送出，帶有 FIN。 |
| `ReleaseLongDataTransmission()` | 釋放連線，不送出任何東西。 |

規則：

- **Start 與 Release 就像 `Lock` 與 `Unlock` 一樣成對。** Start 回傳 nil 之後，立刻
  defer Release。絕對不要在沒有 Start 的情況下呼叫 Release，也不要呼叫兩次：就像
  `sync.Mutex.Unlock` 一樣，那會是 fatal error。
- **End 只呼叫一次。** End 之後只剩 Release 可以呼叫，即使 End 失敗也一樣。
- **End 會送出一個 frame。** `End(nil)` 會送出一個空的最後 frame，所以四個 chunk 會
  送出五個 frame。若你知道哪一個是最後的 chunk，就把它交給 End，讓它自己帶上
  FIN。若 End 之前什麼都沒送，它的 data 就是整則 message，即使是空的也一樣。
- **`TransmitData` 一回傳，你的 buffer 就還給你了**：這個 frame 已經 flush 到 socket。
  兩次呼叫之間不會保留任何東西，所以重複讀進同一個 buffer 是安全的。
- **文字必須是有效的 UTF-8。** 只有 `SendText` 會檢查。文字串流的有效性由你負責。
- **同一個 goroutine。** 這四個呼叫都要在呼叫 Start 的那個 goroutine 裡完成。

[`stream_client`](./example/stream_client/main.go) 就是這樣串流傳送檔案，而
[`stream_server`](./example/stream_server/main.go) 則一個 frame 一個 frame 地接收。

## 記憶體與 write buffer

`Conn` 送出的每一個 frame，都會經過同一塊 write buffer：也就是建立 `Conn` 時你交給
它的 `*bufio.Writer`。它的大小就是 buffer 的大小：

```go
serverConn, _, err := wlgows.ServerHandShake(netConn, r, bufio.NewWriterSize(netConn, 16*1024), req) // 16 KB
clientConn, _, err := wlgows.ClientHandShake(netConn, r, bufio.NewWriter(netConn), req)        // bufio 的 4096
```

- **它必須寫到同一條連線。** 請建立在 `netConn` 上。
- **小於 14 byte** 時，它會先被 flush，再換成一個建立在這條連線上的 14 byte 新
  writer。14 是最長的 frame header（2 + 8 extended length + 4 masking key）。若那次
  flush 失敗，你會拿到 error，而沒有 `Conn`。
- 從 `net/http` hijack 而來？傳入 `bufRW.Writer`，沿用 net/http 的 writer。

**它是固定的。** 不管你送什麼，它都不會自己變大，也不會自己變小。`Conn` 從第一個
frame 到最後一個 frame，都剛好持有 writer 那麼大的記憶體用來傳送，所以每條連線的
記憶體在送出任何 message 之前就已確定：

| Writer 大小 | 每條連線 | 100,000 條連線 |
|---|---|---|
| 14 | 14 B | 約 1.4 MB |
| 4096（`bufio.NewWriter`） | 4 KB | 約 400 MB |
| 64 KB | 64 KB | 約 6.4 GB |

比 buffer 大的 frame 仍然會完整送出，只是分成幾段：

- **server** 會把大的 payload 直接從你的 slice 寫出。不複製，buffer 大小幾乎沒有
  影響。
- **client** 必須對送出的內容加上 mask，又從不動你的 slice，所以它在 buffer 裡對一份
  複本加上 mask，一次一個 buffer 的量。這時 buffer 大小就決定了 socket 寫入的次數。

大小換來的是更少的 socket 寫入：放得進 buffer 的 frame 會共用一次寫入。實測的
socket 寫入次數：

| 傳送 | 14，server | 14，client | 4096 | 64 KB |
|---|---|---|---|---|
| 一則 10 B 的 message | 1 | 2 | 1 | 1 |
| 1000 B，每個 frame 10 B（`chunkSize` 10） | 86 | 134 | 1 | 1 |
| 一則 1 MB 的 message，server | 2 | | 2 | 2 |
| 一則 1 MB 的 message，client | | 87,383 | 257 | 17 |

所以 server 用小 buffer 的代價很低：只有大量的小 frame 會感覺到。會送出大 message
的 client 則需要大的 buffer。

### 更換：`RenewWriter`

```go
conn.RenewWriter(bufio.NewWriterSize(conn, 64*1024)) // 之後改用 64 KB 的 buffer
conn.RenewWriter(bufio.NewWriterSize(conn, 14))      // 最小的大小
```

它會先把舊 writer 裡的內容 flush 出去，之後的每個 frame 都改走新的 writer，舊的
交給 GC。新的 writer 和 handshake 收到的一樣處理：小於 14 byte 的會被 flush 後換掉。
什麼時候呼叫，由你決定：

- **大量傳送之前**，在 client 上：較大的 buffer 能減少之後每一則大 message 的寫入
  次數。
- **進入一段長時間的安靜之前**，在持有很多連線的 server 上：小的 buffer 讓閒置的
  連線維持低成本。

怎麼呼叫：

- **任何 goroutine 都可以呼叫。** 它會取得 write lock，所以會等正在寫出的 frame
  寫完。
- **新的 writer 要建立在同一條連線上。** 直接用 `conn` 就可以：它本身就是
  `net.Conn`。
- **它會回傳 error。** flush 失敗時會保留舊的 writer，而這代表 socket 寫入已經失敗
  —— 見 [傳送失敗時](#傳送失敗時)。

### 最低記憶體的設定方式

因為大小由你決定，傳送幾乎可以不花 server 任何記憶體：

```go
conn, _, err := wlgows.ServerHandShake(netConn, r, bufio.NewWriterSize(netConn, 14), req) // 整個 Conn 的生命週期都是 14 byte
```

或者忙碌時用一般大小的 buffer，等連線安靜下來再降到 14：

```go
conn.RenewWriter(bufio.NewWriterSize(conn, 14))
```

對一台持有 100,000 條連線的 server 來說，write buffer 總共約 1.4 MB，而 4096 時約
400 MB。代價是 socket 寫入次數，如上方實測：在 server 上大約每個 frame 一次，大
message 幾乎感覺不到。不要在會送出大 message 的 client 上這麼做：它每次寫入只能
mask 12 byte。

**hijack 之後，先讓 handler 返回。** 從 Go 1.25 起，net/http 會一直持有它自己 4 KB
的 writer，也就是 `bufRW.Writer`，直到 handler 返回。請在另一個 goroutine 裡執行
WebSocket，並讓 handler 先返回；這樣改用 14 byte 的 writer 時，那 4 KB 就會被回收。
不要讓那個 goroutine 保留 `http.ResponseWriter`：它也持有這個 writer。如果 WebSocket
就在 handler 裡執行，改用較小的 writer 不會釋放任何記憶體，請繼續使用
`bufRW.Writer`。Go 1.25 之前，`bufRW.Writer` 是 net/http 不會保留的新 writer，所以
不受這個限制。

## 選擇 chunk size

`chunkSize` 決定 message 在哪裡被切成 frame。它不會改變記憶體用量：write buffer 是
固定的，而交給 `SendData` 的整則 message 本來就已經在記憶體裡。它改變的是：

- **control frame 多快能送出去。** ping、pong 或 close 只能插在一則 message 的兩個
  frame 之間，所以一則以單一 frame 送出的 100 MB message，會讓 pong 等到整個寫完。
  frame 越小，它就能越早插進去。
- **對方的限制。** 太大，一個 frame 可能超過對方對單一 frame 或單一 message 的
  限制。太小，大型 message 會變成很多個 frame，而對方可能會限制一則 message 的
  frame 數量。wlgows 自己的 `Listener` 就可以用 `MaxDataFrameCount` 設定這個上限
  （預設沒有限制），超過時會以 `ErrDataFrameCountExceeded` 拒絕這則 message。請讓
  message 大小 ÷ `chunkSize` 保持在這個上限以下。例如 16 MB 以 4 KB 為一個 chunk
  是 4,096 個 frame，比 [Listener 說明文件](./LISTENER_README.zh-TW.md#configuration)
  範例中使用的 4,000 還多。

對於相較於對方限制很小的 message，用 0 就可以。大的 message，4 KB 到 64 KB 是合理
的範圍。串流的 chunk 就是你在 `TransmitData` 之前讀取用的 buffer。

## 更快、更省記憶體：用 `unsafe` 送出 string

**只有在你完全清楚自己在做什麼時才使用。** 這裡的錯誤，compiler、race detector 和
你的測試都抓不到。它會在之後才出現：程式 crash，或某個 string 突然變了內容，而且
發生的地方離真正的原因很遠。如果不確定，請用 `[]byte(text)`：代價只是多複製一次。

wlgows 本身不使用 `unsafe`。但它保證**從不寫入你送出的 payload**，不管是哪一端：
client 是在 write buffer 裡對複本加上 mask。所以你可以直接把 string 本身的記憶體交給
它，而不是複本，只要**你的**程式也從不寫入它。

```go
payload := unsafe.Slice(unsafe.StringData(text), len(text))
err := conn.SendText(payload, 0)
```

以一則 1 MB 的 text message 實測：

| | 時間 | 配置的記憶體 |
|---|---|---|
| `conn.SendText([]byte(text), 0)` | 約 80–110 µs | 1 MB |
| `conn.SendText(payload, 0)`，透過 `unsafe` | 約 23 µs | 0.3 KB |
| `conn.SendBinary(payload, 0)`，透過 `unsafe` | 約 1.3 µs | 0.3 KB |

省下的就是 `[]byte(text)` 的成本：Go 必須複製 string，才能給你一份可以寫入的 byte。
剩下的 23 µs 裡，約 20 µs 是 `SendText` 的 UTF-8 檢查，真正的傳送只要約 1 µs。

**這也是最省記憶體的做法。** 不用 `unsafe` 時，這個 1 MB 的 string 在送完之前會同時
存在兩份：string 本身和它的複本。用了之後，只有一份。

規則：

- **絕對不要寫入 `payload`。** 它就是那個 string 本身的記憶體。string literal 放在
  唯讀記憶體裡，寫入會讓程式 crash；其他 string 則會在所有用到它的地方悄悄改變。
- **絕對不要把 `payload` 交給其他可能寫入它的程式**，也不要對它 `append`。
- **所有傳送都適用**：`SendText`、`SendBinary`、`SendData`、`TransmitData`、
  `EndLongDataTransmission`、`SendPing`、`SendPong` 與 `SendFrame`。

接收時也能反過來這樣做：見
[不複製地讀取 text](./LISTENER_README.zh-TW.md#不複製地讀取-textunsafe)。

## 傳送失敗時

| Error | 意思 |
|---|---|
| `ErrCloseAlreadySent` | close 已經送出，而 RFC 6455 不允許在 close 之後送出 data frame 或第二個 close（5.5.1）。這個 frame 的任何部分都沒有送出。ping 或 pong 從不因此被拒絕。 |
| `ErrControlFramePayloadTooLong` | close、ping 或 pong 的 payload 超過 125 byte（5.5）。什麼都沒有送出。 |
| `ErrInvalidUTF8` | `SendText` 收到無效的 UTF-8。什麼都沒有送出。 |
| `ErrNotDataFrameOpcode`、`ErrContinuationFrameWithoutMsg` | `Start` 或 `SendData` 收到一個無法開啟 message 的 opcode。沒有取得任何 lock。 |
| `ErrLongDataTransmissionNotStarted` | 在沒有開啟 transmission 時呼叫了 `TransmitData` 或 `End`。 |
| 其他 | 無法取得 masking key，或 socket 寫入失敗。 |

**socket 寫入失敗是無法挽回的。** write buffer 會記住它的第一個 error，所以之後的
每一次傳送都會以同樣的方式失敗，`RenewWriter` 也一樣。請關閉連線。

**message 傳到一半失敗，會讓它停在未結束的狀態。** 前面的 chunk 可能已經送到對方，
而 RFC 6455 沒有提早結束一則 message 的方法。請關閉連線。如果你改送另一則 data
message，對方會看到 protocol error。

在 streaming 中，你也可以在失敗後仍然呼叫 End。對方就會把已送出的部分當成一則完整
的 message 收下。只有在你要的正是這個不完整的 message 時才這麼做。

## Concurrency

- **多個 goroutine 都可以傳送。** data message 一次只送一則：一則 message 從第一個
  frame 到最後一個 frame 都佔用著連線。
- **control frame 仍然能穿插進去。** ping、pong 或 close 只需要等正在寫出的那個
  frame，所以可以在一則長 message 的兩個 chunk 之間送出（5.4、5.5.2）。它會立刻
  flush，連同排在它前面、已經進入 buffer 的 message frame 一起送出。
- **緩慢的串流會擋住其他 data 傳送。** 串流開啟期間，這條連線上的每一個
  `SendText`、`SendBinary` 與 `SendData` 都會等它結束。
- **不要在自己的串流裡送出 data message。** 在你的 `Start` 與 `Release` 之間呼叫
  `SendText`，會永遠等著你自己持有的那個 lock。

## Control frame 與自訂 frame

`SendClose`、`SendPing` 與 `SendPong` 各送出一個 control frame，回傳前就已 flush。
RFC 6455 規定 control frame 的 payload 最多 125 byte（5.5）。close 只會送出一次；
在它之後 ping 或 pong 仍然會送出（5.5.2）。若要定期發送 ping，見
[Keepalive](./README.zh-TW.md#keepalive)。

`SendFrame` 會寫出你用 `NewFrame` 組出的 frame，回傳前就已 flush。`Conn` 知道的
它會替你設定，其餘的交給你：

- **替你設定，直接改在你的 frame 上：** client 每次傳送都會設定 `Mask` 與一把新的
  `MaskingKey`（5.1、5.3），length 欄位依 `len(PayloadData)` 設定，close、ping 或
  pong 會設上 FIN（5.5）。所以你的 frame 會被修改，但 `PayloadData` 從不會。
- **拒絕，且什麼都不寫出：** 超過 125 byte 的 control payload，以及在這一端送出
  close 之後的 data frame 或第二個 close。
- **你的責任：** 分段 message 的順序（5.4）、不讓其他傳送者插進來，以及 opcode 與
  RSV bit，它們會照你設定的樣子送出。

使用之前，請先讀過它的文件。
