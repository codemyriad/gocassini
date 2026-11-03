module gocassini

go 1.24.0

require (
	github.com/gorilla/websocket v1.5.3
	github.com/k2-fsa/sherpa-onnx-go/v2 v2.0.1
	github.com/pion/rtcp v1.2.15
	github.com/pion/rtp v1.8.25
	github.com/pion/webrtc/v4 v4.0.11
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/k2-fsa/sherpa-onnx-go-linux/v2 v2.0.1 // indirect
	github.com/k2-fsa/sherpa-onnx-go-macos/v2 v2.0.1 // indirect
	github.com/k2-fsa/sherpa-onnx-go-windows/v2 v2.0.1 // indirect
	github.com/pion/datachannel v1.5.10 // indirect
	github.com/pion/dtls/v3 v3.0.4 // indirect
	github.com/pion/ice/v4 v4.0.7 // indirect
	github.com/pion/interceptor v0.1.37 // indirect
	github.com/pion/logging v0.2.3 // indirect
	github.com/pion/mdns/v2 v2.0.7 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/sctp v1.8.36 // indirect
	github.com/pion/sdp/v3 v3.0.10 // indirect
	github.com/pion/srtp/v3 v3.0.4 // indirect
	github.com/pion/stun/v3 v3.0.0 // indirect
	github.com/pion/transport/v3 v3.0.7 // indirect
	github.com/pion/turn/v4 v4.0.0 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	golang.org/x/crypto v0.32.0 // indirect
	golang.org/x/net v0.34.0 // indirect
	golang.org/x/sys v0.29.0 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
)

require (
	cassini-annotations v0.0.0
	github.com/abadojack/whatlanggo v1.0.1
	github.com/eliben/go-sentencepiece v0.7.0
	github.com/klauspost/compress v1.18.4
	github.com/yalue/onnxruntime_go v1.31.0
)

replace cassini-annotations => ../cassini-annotations

replace github.com/k2-fsa/sherpa-onnx-go-linux/v2 => github.com/codemyriad/sherpa-onnx-go-linux/v2 v2.0.1-cassini.3

replace github.com/k2-fsa/sherpa-onnx-go-macos/v2 => github.com/codemyriad/sherpa-onnx-go-macos/v2 v2.0.1-cassini.3

replace github.com/k2-fsa/sherpa-onnx-go-windows/v2 => github.com/codemyriad/sherpa-onnx-go-windows/v2 v2.0.1-cassini.3
