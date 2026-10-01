[English](./SENDING_README.md) · **繁體中文**

# Sending

`Conn` 可以送出完整的 message、一連串的 chunk、control frame，以及你自行組出的
frame。

## 掌控權在你手上

大多數 WebSocket package 會在建立連線時就固定 frame 大小或 buffer 大小，之後每一次
傳送都只能照著它走。

wlgows 不這麼做。沒有任何東西是固定的。每一次傳送都自己決定 frame 的大小，而
`Conn` 什麼時候把記憶體還回去，也由你決定。這兩件事你都握有完整的權力，也負完整的
責任：如果記憶體用量變得難看，那就是你選的大小造成的。兩個習慣可以讓它保持乾淨：

- **選定一個 chunk size，然後固定使用它。** 見 [使用固定的 chunk size](#1-使用固定的-chunk-size)。
- **在對的時機呼叫 `RenewWriteBuffer`。** 見 [需要時呼叫 RenewWriteBuffer](#2-需要時呼叫-renewwritebuffer)。

同樣的掌控權，也讓你能把 server 傳送用的記憶體壓到極低，代價很小：下一次傳送時
多一次記憶體配置。見 [最低記憶體的設定方式](#最低記憶體的設定方式)。

## Contents

- [掌控權在你手上](#掌控權在你手上)
- [選擇要用哪個呼叫](#選擇要用哪個呼叫)
- [完整的 message](#完整的-message) — `SendText`、`SendBinary`、`SendData`
- [Streaming](#streaming) — `Start`、`TransmitData`、`End`、`Release`
- [記憶體與 write buffer](#記憶體與-write-buffer) — 固定的 chunk size、`RenewWriteBuffer`，以及最低記憶體的設定方式
- [傳送失敗時](#傳送失敗時)
- [Concurrency](#concurrency)
- [Control frame 與自訂 frame](#control-frame-與自訂-frame)

library 的其他部分在 [main README](./README.zh-TW.md)。從 v4 升級？見
[從 v4 升級至 v5](./README.zh-TW.md#從-v4-升級至-v5)。

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
| `TransmitData(chunk)` | 把一個 chunk 當作一個 frame 送出。空的 chunk 會被略過。 |
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
- **`TransmitData` 一回傳，你的 buffer 就還給你了。** 兩次呼叫之間不會保留任何東西，
  所以重複讀進同一個 buffer 是安全的。
- **文字必須是有效的 UTF-8。** 只有 `SendText` 會檢查。文字串流的有效性由你負責。
- **同一個 goroutine。** 這四個呼叫都要在呼叫 Start 的那個 goroutine 裡完成。

[`stream_client`](./example/stream_client/main.go) 就是這樣串流傳送檔案，而
[`stream_server`](./example/stream_server/main.go) 則一個 frame 一個 frame 地接收。

## 記憶體與 write buffer

`Conn` 送出的每一個 frame，都會封裝進同一塊 write buffer。這塊 buffer 會長到能容納
送過的最大 **frame**，之後就維持那個大小，永遠不會自己縮小。

這裡沒有任何東西會替你設上限 —— 見 [掌控權在你手上](#掌控權在你手上)。你有兩個工具。

### 1. 使用固定的 chunk size

這是好的做法。buffer 跟著的是最大的 frame，不是最大的 message。所以固定的
`chunkSize` 會把 buffer 的上限壓在 `chunkSize` 加上最多 14 byte 的 header，不管
message 有多大。

| 送出一則 16 MB 的 message | 之後的 write buffer |
|---|---|
| `SendBinary(data, 0)` | 約 16 MB |
| `SendBinary(data, 4*1024)` | 約 4 KB |
| 以 4 KB 的讀取 buffer 串流傳送 | 約 4 KB |

選定一個大小，然後到處都用它：當作 `chunkSize`，也當作 `TransmitData` 之前讀取用
的 buffer。這樣每一次傳送產生的 frame 都一樣，buffer 也維持同樣的大小。

4 KB 是個合理的預設值。請拿它對照對方的限制：

- **太大**：一個 frame 可能超過對方對單一 frame 或單一 message 的限制。
- **太小**：大型 message 會變成很多個 frame。對方可能會限制一則 message 的 frame
  數量。wlgows 自己的 `Listener` 就可以用 `MaxMsgFrameCount` 設定這個上限（預設
  沒有限制），超過時會以 `ErrMsgFrameCountExceeded` 拒絕這則 message。請讓
  message 大小 ÷ `chunkSize` 保持在這個上限以下。例如 16 MB 以 4 KB 為一個 chunk
  是 4,096 個 frame，比 [Listener 說明文件](./LISTENER_README.zh-TW.md#configuration)
  範例中使用的 4,000 還多。

### 2. 需要時呼叫 RenewWriteBuffer

```go
conn.RenewWriteBuffer(4 * 1024) // 換成一塊 4 KB 的新 buffer，舊的交給 GC
conn.RenewWriteBuffer(0)        // 完全不留 buffer，每一個 byte 都還回去
```

什麼時候呼叫，由你決定。適合的時機：

- **送完一個大 frame 之後。** 一則大 message 把 buffer 撐大了，而你預期短時間內
  不會再有另一則。
- **進入一段長時間的安靜之前。** 你知道這條連線有一陣子不會傳送。在同時持有很多
  連線的 server 上，每條閒置連線各自抓著一塊大 buffer，累積起來很可觀。

怎麼呼叫：

- **傳入你平常的 frame 大小**，例如你的 `chunkSize`，這樣下一次傳送就不必再把
  buffer 撐大。或者傳入 0 —— 見下方。
- **不要在同一則 message 的 chunk 之間呼叫。** 下一個 chunk 會立刻把 buffer 撐回去。
- **任何 goroutine 都可以呼叫。** 它會取得 write lock，所以會等正在寫出的 frame
  寫完。

使用固定的 chunk size 時，buffer 永遠不會超過一個 chunk，所以你可能根本不需要呼叫
`RenewWriteBuffer`。

#### 傳入 0 讓記憶體成本降到最低

`RenewWriteBuffer(0)` 會把整塊 write buffer 還回去。在下一次傳送之前，`Conn` 完全
不持有任何寫入用的記憶體。在連線很多、而且大多閒置的 server 上，這能讓 server
保持乾淨：一條閒置的連線在寫入上不花任何成本。

代價是一次記憶體配置：下一次傳送時，buffer 會再長回那個 frame 的大小。不會有任何
東西壞掉，因為 buffer 是動態的，只要 frame 需要就會長大。所以 0 在任何時候傳入都
是安全的；當你知道這條連線接下來會安靜一陣子時，它就是正確的選擇。如果連線馬上
又要傳送，傳入你平常的 frame 大小可以省下那次配置。

### 最低記憶體的設定方式

因為沒有任何東西是固定的，傳送幾乎可以不花 server 任何記憶體。兩個設定一起用：

```go
conn.SendText(text, 4*1024) // 小而固定的 chunk size
conn.RenewWriteBuffer(0)    // 閒置時不留 write buffer
```

- **傳送中：** write buffer 最多是一個 chunk 加上 14 byte 的 header，不管 message
  有多大。
- **閒置時：** write buffer 完全是空的。

所以一條沒在傳送的連線，不花任何寫入用的記憶體。對一台持有 100,000 條、大多閒置
連線的 server 來說，差別就在「什麼都不花」與「100,000 塊 buffer，每塊都是該連線
送過的最大 frame 那麼大」之間。代價是下一次傳送時多一次記憶體配置，所以在記憶體
比這點代價更重要時使用它。

## 傳送失敗時

| Error | 意思 |
|---|---|
| `ErrCloseAlreadySent` | close 已經送出，而 RFC 6455 不允許在 close 之後送出 data frame（5.5.1）。這個 frame 的任何部分都沒有送出。 |
| `ErrInvalidUTF8` | `SendText` 收到無效的 UTF-8。什麼都沒有送出。 |
| `ErrNotDataFrameOpcode`、`ErrContinuationFrameWithoutMsg` | `Start` 或 `SendData` 收到一個無法開啟 message 的 opcode。沒有取得任何 lock。 |
| `ErrLongDataTransmissionNotStarted` | 在沒有開啟 transmission 時呼叫了 `TransmitData` 或 `End`。 |
| 其他 | frame 無法組出，或 socket 寫入失敗。 |

**message 傳到一半失敗，會讓它停在未結束的狀態。** 前面的 chunk 已經送到對方，而
RFC 6455 沒有提早結束一則 message 的方法。請關閉連線。如果你改送另一則 data
message，對方會看到 protocol error。

在 streaming 中，你也可以在失敗後仍然呼叫 End。對方就會把已送出的部分當成一則完整
的 message 收下。只有在你要的正是這個不完整的 message 時才這麼做。

## Concurrency

- **多個 goroutine 都可以傳送。** data message 一次只送一則：一則 message 從第一個
  frame 到最後一個 frame 都佔用著連線。
- **control frame 仍然能穿插進去。** ping、pong 或 close 只需要等正在寫出的那個
  frame，所以可以在一則長 message 的兩個 chunk 之間送出（5.4、5.5.2）。
- **緩慢的串流會擋住其他 data 傳送。** 串流開啟期間，這條連線上的每一個
  `SendText`、`SendBinary` 與 `SendData` 都會等它結束。
- **不要在自己的串流裡送出 data message。** 在你的 `Start` 與 `Release` 之間呼叫
  `SendText`，會永遠等著你自己持有的那個 lock。

## Control frame 與自訂 frame

`SendClose`、`SendPing` 與 `SendPong` 各送出一個 control frame。RFC 6455 規定
control frame 的 payload 最多 125 byte（5.5）。若要定期發送 ping，見
[Keepalive](./README.zh-TW.md#keepalive)。

`SendFrame` 會寫出你用 `NewFrame` 組出的 frame。它幾乎什麼都不檢查：不檢查 close
規則，也不檢查分段 message 的順序，只會把 masking 調整成符合 `Conn` 的設定。使用
之前，請先讀過它的文件。
