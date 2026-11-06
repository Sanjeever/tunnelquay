package transport

import (
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/hashicorp/yamux"
	quic "github.com/quic-go/quic-go"
)

func TestErrorKindPreservesWrappedTransportCauses(t *testing.T) {
	for _, tc := range []struct {
		err  error
		kind string
	}{{io.EOF, "eof"}, {os.ErrDeadlineExceeded, "io_timeout"}, {yamux.ErrKeepAliveTimeout, "transport_keepalive_timeout"}, {yamux.ErrConnectionWriteTimeout, "transport_write_timeout"}, {&quic.IdleTimeoutError{}, "transport_idle_timeout"}, {&quic.ApplicationError{ErrorCode: 2, ErrorMessage: "心跳检测失败"}, "application_close"}, {&quic.StreamError{ErrorCode: 1}, "stream_reset"}, {errors.New("其他错误"), "io_error"}} {
		if got := ErrorKind(fmt.Errorf("控制消息接收失败：%w", tc.err)); got != tc.kind {
			t.Errorf("%v: %s，期望 %s", tc.err, got, tc.kind)
		}
	}
}
