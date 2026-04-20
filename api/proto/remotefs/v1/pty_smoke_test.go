package remotefsv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPTYTypesPresent(t *testing.T) {
	cf := &ClientFrame{Payload: &ClientFrame_Attach{Attach: &AttachReq{
		SessionId:   "",
		InitialSize: &Resize{Cols: 80, Rows: 24},
		Term:        "xterm-256color",
	}}}
	require.NotNil(t, cf.GetAttach())
	require.Equal(t, uint32(80), cf.GetAttach().GetInitialSize().GetCols())

	sf := &ServerFrame{Payload: &ServerFrame_Error{Error: &Error{
		Kind:    Error_KIND_PROTOCOL,
		Message: "smoke",
	}}}
	require.Equal(t, "smoke", sf.GetError().GetMessage())

	require.NotNil(t, RemotePTY_ServiceDesc.Streams, "RemotePTY service descriptor must register at least one stream")
}
