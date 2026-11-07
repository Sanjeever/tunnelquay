package transport

import (
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/hashicorp/yamux"
)

func TestErrorKindPreservesWrappedTransportCauses(t *testing.T) {
	for _, tc := range []struct {
		err  error
		kind string
	}{
		{io.EOF, "eof"},
		{os.ErrDeadlineExceeded, "io_timeout"},
		{yamux.ErrKeepAliveTimeout, "transport_keepalive_timeout"},
		{yamux.ErrConnectionWriteTimeout, "transport_write_timeout"},
		{yamux.ErrSessionShutdown, "connection_closed"},
		{errors.New("其他错误"), "io_error"},
	} {
		if got := ErrorKind(fmt.Errorf("控制消息接收失败：%w", tc.err)); got != tc.kind {
			t.Errorf("%v: %s，期望 %s", tc.err, got, tc.kind)
		}
	}
}
